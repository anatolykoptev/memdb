package scheduler

// reorganizer_mem_read.go — mem_read handler entry + fine-level orchestrator.
//
// When llmExtractor is available, runs full fine-level processing:
//   1. Fetch WM nodes with full properties                   → reorganizer_mem_read_parse.go
//   2. Concatenate into conversation block                    → reorganizer_mem_read_parse.go
//   3. Fetch dedup candidates (VSET + pgvector)               → reorganizer_mem_read_candidates.go
//   4. One LLM call: ExtractAndDedup(conversation, candidates)
//   5. Content-hash dedup for ADD facts                       → reorganizer_mem_read_dedup.go
//   6. Batch embed ADD/UPDATE facts                           → reorganizer_mem_read_dedup.go
//   7. Apply actions: ADD → insert LTM, UPDATE → merge,
//      DELETE → invalidate+remove                             → reorganizer_mem_read_actions.go
//   8. Entity linking (async goroutine)                       → reorganizer_entities.go
//   9. Delete original WM staging nodes                       → reorganizer_mem_read_cleanup.go
//  10. Episodic summary (async goroutine)                     → reorganizer_episodic.go
//  11. Profiler TriggerRefresh (async)
//
// Falls back to the old llmEnhance path when llmExtractor is nil → reorganizer_mem_read_legacy.go

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// wmInfo holds the extracted fields from WorkingMemory node properties.
type wmInfo struct {
	texts          []string
	sessionID      string
	agentID        string
	processedWMIDs []string
}

// actionCounts holds outcome counters for Step 7.
type actionCounts struct{ inserted, updated, deleted int }

// ProcessRawMemory implements the Go-native mem_read handler.
//
// When llmExtractor is available, runs the full fine-level pipeline (ExtractAndDedup,
// content-hash dedup, entity linking, episodic summary, profiler refresh).
// Falls back to the legacy llmEnhance path when llmExtractor is nil.
func (r *Reorganizer) ProcessRawMemory(ctx context.Context, userID, cubeID string, wmIDs []string) {
	if len(wmIDs) == 0 {
		return
	}
	if r.embedder == nil {
		r.logger.Debug("mem_read: embedder not configured, skipping")
		return
	}

	log := r.logger.With(slog.String("cube_id", cubeID), slog.Int("wm_ids", len(wmIDs)))
	log.Info("mem_read: processing raw WM nodes")

	// Guard: use fine-level pipeline only if llmExtractor is available.
	if r.llmExtractor != nil {
		r.processRawMemoryFine(ctx, userID, cubeID, wmIDs, log)
		return
	}

	// Legacy path: simple llmEnhance per node.
	r.processRawMemoryLegacy(ctx, userID, cubeID, wmIDs, log)
}

// processRawMemoryFine runs the full fine-level pipeline for async mem_read.
func (r *Reorganizer) processRawMemoryFine(ctx context.Context, userID, cubeID string, wmIDs []string, log *slog.Logger) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000")

	fullNodes, err := r.postgres.GetMemoriesByPropertyIDs(ctx, wmIDs)
	if err != nil || len(fullNodes) == 0 {
		log.Warn("mem_read: GetMemoriesByPropertyIDs failed or returned empty",
			slog.Any("error", err), slog.Int("results", len(fullNodes)))
		return
	}

	info := extractWMInfo(fullNodes)
	if len(info.texts) == 0 {
		log.Debug("mem_read: no valid WM texts found")
		return
	}
	conversation := strings.Join(info.texts, "\n")

	candidates := r.fetchMemReadCandidates(ctx, conversation, cubeID, info.agentID, log)
	facts, err := r.llmExtractor.ExtractAndDedup(ctx, conversation, candidates)
	if err != nil {
		log.Warn("mem_read: ExtractAndDedup failed", slog.Any("error", err))
		return
	}
	if len(facts) == 0 {
		log.Debug("mem_read: no facts extracted")
		r.deleteWMNodes(ctx, cubeID, info.processedWMIDs, log)
		return
	}
	log.Info("mem_read: extracted facts", slog.Int("count", len(facts)), slog.String("model", r.llmExtractor.Model()))

	facts = r.filterAddsByContentHash(ctx, facts, cubeID, log)
	embedded := r.embedFacts(ctx, facts, log)

	allNodes, counts := r.applyMemoryActions(ctx, embedded, userID, cubeID, info.agentID, info.sessionID, now, log)
	r.insertAndLinkLTMNodes(ctx, allNodes, info.processedWMIDs, now, log)
	r.linkEntities(embedded, cubeID, now)
	r.deleteWMNodes(ctx, cubeID, info.processedWMIDs, log)

	if info.sessionID != "" {
		r.generateEpisodicSummary(userID, cubeID, info.sessionID, conversation, now)
	}
	if r.profiler != nil {
		r.profiler.TriggerRefresh(cubeID)
	}

	log.Info("mem_read: complete",
		slog.Int("wm_nodes_processed", len(info.processedWMIDs)),
		slog.Int("ltm_inserted", counts.inserted),
		slog.Int("updated", counts.updated),
		slog.Int("deleted", counts.deleted),
	)
}
