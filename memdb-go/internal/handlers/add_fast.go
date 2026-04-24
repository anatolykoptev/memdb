package handlers

// add_fast.go — fast-mode memory add pipeline.
// Responsibility: embed each sliding-window memory, dedup by cosine similarity,
// batch-insert new nodes into Postgres, cleanup old WorkingMemory.
// No LLM calls. Uses: embedder, postgres.
// Helpers (embed, dedup, hash, cache): add_fast_helpers.go

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// fastAddContext holds shared state for the fast-add pipeline to avoid long parameter lists.
type fastAddContext struct {
	cubeID     string
	userID     string // person identity — Phase 2 split from cube_id
	agentID    string
	sessionID  string
	now        string
	info       map[string]any
	customTags []string
}

// nativeFastAddForCube processes fast-mode add for a single cube/user.
// Pipeline:
//  1. Extract sliding-window memories from messages
//  2. Per memory: content-hash dedup → embed → cosine dedup → build nodes
//  3. Batch insert into Postgres
//  4. Cleanup old WorkingMemory
func (h *Handler) nativeFastAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	memories := extractFastMemories(req.Messages, windowSizeFor(req))
	if len(memories) == 0 {
		return nil, nil
	}

	fac := fastAddContext{
		cubeID:     cubeID,
		userID:     *req.UserID,
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
		h.logger.Debug("fast add: skipping exact duplicate by content_hash")
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
		wmID, mem.Text, "WorkingMemory", fac.cubeID, fac.userID, fac.agentID, fac.sessionID, fac.now,
		memInfo, fac.customTags, mem.Sources, "",
	))
	if err != nil {
		return nil, addResponseItem{}, fmt.Errorf("marshal wm properties: %w", err)
	}

	ltID := uuid.New().String()
	ltJSON, err := marshalProps(buildMemoryProperties(
		ltID, mem.Text, mem.MemoryType, fac.cubeID, fac.userID, fac.agentID, fac.sessionID, fac.now,
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
