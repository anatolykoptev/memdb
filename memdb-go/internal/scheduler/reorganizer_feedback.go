package scheduler

// reorganizer_feedback.go — mem_feedback handler: LLM-driven keep/update/remove.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	feedbackLLMMaxTokens = 768 // max_tokens for feedback analysis LLM call
	feedbackErrTruncLen  = 200 // max chars of LLM error output to include in error message
)

// feedbackAction is one action returned by the LLM feedback analysis.
type feedbackAction struct {
	ID      string `json:"id"`
	Action  string `json:"action"`   // "keep" | "update" | "remove"
	NewText string `json:"new_text"` // only for action="update"
}

// ProcessFeedback implements the full Go-native mem_feedback handler.
//
// Pipeline:
//  1. Parse retrieved_memory_ids and feedback_content from JSON payload
//  2. Fetch memory texts from Postgres
//  3. LLM call: analyze feedback against retrieved memories → keep/update/remove actions
//  4. Apply updates (UpdateMemoryNodeFull) and hard-deletes (DeleteByPropertyIDs)
//
// Falls back to RunTargeted (near-duplicate consolidation) if LLM call fails.
// Non-fatal: errors are logged; the method always returns normally.
func (r *Reorganizer) ProcessFeedback(ctx context.Context, cubeID string, ids []string, feedbackContent string) {
	if len(ids) == 0 {
		return
	}

	log := r.logger.With(
		slog.String("cube_id", cubeID),
		slog.Int("memory_ids", len(ids)),
	)

	// Step 1: Fetch memory texts by property UUID.
	nodes, err := r.postgres.GetMemoryByPropertyIDs(ctx, ids, cubeID)
	if err != nil || len(nodes) == 0 {
		log.Warn("mem_feedback: GetMemoryByPropertyIDs failed, falling back to targeted reorg", slog.Any("error", err))
		r.RunTargeted(ctx, cubeID, ids)
		return
	}

	// Step 2: LLM feedback analysis.
	actions, err := r.llmAnalyzeFeedback(ctx, feedbackContent, nodes)
	if err != nil {
		log.Warn("mem_feedback: LLM analysis failed, falling back to targeted reorg", slog.Any("error", err))
		r.RunTargeted(ctx, cubeID, ids)
		return
	}
	if len(actions) == 0 {
		log.Debug("mem_feedback: LLM returned no actions")
		return
	}

	// Step 3: Apply actions.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	updated, removed := 0, 0

	for _, act := range actions {
		switch act.Action {
		case "update":
			if r.applyFeedbackUpdate(ctx, cubeID, act, now, log) {
				updated++
			}
		case "remove":
			if r.applyFeedbackRemove(ctx, cubeID, act.ID, log) {
				removed++
			}
		}
	}

	log.Info("mem_feedback: processing complete",
		slog.Int("updated", updated),
		slog.Int("removed", removed),
	)
}

// llmAnalyzeFeedback calls the LLM to decide keep/update/remove per memory.
func (r *Reorganizer) llmAnalyzeFeedback(ctx context.Context, feedbackContent string, nodes []db.MemNode) ([]feedbackAction, error) {
	type memItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	var items []memItem
	for _, n := range nodes {
		if n.ID != "" && n.Text != "" {
			items = append(items, memItem{ID: n.ID, Text: n.Text})
		}
	}
	if len(items) == 0 {
		return nil, nil
	}

	memoriesJSON, _ := json.Marshal(items)
	msgs := []map[string]string{
		{"role": "system", "content": memFeedbackSystemPrompt},
		{"role": "user", "content": fmt.Sprintf("User feedback:\n%s\n\nMemories shown to the user:\n%s",
			feedbackContent, memoriesJSON)},
	}

	callCtx, cancel := context.WithTimeout(ctx, reorganizerLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, msgs, feedbackLLMMaxTokens)
	if err != nil {
		return nil, err
	}

	raw = stripFences(raw)
	var result struct {
		Actions []feedbackAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("feedback llm parse json (%s): %w", truncate(raw, feedbackErrTruncLen), err)
	}
	return result.Actions, nil
}

// applyFeedbackUpdate embeds new text and updates a memory node. Returns true on success.
func (r *Reorganizer) applyFeedbackUpdate(ctx context.Context, cubeID string, act feedbackAction, now string, log *slog.Logger) bool {
	if act.NewText == "" {
		return false
	}
	embs, err := r.embedder.Embed(ctx, []string{act.NewText})
	if err != nil || len(embs) == 0 || len(embs[0]) == 0 {
		log.Warn("mem_feedback: embed update failed", slog.String("id", act.ID), slog.Any("error", err))
		return false
	}
	if err := r.postgres.UpdateMemoryNodeFull(ctx, act.ID, act.NewText, db.FormatVector(embs[0]), now); err != nil {
		log.Warn("mem_feedback: update failed", slog.String("id", act.ID), slog.Any("error", err))
		return false
	}
	r.evictVSet(ctx, cubeID, act.ID, "mem_feedback: vset evict after update failed (non-fatal)")
	return true
}

// applyFeedbackRemove deletes a memory node. Returns true on success.
func (r *Reorganizer) applyFeedbackRemove(ctx context.Context, cubeID, id string, log *slog.Logger) bool {
	if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{id}, cubeID); err != nil {
		log.Warn("mem_feedback: delete failed", slog.String("id", id), slog.Any("error", err))
		return false
	}
	r.evictVSet(ctx, cubeID, id, "mem_feedback: vset evict after remove failed (non-fatal)")
	return true
}
