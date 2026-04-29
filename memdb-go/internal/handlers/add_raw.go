package handlers

// add_raw.go — raw-mode memory add pipeline.
// Responsibility: take message content as-is (no sliding window, no timestamp prefix),
// embed, dedup, and insert as LongTermMemory nodes. No WorkingMemory nodes, no LLM calls.
// Used for structured data (e.g. experience records) that must not be split or reformatted.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// nativeRawAddForCube processes raw-mode add for a single cube/user.
// Pipeline:
//  1. Extract text + per-msg metadata directly from each message (no windowing)
//  2. Per message: content-hash dedup → embed → cosine dedup → build LTM node
//  3. Batch insert into Postgres
//  4. Write to VSET cache
//
// Each row maps 1:1 to a source message, so per-message metadata
// (chat_time, uuid, agent_id, role) is stamped into properties.info instead
// of a sources[] array. Filterable via existing info-key search predicates.
func (h *Handler) nativeRawAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	items := extractRawItems(req.Messages)
	if len(items) == 0 {
		return nil, nil
	}

	fac := fastAddContext{
		cubeID:          cubeID,
		userID:          *req.UserID, // Phase 2: person identity threaded into raw write path
		agentID:         stringOrEmpty(req.AgentID),
		sessionID:       stringOrEmpty(req.SessionID),
		now:             nowTimestamp(),
		info:            mapOrEmpty(req.Info),
		customTags:      req.CustomTags,
		key:             stringOrEmpty(req.Key),
		observationDate: h.resolveObservationDate(ctx, req.Messages),
	}

	hashes := hashRawItems(items)
	existingHashes := h.filterExistingHashes(ctx, hashes, cubeID)

	var nodes []db.MemoryInsertNode
	var responseItems []addResponseItem
	var embeddings [][]float32

	for i, it := range items {
		node, item, emb, skip, err := h.processRawMemory(ctx, it, hashes[i], existingHashes, fac)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		nodes = append(nodes, node)
		responseItems = append(responseItems, item)
		embeddings = append(embeddings, emb)
	}

	if len(nodes) == 0 {
		return responseItems, nil
	}
	if err := h.postgres.InsertMemoryNodes(ctx, nodes); err != nil {
		return nil, fmt.Errorf("insert nodes: %w", err)
	}
	h.writeRawCache(ctx, cubeID, nodes, responseItems, embeddings)
	// M8 Stream 10 — emit structural edges for the LTM rows just inserted.
	// Raw mode is 1 node per /add item (no WM pair), so refs map 1:1 to nodes.
	h.emitStructuralEdges(ctx, fac, rawBatchRefs(nodes, embeddings, fac.now))
	return responseItems, nil
}

// rawBatchRefs zips raw-mode insert nodes with their embeddings into
// newMemoryRef slices for the structural-edge emitter. Skips entries with
// empty IDs (defence; raw pipeline always sets them).
func rawBatchRefs(nodes []db.MemoryInsertNode, embeddings [][]float32, createdAt string) []newMemoryRef {
	if len(nodes) == 0 {
		return nil
	}
	refs := make([]newMemoryRef, 0, len(nodes))
	for i, n := range nodes {
		if n.ID == "" {
			continue
		}
		var emb []float32
		if i < len(embeddings) {
			emb = embeddings[i]
		}
		refs = append(refs, newMemoryRef{ID: n.ID, CreatedAt: createdAt, Embedding: emb})
	}
	return refs
}

// rawTextItem carries the trimmed message content alongside per-message
// metadata that must survive into properties.info on the resulting LTM row.
// Replaces the prior []string contract (which silently dropped chat_time,
// uuid, agent_id, role).
type rawTextItem struct {
	Text     string
	ChatTime string
	UUID     string
	AgentID  string
	Role     string
}

// extractRawItems returns non-empty messages with per-msg metadata preserved.
func extractRawItems(messages []chatMessage) []rawTextItem {
	out := make([]rawTextItem, 0, len(messages))
	for _, msg := range messages {
		trimmed := strings.TrimSpace(msg.Content)
		if trimmed == "" {
			continue
		}
		out = append(out, rawTextItem{
			Text:     trimmed,
			ChatTime: msg.ChatTime,
			UUID:     msg.UUID,
			AgentID:  msg.AgentID,
			Role:     msg.Role,
		})
	}
	return out
}

// hashRawItems computes content hashes for raw items.
func hashRawItems(items []rawTextItem) []string {
	hashes := make([]string, len(items))
	for i, it := range items {
		hashes[i] = textHash(it.Text)
	}
	return hashes
}

// processRawMemory handles hash dedup, embedding, cosine dedup, and node building for one item.
// Returns (node, item, embedding, skip, err).
func (h *Handler) processRawMemory(
	ctx context.Context,
	it rawTextItem,
	hash string,
	existingHashes map[string]bool,
	fac fastAddContext,
) (db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
	if existingHashes[hash] {
		h.logger.Debug("raw add: skipping exact duplicate by content_hash", slog.String("hash", hash))
		return db.MemoryInsertNode{}, addResponseItem{}, nil, true, nil
	}

	embedding, err := h.embedSingle(ctx, it.Text)
	if err != nil {
		return db.MemoryInsertNode{}, addResponseItem{}, nil, false, err
	}

	if h.isDuplicate(ctx, embedding, fac.cubeID, fac.agentID) {
		return db.MemoryInsertNode{}, addResponseItem{}, nil, true, nil
	}

	memInfo := mergeInfo(fac.info, hash)
	// Per-msg passthrough into info so raw rows carry the same per-message
	// signal as fast/fine rows carry in their sources[]. Empty values stay
	// absent — back-compat with pre-fix corpus and existing search filters.
	if it.ChatTime != "" {
		memInfo["chat_time"] = it.ChatTime
	}
	if it.UUID != "" {
		memInfo["uuid"] = it.UUID
	}
	if it.AgentID != "" {
		memInfo["agent_id"] = it.AgentID
	}
	if it.Role != "" {
		memInfo["role"] = it.Role
	}
	node, item, err := buildRawNode(it.Text, embedding, fac, memInfo)
	if err != nil {
		return db.MemoryInsertNode{}, addResponseItem{}, nil, false, err
	}
	return node, item, embedding, false, nil
}

// buildRawNode constructs a single LongTermMemory node (no WorkingMemory pair).
func buildRawNode(
	text string,
	embedding []float32,
	fac fastAddContext,
	info map[string]any,
) (db.MemoryInsertNode, addResponseItem, error) {
	id := uuid.New().String()
	props := buildNodeProps(memoryNodeProps{
		ID:              id,
		Memory:          text,
		MemoryType:      memTypeLongTerm,
		UserName:        fac.cubeID,
		UserID:          fac.userID, // Phase 2: person identity slot
		AgentID:         fac.agentID,
		SessionID:       fac.sessionID,
		Mode:            modeRaw,
		Now:             fac.now,
		CreatedAt:       fac.now,
		Info:            info,
		CustomTags:      fac.customTags,
		Sources:         nil,
		Background:      "",
		Key:             fac.key,
		ObservationDate: fac.observationDate,
		ExtractionState: extractionStateExtracted,
	})

	propsJSON, err := marshalProps(props)
	if err != nil {
		return db.MemoryInsertNode{}, addResponseItem{}, fmt.Errorf("marshal raw properties: %w", err)
	}

	node := db.MemoryInsertNode{
		ID:             id,
		PropertiesJSON: propsJSON,
		EmbeddingVec:   db.FormatVector(embedding),
	}
	item := addResponseItem{
		Memory:     text,
		MemoryID:   id,
		MemoryType: memTypeLongTerm,
		CubeID:     fac.cubeID,
	}
	return node, item, nil
}

// writeRawCache writes LTM nodes to the VSET hot cache (non-fatal).
// Raw mode has one node per item (no WM/LTM pairs).
func (h *Handler) writeRawCache(
	ctx context.Context,
	cubeID string,
	nodes []db.MemoryInsertNode,
	items []addResponseItem,
	embeddings [][]float32,
) {
	if h.wmCache == nil {
		return
	}
	ts := nowUnix()
	for i, node := range nodes {
		if i < len(embeddings) && len(embeddings[i]) > 0 {
			if err := h.wmCache.VAdd(ctx, cubeID, node.ID, items[i].Memory, embeddings[i], ts); err != nil {
				h.logger.Debug("raw add: vset write failed",
					slog.String("id", node.ID), slog.Any("error", err))
			}
		}
	}
}
