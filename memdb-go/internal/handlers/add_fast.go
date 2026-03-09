package handlers

// add_fast.go — fast-mode memory add pipeline.
// Responsibility: embed each sliding-window memory, dedup by cosine similarity,
// batch-insert new nodes into Postgres, cleanup old WorkingMemory.
// No LLM calls. Uses: embedder, postgres.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// fastAddContext holds shared state for the fast-add pipeline to avoid long parameter lists.
type fastAddContext struct {
	cubeID    string
	agentID   string
	sessionID string
	now       string
	info      map[string]any
	customTags []string
}

// nativeFastAddForCube processes fast-mode add for a single cube/user.
// Pipeline:
//  1. Extract sliding-window memories from messages
//  2. Per memory: content-hash dedup → embed → cosine dedup → build nodes
//  3. Batch insert into Postgres
//  4. Cleanup old WorkingMemory
func (h *Handler) nativeFastAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	memories := extractFastMemories(req.Messages)
	if len(memories) == 0 {
		return nil, nil
	}

	fac := fastAddContext{
		cubeID:     cubeID,
		agentID:    stringOrEmpty(req.AgentID),
		sessionID:  stringOrEmpty(req.SessionID),
		now:        nowTimestamp(),
		info:       mapOrEmpty(req.Info),
		customTags: req.CustomTags,
	}

	hashes := computeHashes(memories)
	existingHashes := h.filterExistingHashes(ctx, hashes, cubeID)

	var allNodes []db.MemoryInsertNode
	var items []addResponseItem
	var wmEmbeddings [][]float32

	for i, mem := range memories {
		nodes, item, embedding, skip, err := h.processFastMemory(ctx, mem, hashes[i], existingHashes, fac)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		allNodes = append(allNodes, nodes...)
		items = append(items, item)
		wmEmbeddings = append(wmEmbeddings, embedding)
	}

	if len(allNodes) == 0 {
		return items, nil
	}
	if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("insert nodes: %w", err)
	}
	h.writeWMCache(ctx, cubeID, allNodes, items, wmEmbeddings)
	h.cleanupWorkingMemory(ctx, cubeID)
	return items, nil
}

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

// processFastMemory processes a single memory through hash dedup, embedding, and cosine dedup.
// Returns (nodes, item, wmEmbedding, skip, err).
func (h *Handler) processFastMemory(
	ctx context.Context,
	mem extractedMemory,
	hash string,
	existingHashes map[string]bool,
	fac fastAddContext,
) ([]db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
	if existingHashes[hash] {
		h.logger.Debug("fast add: skipping exact duplicate by content_hash", slog.String("hash", hash))
		return nil, addResponseItem{}, nil, true, nil
	}

	memInfo := mergeInfo(fac.info, hash)
	embedding, err := h.embedSingle(ctx, mem.Text)
	if err != nil {
		return nil, addResponseItem{}, nil, false, err
	}

	if h.isDuplicate(ctx, embedding, fac.cubeID, fac.agentID) {
		return nil, addResponseItem{}, nil, true, nil
	}

	nodes, item, err := h.buildFastNodes(mem, embedding, fac, memInfo)
	if err != nil {
		return nil, addResponseItem{}, nil, false, err
	}
	return nodes, item, embedding, false, nil
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
	results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, searchTypes, agentID, 1)
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

// buildFastNodes constructs the WM and LTM MemoryInsertNode pair for a memory.
func (h *Handler) buildFastNodes(
	mem extractedMemory,
	embedding []float32,
	fac fastAddContext,
	memInfo map[string]any,
) ([]db.MemoryInsertNode, addResponseItem, error) {
	embeddingStr := db.FormatVector(embedding)
	wmID := uuid.New().String()
	wmJSON, err := marshalProps(buildMemoryProperties(
		wmID, mem.Text, "WorkingMemory", fac.cubeID, fac.agentID, fac.sessionID, fac.now,
		memInfo, fac.customTags, mem.Sources, "",
	))
	if err != nil {
		return nil, addResponseItem{}, fmt.Errorf("marshal wm properties: %w", err)
	}

	ltID := uuid.New().String()
	ltJSON, err := marshalProps(buildMemoryProperties(
		ltID, mem.Text, mem.MemoryType, fac.cubeID, fac.agentID, fac.sessionID, fac.now,
		memInfo, fac.customTags, mem.Sources, workingBinding(wmID),
	))
	if err != nil {
		return nil, addResponseItem{}, fmt.Errorf("marshal lt properties: %w", err)
	}

	nodes := []db.MemoryInsertNode{
		{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: embeddingStr},
		{ID: ltID, PropertiesJSON: ltJSON, EmbeddingVec: embeddingStr},
	}
	item := addResponseItem{Memory: mem.Text, MemoryID: ltID, MemoryType: mem.MemoryType, CubeID: fac.cubeID}
	return nodes, item, nil
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
