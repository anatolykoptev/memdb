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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

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

// pendingFastMemory is a memory that survived hash-dedup and is queued for embedding.
type pendingFastMemory struct {
	mem  extractedMemory
	hash string
}

// nativeFastAddForCube processes fast-mode add for a single cube/user.
// Pipeline:
//  1. Extract sliding-window memories from messages
//  2. Hash dedup against existing rows (skip exact duplicates)
//  3. Single batched embed call for all surviving memories (was: N sequential calls)
//  4. Per memory: cosine dedup → build nodes
//  5. Batch insert into Postgres
//  6. Cleanup old WorkingMemory
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

	pending, texts := selectPendingFastMemories(memories, hashes, existingHashes, h.logger)
	if len(pending) == 0 {
		return nil, nil
	}

	vecs, err := h.batchEmbedFastTexts(ctx, texts)
	if err != nil {
		return nil, err
	}

	allNodes, items, wmEmbeddings, err := h.buildFastBatch(ctx, pending, vecs, fac)
	if err != nil {
		return nil, err
	}

	if len(allNodes) == 0 {
		return items, nil
	}
	if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("insert nodes: %w", err)
	}
	h.writeWMCache(ctx, cubeID, allNodes, items, wmEmbeddings)
	// M8 Stream 10 — emit structural edges (SAME_SESSION / TIMELINE_NEXT /
	// SIMILAR_COSINE_HIGH) for the LTM rows we just inserted. allNodes is
	// laid out as [WM0, LTM0, WM1, LTM1, ...] so odd indices are LTM IDs.
	h.emitStructuralEdges(ctx, fac, fastBatchLTMRefs(allNodes, wmEmbeddings, fac.now))
	h.cleanupWorkingMemory(ctx, cubeID)
	return items, nil
}

// fastBatchLTMRefs extracts (LTM ID, embedding) pairs from the WM/LTM
// interleaved insert batch. Index layout matches buildFastBatch: WM at even
// indices, LTM at odd; wmEmbeddings is parallel to the (WM, LTM) PAIRS, so
// pair index = i/2. createdAt is fac.now — every node in this batch shares
// the ingest timestamp.
func fastBatchLTMRefs(allNodes []db.MemoryInsertNode, wmEmbeddings [][]float32, createdAt string) []newMemoryRef {
	pairs := len(allNodes) / 2
	if pairs == 0 {
		return nil
	}
	refs := make([]newMemoryRef, 0, pairs)
	for i := 0; i+1 < len(allNodes); i += 2 {
		ltm := allNodes[i+1]
		var emb []float32
		if pairIdx := i / 2; pairIdx < len(wmEmbeddings) {
			emb = wmEmbeddings[pairIdx]
		}
		refs = append(refs, newMemoryRef{ID: ltm.ID, CreatedAt: createdAt, Embedding: emb})
	}
	return refs
}

// selectPendingFastMemories filters out memories whose content_hash already exists in the DB.
// Returns the surviving memories and a parallel slice of their texts (for batched embedding).
func selectPendingFastMemories(
	memories []extractedMemory,
	hashes []string,
	existingHashes map[string]bool,
	logger debugLogger,
) ([]pendingFastMemory, []string) {
	pending := make([]pendingFastMemory, 0, len(memories))
	texts := make([]string, 0, len(memories))
	for i, mem := range memories {
		if existingHashes[hashes[i]] {
			if logger != nil {
				logger.Debug("fast add: skipping exact duplicate by content_hash")
			}
			continue
		}
		pending = append(pending, pendingFastMemory{mem: mem, hash: hashes[i]})
		texts = append(texts, mem.Text)
	}
	return pending, texts
}

// batchEmbedFastTexts performs a single Embed call for all pending memory texts.
// Records the batch size to the memdb.add.embed_batch_size histogram.
// Errors out (rather than silently dropping memories) if the result length mismatches input.
func (h *Handler) batchEmbedFastTexts(ctx context.Context, texts []string) ([][]float32, error) {
	addMx().EmbedBatchSize.Record(ctx, float64(len(texts)),
		metric.WithAttributes(modeAttr(modeFast)))
	vecs, err := h.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("batch embed: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("embed result length mismatch: got %d want %d", len(vecs), len(texts))
	}
	return vecs, nil
}

// buildFastBatch runs cosine-dedup against pre-computed embeddings and builds the
// WM/LTM node pairs for memories that survive. Returns parallel slices: nodes (WM+LTM
// pairs in [WM0,LTM0,WM1,LTM1,...] order), addResponseItems, and the WM embeddings
// to push into the VSET hot cache.
func (h *Handler) buildFastBatch(
	ctx context.Context,
	pending []pendingFastMemory,
	vecs [][]float32,
	fac fastAddContext,
) ([]db.MemoryInsertNode, []addResponseItem, [][]float32, error) {
	allNodes := make([]db.MemoryInsertNode, 0, len(pending)*2)
	items := make([]addResponseItem, 0, len(pending))
	wmEmbeddings := make([][]float32, 0, len(pending))

	for j, p := range pending {
		embedding := vecs[j]
		if h.isDuplicate(ctx, embedding, fac.cubeID, fac.agentID) {
			continue
		}
		memInfo := mergeInfo(fac.info, p.hash)
		nodes, item, err := h.buildFastNodes(p.mem, embedding, fac, memInfo)
		if err != nil {
			return nil, nil, nil, err
		}
		allNodes = append(allNodes, nodes...)
		items = append(items, item)
		wmEmbeddings = append(wmEmbeddings, embedding)
	}
	return allNodes, items, wmEmbeddings, nil
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

// debugLogger is the minimal slog surface used by selectPendingFastMemories so
// the helper stays unit-testable without standing up a full *slog.Logger.
type debugLogger interface {
	Debug(msg string, args ...any)
}

// modeAttr returns the OTel attribute used to label add-pipeline metrics by mode.
func modeAttr(mode string) attribute.KeyValue {
	return attribute.String("mode", mode)
}
