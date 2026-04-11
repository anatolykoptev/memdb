package scheduler

// reorganizer_wm_compact.go — WorkingMemory compaction with LLM summarization.
//
// When a cube's WorkingMemory node count exceeds wmCompactThreshold, this module:
//  1. Fetches all WM nodes from Postgres (oldest first)
//  2. Calls LLM to summarize the oldest (count - wmCompactKeepRecent) nodes
//  3. Inserts the summary as an EpisodicMemory LTM node (preserved in long-term storage)
//  4. Deletes the summarized WM nodes + evicts them from the VSET hot cache
//
// Design rationale vs competitors:
//   - Redis AMS: token-based (tiktoken cl100k_base) — model-specific, Python-only dep
//   - MemOS: no WM compaction at all
//   - Our approach: count-based threshold (model-agnostic) + EpisodicMemory promotion
//     (context is NOT lost — it becomes searchable LTM, not discarded)
//
// Trigger: called from periodicReorgLoop for every active cube.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	// wmCompactThreshold is the number of WorkingMemory nodes that triggers compaction.
	// At or above this count: LLM summarize older nodes → EpisodicMemory LTM.
	// Default 50 mirrors Redis AMS default summarization_threshold behaviour (~50 turns).
	wmCompactThreshold = 50

	// wmCompactKeepRecent is the number of most-recent WM nodes to keep after compaction.
	// These are preserved as-is so the current session context is not lost.
	wmCompactKeepRecent = 10

	// wmCompactLLMTimeout is the per-compaction LLM call deadline.
	wmCompactLLMTimeout = 60 * time.Second

	// wmCompactFetchLimit caps how many WM nodes are fetched for summarization.
	wmCompactFetchLimit = 200
)

// CompactWorkingMemory checks if the cube's WM exceeds wmCompactThreshold and,
// if so, summarizes older nodes into an EpisodicMemory LTM node, then deletes them.
//
// Returns (compacted bool, err).
// Non-fatal: safe to call from periodicReorgLoop.
func (r *Reorganizer) CompactWorkingMemory(ctx context.Context, cubeID string) (bool, error) {
	log := r.logger.With(slog.String("cube_id", cubeID))

	// Step 1: count current WM nodes.
	count, err := r.postgres.CountWorkingMemory(ctx, cubeID)
	if err != nil {
		return false, fmt.Errorf("wm compact: count: %w", err)
	}
	if count < int64(wmCompactThreshold) {
		log.Debug("wm compact: below threshold, skipping",
			slog.Int64("count", count),
			slog.Int("threshold", wmCompactThreshold),
		)
		return false, nil
	}

	log.Info("wm compact: threshold exceeded, starting compaction",
		slog.Int64("count", count),
		slog.Int("threshold", wmCompactThreshold),
		slog.Int("keep_recent", wmCompactKeepRecent),
	)

	// Step 2: fetch WM nodes oldest-first for summarization.
	nodes, err := r.postgres.GetWorkingMemoryOldestFirst(ctx, cubeID, wmCompactFetchLimit)
	if err != nil {
		return false, fmt.Errorf("wm compact: fetch: %w", err)
	}
	if len(nodes) <= wmCompactKeepRecent {
		log.Debug("wm compact: not enough nodes to compact after keep_recent")
		return false, nil
	}

	// Split: oldest nodes to summarize, most-recent nodes to keep.
	toSummarize := nodes[:len(nodes)-wmCompactKeepRecent]

	// Step 3: LLM summarize the older nodes.
	summary, err := r.llmSummarizeWM(ctx, cubeID, toSummarize)
	if err != nil {
		return false, fmt.Errorf("wm compact: llm summarize: %w", err)
	}
	if summary == "" {
		log.Debug("wm compact: LLM returned empty summary, skipping deletion")
		return false, nil
	}

	// Step 4: embed the summary for vector search.
	var embStr string
	if r.embedder != nil {
		embedCtx, cancel := context.WithTimeout(ctx, wmRefreshEmbedTimeout)
		defer cancel()
		embs, err := r.embedder.EmbedQuery(embedCtx, summary)
		if err != nil {
			log.Warn("wm compact: embed summary failed, storing without embedding",
				slog.Any("error", err))
		} else if len(embs) > 0 {
			embStr = db.FormatVector(embs)
		}
	}

	// Step 5: insert EpisodicMemory LTM node.
	episodicID := r.generateUUID()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	propsJSON := r.buildEpisodicProps(episodicID, summary, cubeID, cubeID, now, len(toSummarize))

	if err := r.postgres.InsertMemoryNodes(ctx, []db.MemoryInsertNode{
		{ID: episodicID, PropertiesJSON: propsJSON, EmbeddingVec: embStr},
	}); err != nil {
		return false, fmt.Errorf("wm compact: insert episodic: %w", err)
	}

	// Step 6: delete the summarized WM nodes from Postgres.
	ids := make([]string, len(toSummarize))
	for i, n := range toSummarize {
		ids[i] = n.ID
	}

	deleted, err := r.postgres.DeleteByPropertyIDs(ctx, ids, cubeID)
	if err != nil {
		log.Warn("wm compact: delete WM nodes failed", slog.Any("error", err))
	}

	// Step 7: evict from VSET hot cache.
	if r.wmCache != nil {
		if err := r.wmCache.VRemBatch(ctx, cubeID, ids); err != nil {
			log.Debug("wm compact: vset evict failed", slog.Any("error", err))
		}
	}

	log.Info("wm compact: complete",
		slog.Int("summarized", len(toSummarize)),
		slog.Int64("deleted", deleted),
		slog.Int("kept_recent", wmCompactKeepRecent),
		slog.String("episodic_id", episodicID),
	)
	return true, nil
}

// llmSummarizeWM calls the LLM to summarize WM nodes into one paragraph.
func (r *Reorganizer) llmSummarizeWM(ctx context.Context, cubeID string, nodes []db.MemNode) (string, error) {
	if len(nodes) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Working memory notes for session (cube: %s):\n\n", cubeID))
	for i, n := range nodes {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, n.Text))
	}

	llmCtx, cancel := context.WithTimeout(ctx, wmCompactLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(llmCtx, []map[string]string{
		{"role": "system", "content": wmCompactionSystemPrompt},
		{"role": "user", "content": sb.String()},
	}, llmCompactMaxTokens)
	if err != nil {
		return "", fmt.Errorf("llm call: %w", err)
	}

	summary, err := extractJSONSummary(raw)
	if err != nil {
		return "", err
	}
	return summary, nil
}

// extractJSONSummary parses {"summary": "..."} from raw LLM output, stripping markdown fences.
func extractJSONSummary(raw string) (string, error) {
	var result struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err == nil {
		return strings.TrimSpace(result.Summary), nil
	}
	start := strings.Index(raw, "{")
	if start < 0 {
		return "", errors.New("parse llm response: no JSON object found")
	}
	end := strings.LastIndex(raw, "}")
	if end <= start {
		return "", errors.New("parse llm response: malformed JSON object")
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return "", fmt.Errorf("parse llm response: %w", err)
	}
	return strings.TrimSpace(result.Summary), nil
}

// buildEpisodicProps builds the JSONB properties for an EpisodicMemory node.
func (r *Reorganizer) buildEpisodicProps(id, summary, userID, cubeID, now string, sourceCount int) []byte {
	props := map[string]any{
		"id":               id,
		"memory":           summary,
		"memory_type":      "EpisodicMemory",
		"status":           "activated",
		"user_name":        cubeID, // partition key (upstream convention)
		"user_id":          userID, // person identity — Phase 2 split
		"created_at":       now,
		"updated_at":       now,
		"tags":             []string{"mode:compacted"},
		"confidence":       0.95,
		"type":             "episodic",
		"importance_score": 1.0,
		"retrieval_count":  0,
		"info": map[string]any{
			"compacted_from": sourceCount,
			"compacted_at":   now,
		},
	}
	b, _ := json.Marshal(props)
	return b
}
