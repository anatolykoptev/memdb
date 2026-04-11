package handlers

// add_fast_helpers.go — helper functions for the fast-mode add pipeline.
// Covers: hash computation, embedding, cosine dedup, VSET cache writes.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// computeHashes returns content-hash for each memory.
func computeHashes(memories []extractedMemory) []string {
	hashes := make([]string, len(memories))
	for i, mem := range memories {
		hashes[i] = textHash(mem.Text)
	}
	return hashes
}

// filterExistingHashes returns the set of content hashes already in the DB.
// Returns nil on error (caller continues without hash dedup).
func (h *Handler) filterExistingHashes(ctx context.Context, hashes []string, cubeID string) map[string]bool {
	existing, err := h.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		h.logger.Debug("fast add: batch hash check failed (continuing without hash dedup)",
			slog.Any("error", err))
		return nil
	}
	return existing
}

// mergeInfo creates a new info map with content_hash added.
func mergeInfo(base map[string]any, hash string) map[string]any {
	merged := make(map[string]any, len(base)+1)
	for k, v := range base {
		merged[k] = v
	}
	merged["content_hash"] = hash
	return merged
}

// embedSingle embeds a single text, returning the embedding vector.
func (h *Handler) embedSingle(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := h.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, errors.New("embedder returned empty result")
	}
	return embeddings[0], nil
}

// isDuplicate returns true if a near-duplicate already exists in the DB.
func (h *Handler) isDuplicate(ctx context.Context, embedding []float32, cubeID, agentID string) bool {
	searchTypes := []string{"LongTermMemory", "UserMemory", "WorkingMemory"}
	results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, cubeID, searchTypes, agentID, 1)
	if err != nil {
		h.logger.Debug("dedup vector search failed", slog.Any("error", err))
		return false
	}
	if len(results) > 0 && results[0].Score >= dedupThreshold {
		h.logger.Debug("skipping duplicate by cosine similarity", slog.Float64("score", results[0].Score))
		return true
	}
	return false
}

// writeWMCache writes accepted WorkingMemory nodes to the VSET hot cache (non-fatal).
// allNodes layout: [WM0, LTM0, WM1, LTM1, ...] — WM at even indices.
func (h *Handler) writeWMCache(ctx context.Context, cubeID string, allNodes []db.MemoryInsertNode, items []addResponseItem, wmEmbeddings [][]float32) {
	if h.wmCache == nil {
		return
	}
	ts := nowUnix()
	for i := 0; i+1 < len(allNodes); i += 2 {
		wm := allNodes[i]
		itemIdx := i / 2
		if itemIdx >= len(items) {
			break
		}
		if itemIdx < len(wmEmbeddings) && len(wmEmbeddings[itemIdx]) > 0 {
			if err := h.wmCache.VAdd(ctx, cubeID, wm.ID, items[itemIdx].Memory, wmEmbeddings[itemIdx], ts); err != nil {
				h.logger.Debug("fast add: vset write failed", slog.String("id", wm.ID), slog.Any("error", err))
			}
		}
	}
}
