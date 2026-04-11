package handlers

// admin_reprocess.go — POST /product/admin/reprocess
// Re-processes raw conversation-window LTM nodes through the fine extraction pipeline.
// One-time or periodic migration for memories stored by the old fast-mode default.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// reprocessRequest is the JSON body for POST /product/admin/reprocess.
type reprocessRequest struct {
	CubeID string `json:"cube_id"`
	Limit  int    `json:"limit"`
	DryRun bool   `json:"dry_run"`
}

// reprocessResult holds the outcome of a reprocess run.
type reprocessResult struct {
	TotalRaw   int64 `json:"total_raw"`
	Processed  int   `json:"processed"`
	Extracted  int   `json:"extracted"`
	Deleted    int   `json:"deleted"`
	Remaining  int64 `json:"remaining"`
	DurationMs int64 `json:"duration_ms"`
}

const (
	// reprocessBatchSize is how many raw nodes to process per LLM call.
	reprocessBatchSize = 5
	// reprocessDefaultLimit is the default number of raw nodes to process per request.
	reprocessDefaultLimit = 50
	// reprocessMaxLimit caps the maximum nodes per request.
	reprocessMaxLimit = 200
)

// AdminReprocess re-processes raw conversation-window memories through fine extraction.
func (h *Handler) AdminReprocess(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil || h.embedder == nil || h.llmExtractor == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "reprocess requires postgres, embedder, and llm extractor",
		})
		return
	}

	req, ok := h.parseReprocessRequest(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	totalRaw, err := h.postgres.CountRawMemories(ctx, req.CubeID)
	if err != nil {
		h.logger.Error("reprocess: count raw failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "failed to count raw memories",
		})
		return
	}

	if req.DryRun {
		h.writeJSON(w, http.StatusOK, map[string]any{
			"code": 200, "message": "dry run",
			"data": map[string]any{"total_raw": totalRaw, "would_process": min(int64(req.Limit), totalRaw)},
		})
		return
	}

	h.runReprocess(w, ctx, req, totalRaw)
}

// parseReprocessRequest validates and returns the request, writing errors to w.
func (h *Handler) parseReprocessRequest(w http.ResponseWriter, r *http.Request) (reprocessRequest, bool) {
	var req reprocessRequest
	if err := parseJSONBody(r, &req); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "message": "invalid JSON: " + err.Error(),
		})
		return req, false
	}
	if req.CubeID == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "message": "cube_id is required",
		})
		return req, false
	}
	if req.Limit <= 0 {
		req.Limit = reprocessDefaultLimit
	}
	if req.Limit > reprocessMaxLimit {
		req.Limit = reprocessMaxLimit
	}
	return req, true
}

// runReprocess fetches raw nodes and processes them in batches.
func (h *Handler) runReprocess(w http.ResponseWriter, ctx context.Context, req reprocessRequest, totalRaw int64) {
	rawNodes, err := h.postgres.FindRawMemories(ctx, req.CubeID, req.Limit)
	if err != nil {
		h.logger.Error("reprocess: find raw failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "failed to find raw memories",
		})
		return
	}

	if len(rawNodes) == 0 {
		h.writeJSON(w, http.StatusOK, map[string]any{
			"code": 200, "message": "no raw memories to reprocess",
			"data": reprocessResult{TotalRaw: totalRaw},
		})
		return
	}

	h.logger.Info("reprocess: starting",
		slog.String("cube_id", req.CubeID),
		slog.Int("raw_nodes", len(rawNodes)),
		slog.Int64("total_raw", totalRaw),
	)

	start := time.Now()
	var totalExtracted, totalDeleted int
	for i := 0; i < len(rawNodes); i += reprocessBatchSize {
		end := min(i+reprocessBatchSize, len(rawNodes))
		extracted, deleted := h.reprocessBatch(ctx, rawNodes[i:end], req.CubeID)
		totalExtracted += extracted
		totalDeleted += deleted
	}

	result := reprocessResult{
		TotalRaw:   totalRaw,
		Processed:  len(rawNodes),
		Extracted:  totalExtracted,
		Deleted:    totalDeleted,
		Remaining:  totalRaw - int64(totalDeleted),
		DurationMs: time.Since(start).Milliseconds(),
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "reprocess complete", "data": result,
	})

	h.logger.Info("reprocess: complete",
		slog.String("cube_id", req.CubeID),
		slog.Int("processed", result.Processed),
		slog.Int("extracted", totalExtracted),
		slog.Int("deleted", totalDeleted),
		slog.Duration("duration", time.Since(start)),
	)
}

// reprocessBatch processes a batch of raw nodes through fine extraction.
// Returns (factsInserted, rawNodesDeleted).
func (h *Handler) reprocessBatch(ctx context.Context, batch []db.RawMemory, cubeID string) (int, int) {
	log := h.logger.With(slog.String("cube_id", cubeID), slog.Int("batch_size", len(batch)))

	// Concatenate raw texts into a "conversation" for the LLM
	var sb strings.Builder
	rawIDs := make([]string, 0, len(batch))
	for _, node := range batch {
		sb.WriteString(node.Memory)
		sb.WriteString("\n\n")
		rawIDs = append(rawIDs, node.ID)
	}
	conversation := strings.TrimSpace(sb.String())

	// Fetch dedup candidates (existing clean LTM nodes)
	candidates, _ := h.fetchFineCandidates(ctx, conversation, cubeID, "")

	// Extract facts via LLM
	facts, err := h.llmExtractor.ExtractAndDedup(ctx, conversation, candidates)
	if err != nil {
		log.Warn("reprocess: ExtractAndDedup failed", slog.Any("error", err))
		return 0, 0
	}
	if len(facts) == 0 {
		log.Debug("reprocess: no facts extracted, deleting raw nodes")
		h.deleteRawNodes(ctx, rawIDs, cubeID, log)
		return 0, len(rawIDs)
	}

	log.Debug("reprocess: extracted facts",
		slog.Int("count", len(facts)), slog.String("model", h.llmExtractor.Model()))

	// Content-hash dedup → embed → apply actions
	facts = h.filterAddsByContentHash(ctx, facts, cubeID)
	embedded := h.embedFacts(ctx, facts)
	inserted := h.applyReprocessActions(ctx, embedded, cubeID)

	// Delete old raw nodes
	h.deleteRawNodes(ctx, rawIDs, cubeID, log)
	return inserted, len(rawIDs)
}

// applyReprocessActions builds and inserts new LTM nodes from extracted facts.
// Handles ADD/UPDATE/DELETE actions. Returns number of new facts inserted.
func (h *Handler) applyReprocessActions(ctx context.Context, embedded []embeddedFact, cubeID string) int {
	now := nowTimestamp()
	var allNodes []db.MemoryInsertNode
	var inserted int

	for i := range embedded {
		ef := embedded[i]
		f := ef.fact
		switch f.Action {
		case llm.MemSkip:
			continue
		case llm.MemDelete:
			h.applyDeleteAction(ctx, f.TargetID, cubeID)
		case llm.MemUpdate:
			h.applyUpdateAction(ctx, f.TargetID, f.Memory, ef.embVec, now)
		default: // llm.MemAdd
			if node := buildReprocessLTMNode(f, ef.embVec, cubeID, now); node != nil {
				allNodes = append(allNodes, *node)
				inserted++
			}
		}
	}

	if len(allNodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			h.logger.Warn("reprocess: InsertMemoryNodes failed", slog.Any("error", err))
			return 0
		}
	}
	return inserted
}

// buildReprocessLTMNode creates an LTM node for a reprocessed fact.
// Simpler than buildAddNodes: no WM node, no VSET, no sources.
func buildReprocessLTMNode(f llm.ExtractedFact, embVec, cubeID, now string) *db.MemoryInsertNode {
	if embVec == "" || f.Memory == "" {
		return nil
	}
	createdAt := now
	if f.ValidAt != "" {
		createdAt = f.ValidAt
	}
	memType := f.Type
	if memType != memTypeUser {
		memType = memTypeLongTerm
	}
	ltID := uuid.New().String()

	factInfo := map[string]any{}
	if f.ContentHash != "" {
		factInfo["content_hash"] = f.ContentHash
	}
	if f.Confidence > 0 {
		factInfo["confidence"] = f.Confidence
	}
	if f.ValidAt != "" {
		factInfo["valid_at"] = f.ValidAt
	}

	props := map[string]any{
		"id": ltID, "memory": f.Memory, "memory_type": memType,
		// user_name is the cube partition key (upstream MemOS convention; populated from cube_id)
		// user_id falls back to cubeID — admin reprocess has no original person identity.
		"user_name": cubeID, "user_id": cubeID,
		"status": "activated", "created_at": createdAt, "updated_at": now,
		"tags":       append([]string{"mode:reprocess"}, f.Tags...),
		"background": "", "delete_time": "", "delete_record_id": "",
		"confidence": f.Confidence, "type": "fact", "info": factInfo,
		"graph_id":         uuid.New().String(),
		"importance_score": 1.0, "retrieval_count": 0,
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return nil
	}
	return &db.MemoryInsertNode{ID: ltID, PropertiesJSON: propsJSON, EmbeddingVec: embVec}
}

// deleteRawNodes deletes raw memory nodes by their property UUIDs.
func (h *Handler) deleteRawNodes(ctx context.Context, ids []string, cubeID string, log *slog.Logger) {
	deleted, err := h.postgres.DeleteByPropertyIDs(ctx, ids, cubeID)
	if err != nil {
		log.Warn("reprocess: delete raw nodes failed", slog.Any("error", err))
		return
	}
	log.Debug("reprocess: deleted raw nodes", slog.Int64("count", deleted))

	// Evict from VSET if active
	if h.wmCache != nil {
		for _, id := range ids {
			_ = h.wmCache.VRem(ctx, cubeID, id)
		}
	}
}
