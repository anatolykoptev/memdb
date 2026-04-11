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
//  1. Extract text directly from each message's Content (no windowing)
//  2. Per message: content-hash dedup → embed → cosine dedup → build LTM node
//  3. Batch insert into Postgres
//  4. Write to VSET cache
func (h *Handler) nativeRawAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	texts := extractRawTexts(req.Messages)
	if len(texts) == 0 {
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

	hashes := hashRawTexts(texts)
	existingHashes := h.filterExistingHashes(ctx, hashes, cubeID)

	var nodes []db.MemoryInsertNode
	var items []addResponseItem
	var embeddings [][]float32

	for i, text := range texts {
		node, item, emb, skip, err := h.processRawMemory(ctx, text, hashes[i], existingHashes, fac)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		nodes = append(nodes, node)
		items = append(items, item)
		embeddings = append(embeddings, emb)
	}

	if len(nodes) == 0 {
		return items, nil
	}
	if err := h.postgres.InsertMemoryNodes(ctx, nodes); err != nil {
		return nil, fmt.Errorf("insert nodes: %w", err)
	}
	h.writeRawCache(ctx, cubeID, nodes, items, embeddings)
	return items, nil
}

// extractRawTexts returns non-empty Content from each message without any processing.
func extractRawTexts(messages []chatMessage) []string {
	var texts []string
	for _, msg := range messages {
		trimmed := strings.TrimSpace(msg.Content)
		if trimmed != "" {
			texts = append(texts, trimmed)
		}
	}
	return texts
}

// hashRawTexts computes content hashes for raw texts.
func hashRawTexts(texts []string) []string {
	hashes := make([]string, len(texts))
	for i, t := range texts {
		hashes[i] = textHash(t)
	}
	return hashes
}

// processRawMemory handles hash dedup, embedding, cosine dedup, and node building for one text.
// Returns (node, item, embedding, skip, err).
func (h *Handler) processRawMemory(
	ctx context.Context,
	text, hash string,
	existingHashes map[string]bool,
	fac fastAddContext,
) (db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
	if existingHashes[hash] {
		h.logger.Debug("raw add: skipping exact duplicate by content_hash", slog.String("hash", hash))
		return db.MemoryInsertNode{}, addResponseItem{}, nil, true, nil
	}

	embedding, err := h.embedSingle(ctx, text)
	if err != nil {
		return db.MemoryInsertNode{}, addResponseItem{}, nil, false, err
	}

	if h.isDuplicate(ctx, embedding, fac.cubeID, fac.agentID) {
		return db.MemoryInsertNode{}, addResponseItem{}, nil, true, nil
	}

	memInfo := mergeInfo(fac.info, hash)
	node, item, err := buildRawNode(text, embedding, fac, memInfo)
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
		ID:         id,
		Memory:     text,
		MemoryType: memTypeLongTerm,
		UserName:   fac.cubeID,
		UserID:     fac.userID, // Phase 2: person identity slot
		AgentID:    fac.agentID,
		SessionID:  fac.sessionID,
		Mode:       modeRaw,
		Now:        fac.now,
		CreatedAt:  fac.now,
		Info:       info,
		CustomTags: fac.customTags,
		Sources:    nil,
		Background: "",
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
