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
	"sync"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// rawBatchSize is the maximum number of items processed per chunk in the raw
// add pipeline. After each chunk the accumulated nodes are inserted into
// Postgres, written to the VSET cache, and structural edges are emitted —
// bounding peak memory to O(rawBatchSize) instead of O(len(items)).
//
// A single /product/add with 100K items previously allocated all nodes,
// response items, and embeddings into memory before any batch insert (M7 OOM
// root cause). Chunking keeps memory flat regardless of request size.
const rawBatchSize = 1000

// rawNodeInserter abstracts batch node insertion so the raw-add batch loop
// can be unit-tested without a live Postgres. Production uses *db.Postgres.
type rawNodeInserter interface {
	InsertMemoryNodes(ctx context.Context, nodes []db.MemoryInsertNode) error
}

// rawItemProcessor processes a single raw item (embed, dedup, build node).
// Returns (node, item, embedding, skip, err). Extracted as a callback so the
// batch loop can be tested with a stub processor while still exercising the
// real chunking/insert/cache/edge code path.
type rawItemProcessor func(
	ctx context.Context,
	it rawTextItem,
	hash string,
) (db.MemoryInsertNode, addResponseItem, []float32, bool, error)

// rawBatchMetricsOnce lazily registers the memdb.add.raw_batch_count
// histogram, recording how many chunks a single /product/add raw request
// produces. Alert: resource_exhaustion fires when a single request allocates
// >1GB — a high batch count is the leading indicator.
var (
	rawBatchMetricsOnce sync.Once
	rawBatchCountHist   metric.Float64Histogram
)

func rawBatchCountMetric() metric.Float64Histogram {
	rawBatchMetricsOnce.Do(func() {
		h, _ := otel.Meter("memdb-go/add").Float64Histogram(
			"memdb.add.raw_batch_count",
			metric.WithDescription("Number of chunks processed in a single raw-mode /product/add request"),
		)
		rawBatchCountHist = h
	})
	return rawBatchCountHist
}

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

	// Chunk the loop into batches of rawBatchSize to bound peak memory to
	// O(batchSize) instead of O(len(items)). After each chunk: insert nodes,
	// write VSET cache, emit structural edges — then the per-chunk slices are
	// garbage-collected before the next chunk allocates. This prevents the M7
	// OOM where a single 100K-item request allocated all nodes/embeddings
	// before any batch insert.
	processItem := func(ctx context.Context, it rawTextItem, hash string) (db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
		return h.processRawMemory(ctx, it, hash, existingHashes, fac)
	}
	return h.processRawBatches(ctx, items, hashes, processItem, h.postgres, fac)
}

// processRawBatches chunks items into batches of rawBatchSize, processing
// each chunk through processItem (embed, dedup, build node), then inserting
// nodes, writing VSET cache, and emitting structural edges per batch. The
// per-chunk slices are scoped to the loop body so they're eligible for GC
// before the next chunk allocates — keeping peak memory flat regardless of
// total request size.
//
// The inserter parameter abstracts InsertMemoryNodes so the batch loop can be
// unit-tested without a live Postgres (Handler holds *db.Postgres directly,
// no interface — see add_fast_batched_test.go for the same constraint).
func (h *Handler) processRawBatches(
	ctx context.Context,
	items []rawTextItem,
	hashes []string,
	processItem rawItemProcessor,
	inserter rawNodeInserter,
	fac fastAddContext,
) ([]addResponseItem, error) {
	var allResponseItems []addResponseItem
	batchCount := 0

	for start := 0; start < len(items); start += rawBatchSize {
		end := start + rawBatchSize
		if end > len(items) {
			end = len(items)
		}
		batchCount++

		// Pre-allocate per-chunk slices with capacity = chunk size, not total.
		nodes := make([]db.MemoryInsertNode, 0, end-start)
		responseItems := make([]addResponseItem, 0, end-start)
		embeddings := make([][]float32, 0, end-start)

		for i, it := range items[start:end] {
			node, item, emb, skip, err := processItem(ctx, it, hashes[start+i])
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
			continue
		}
		if err := inserter.InsertMemoryNodes(ctx, nodes); err != nil {
			return nil, fmt.Errorf("insert nodes: %w", err)
		}
		h.writeRawCache(ctx, fac.cubeID, nodes, responseItems, embeddings)
		// M8 Stream 10 — emit structural edges for the LTM rows just inserted.
		// Raw mode is 1 node per /add item (no WM pair), so refs map 1:1 to nodes.
		h.emitStructuralEdges(ctx, fac, rawBatchRefs(nodes, embeddings, fac.now))

		allResponseItems = append(allResponseItems, responseItems...)
	}

	rawBatchCountMetric().Record(ctx, float64(batchCount))
	return allResponseItems, nil
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

	if h.isDuplicate(ctx, embedding, fac.cubeID, fac.agentID, fac.sessionID) {
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
