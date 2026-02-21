package scheduler

// reorganizer_feedback.go — mem_feedback handler: LLM-driven keep/update/remove.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
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
			if act.NewText == "" {
				continue
			}
			embs, err := r.embedder.Embed(ctx, []string{act.NewText})
			if err != nil || len(embs) == 0 || len(embs[0]) == 0 {
				log.Warn("mem_feedback: embed update failed", slog.String("id", act.ID), slog.Any("error", err))
				continue
			}
			if err := r.postgres.UpdateMemoryNodeFull(ctx, act.ID, act.NewText, db.FormatVector(embs[0]), now); err != nil {
				log.Warn("mem_feedback: update failed", slog.String("id", act.ID), slog.Any("error", err))
				continue
			}
			if r.wmCache != nil {
				_ = r.wmCache.VRem(ctx, cubeID, act.ID)
			}
			updated++

		case "remove":
			if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{act.ID}, cubeID); err != nil {
				log.Warn("mem_feedback: delete failed", slog.String("id", act.ID), slog.Any("error", err))
				continue
			}
			if r.wmCache != nil {
				_ = r.wmCache.VRem(ctx, cubeID, act.ID)
			}
			removed++
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

	raw, err := r.callLLM(callCtx, msgs, 768)
	if err != nil {
		return nil, err
	}

	raw = stripFences(raw)
	var result struct {
		Actions []feedbackAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("feedback llm parse json (%s): %w", truncate(raw, 200), err)
	}
	return result.Actions, nil
}
