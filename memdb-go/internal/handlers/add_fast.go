package handlers

// add_fast.go — fast-mode memory add pipeline.
// Responsibility: embed each sliding-window memory, dedup by cosine similarity,
// batch-insert new nodes into Postgres, cleanup old WorkingMemory.
// No LLM calls. Uses: embedder, postgres.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

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

	now := nowTimestamp()
	sessionID := stringOrEmpty(req.SessionID)
	info := mapOrEmpty(req.Info)

	// Batch content-hash dedup: compute SHA256 for every candidate, one DB round-trip.
	// Exact duplicates (same normalized text) are skipped without embedding or DB insert.
	hashes := make([]string, len(memories))
	for i, mem := range memories {
		hashes[i] = textHash(mem.Text)
	}
	existingHashes, err := h.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		h.logger.Debug("fast add: batch hash check failed (continuing without hash dedup)",
			slog.Any("error", err))
		existingHashes = nil
	}

	var allNodes []db.MemoryInsertNode
	var items []addResponseItem
	var wmEmbeddings [][]float32 // parallel to items: raw embedding for each accepted WM node

	for i, mem := range memories {
		hash := hashes[i]
		if existingHashes[hash] {
			h.logger.Debug("fast add: skipping exact duplicate by content_hash",
				slog.String("hash", hash))
			continue
		}

		// Merge computed hash into per-memory info copy (avoids mutating shared map).
		memInfo := make(map[string]any, len(info)+1)
		for k, v := range info {
			memInfo[k] = v
		}
		memInfo["content_hash"] = hash

		// Embed
		embeddings, err := h.embedder.Embed(ctx, []string{mem.Text})
		if err != nil {
			return nil, fmt.Errorf("embed: %w", err)
		}
		if len(embeddings) == 0 || len(embeddings[0]) == 0 {
			return nil, fmt.Errorf("embedder returned empty result")
		}
		embedding := embeddings[0]
		embeddingStr := db.FormatVector(embedding)

		// Cosine similarity dedup
		searchTypes := []string{"LongTermMemory", "UserMemory", "WorkingMemory"}
		results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, searchTypes, stringOrEmpty(req.AgentID), 1)
		if err != nil {
			h.logger.Debug("dedup vector search failed", slog.Any("error", err))
		} else if len(results) > 0 && results[0].Score >= dedupThreshold {
			h.logger.Debug("skipping duplicate by cosine similarity",
				slog.Float64("score", results[0].Score),
			)
			continue
		}

		// Build WorkingMemory node
		wmID := uuid.New().String()
		wmJSON, err := marshalProps(buildMemoryProperties(
			wmID, mem.Text, "WorkingMemory", cubeID, stringOrEmpty(req.AgentID), sessionID, now,
			memInfo, req.CustomTags, mem.Sources, "",
		))
		if err != nil {
			return nil, fmt.Errorf("marshal wm properties: %w", err)
		}

		// Build LongTermMemory / UserMemory node
		ltID := uuid.New().String()
		background := workingBinding(wmID)
		ltJSON, err := marshalProps(buildMemoryProperties(
			ltID, mem.Text, mem.MemoryType, cubeID, stringOrEmpty(req.AgentID), sessionID, now,
			memInfo, req.CustomTags, mem.Sources, background,
		))
		if err != nil {
			return nil, fmt.Errorf("marshal lt properties: %w", err)
		}

		allNodes = append(allNodes,
			db.MemoryInsertNode{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: embeddingStr},
			db.MemoryInsertNode{ID: ltID, PropertiesJSON: ltJSON, EmbeddingVec: embeddingStr},
		)
		items = append(items, addResponseItem{
			Memory:     mem.Text,
			MemoryID:   ltID,
			MemoryType: mem.MemoryType,
			CubeID:     cubeID,
		})
		wmEmbeddings = append(wmEmbeddings, embedding)
	}

	if len(allNodes) == 0 {
		return items, nil
	}

	if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("insert nodes: %w", err)
	}

	// Write new WorkingMemory nodes to VSET hot cache (non-fatal).
	// allNodes layout: [WM0, LTM0, WM1, LTM1, ...] — WM at even indices.
	if h.wmCache != nil {
		ts := nowUnix()
		for i := 0; i+1 < len(allNodes); i += 2 {
			wm := allNodes[i]
			// Match WM node text from items (items and WM nodes are in same order).
			itemIdx := i / 2
			if itemIdx >= len(items) {
				break
			}
			if len(wmEmbeddings) > itemIdx && len(wmEmbeddings[itemIdx]) > 0 {
				if err := h.wmCache.VAdd(ctx, cubeID, wm.ID, items[itemIdx].Memory, wmEmbeddings[itemIdx], ts); err != nil {
					h.logger.Debug("fast add: vset write failed",
						slog.String("id", wm.ID), slog.Any("error", err))
				}
			}
		}
	}

	h.cleanupWorkingMemory(ctx, cubeID)
	return items, nil
}
