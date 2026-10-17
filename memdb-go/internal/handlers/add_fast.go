package handlers

// add_fast.go — fast-mode memory add pipeline.
// Responsibility: embed each sliding-window memory, dedup by cosine similarity,
// batch-insert new nodes into Postgres, cleanup old WorkingMemory.
// No LLM calls. Uses: embedder, postgres.
// Helpers (embed, dedup, hash, cache): add_fast_helpers.go

import (
	"context"
	"fmt"
	"os"
	"strings"

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
	// key is the optional caller-supplied stable identifier propagated from
	// fullAddRequest.Key into properties.key. Empty means "no key" (historical
	// default). Validation lives at the request boundary.
	key string

	// observationDate is the in-conversation timestamp (YYYY-MM-DD) of the
	// latest message in the source batch — derived once at the top of each
	// add pipeline via observationDateFromMessages. Empty means "no chat_time
	// on any message"; downstream callers (buildNodeProps) treat empty as
	// "do not emit observation_date prop". M12.1.
	observationDate string
}

// pendingFastMemory is a memory that survived hash-dedup and is queued for embedding.
type pendingFastMemory struct {
	mem  extractedMemory
	hash string
}

// nativeFastAddForCube processes fast-mode add for a single cube/user.
// Pipeline:
//  1. Extract memories — per-message (default) or sliding-window (legacy A/B)
//  2. Hash dedup against existing rows (skip exact duplicates)
//  3. Single batched embed call for all surviving memories (was: N sequential calls)
//  4. Per memory: cosine dedup → build nodes
//  5. Batch insert into Postgres
//  6. Cleanup old WorkingMemory
//
// Granularity is per-message by default — fixes cat-1 simple-fact retrieval
// loss observed when sliding windows averaged out individual facts (see
// extractFastMemoriesPerMessage docstring). Set MEMDB_FAST_GRANULARITY=window
// to fall back to the legacy windowed extractor for A/B comparison.
func (h *Handler) nativeFastAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	memories := selectFastExtractor(req)
	if len(memories) == 0 {
		return nil, nil
	}

	fac := fastAddContext{
		cubeID:          cubeID,
		userID:          *req.UserID,
		agentID:         stringOrEmpty(req.AgentID),
		sessionID:       stringOrEmpty(req.SessionID),
		now:             nowTimestamp(),
		info:            mapOrEmpty(req.Info),
		customTags:      req.CustomTags,
		key:             stringOrEmpty(req.Key),
		observationDate: h.resolveObservationDate(ctx, req.Messages),
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
	// Reject zero vectors — pgvector <=> returns NaN for them, which
	// breaks dedup (NaN < threshold is false → false duplicate →
	// silent data loss). See isZeroVector doc for the ONNX fallback.
	for i, v := range vecs {
		if isZeroVector(v) {
			return nil, fmt.Errorf("embedder returned zero vector for text %q: downstream cosine dedup would produce NaN", texts[i])
		}
	}
	return vecs, nil
}

// buildFastBatch runs cosine-dedup against pre-computed embeddings and builds the
// WM/LTM node pairs for memories that survive. Returns parallel slices: nodes (WM+LTM
// pairs in [WM0,LTM0,WM1,LTM1,...] order), addResponseItems, and the WM embeddings
// to push into the VSET hot cache.
//
// Two-pass shape (since 2026-04-29 — fast no-LLM fields):
//   - Pass 1 selects survivors (cosine-dedup) and pre-allocates the LTM UUIDs.
//     We need the IDs up front so each row's linked_memory_ids can point at the
//     next survivor's LTM ID — TIMELINE_NEXT chain materialised in JSONB so the
//     search-side F12 GIN index (idx_memory_linked_ids) actually catches forward
//     hops without waiting on the post-insert structural-edge writer.
//   - Pass 2 marshals props with each row's forward neighbour wired in. Last
//     row in the batch has no neighbour → empty linked slice → property omitted.
func (h *Handler) buildFastBatch(
	ctx context.Context,
	pending []pendingFastMemory,
	vecs [][]float32,
	fac fastAddContext,
) ([]db.MemoryInsertNode, []addResponseItem, [][]float32, error) {
	survivors, wmEmbeddings := h.selectFastSurvivors(ctx, pending, vecs, fac)
	if len(survivors) == 0 {
		return nil, nil, nil, nil
	}

	allNodes := make([]db.MemoryInsertNode, 0, len(survivors)*2)
	items := make([]addResponseItem, 0, len(survivors))
	for i, s := range survivors {
		var nextLTMID string
		if i+1 < len(survivors) {
			nextLTMID = survivors[i+1].ltmID
		}
		nodes, item, err := h.buildFastNodes(s, fac, nextLTMID)
		if err != nil {
			return nil, nil, nil, err
		}
		allNodes = append(allNodes, nodes...)
		items = append(items, item)
	}
	return allNodes, items, wmEmbeddings, nil
}

// fastSurvivor carries everything buildFastNodes needs for a single survivor:
// the source memory, its embedding, the per-row info bag, and the pre-allocated
// WM/LTM UUIDs. Pre-allocating the UUIDs in pass 1 lets pass 2 wire each row's
// linked_memory_ids to the next survivor's LTM ID before the JSONB props are
// marshalled — the only way to get TIMELINE_NEXT into the GIN index without a
// second round-trip through Postgres.
type fastSurvivor struct {
	mem       extractedMemory
	embedding []float32
	info      map[string]any
	wmID      string
	ltmID     string
}

// selectFastSurvivors runs cosine-dedup over the pending batch and returns the
// rows that pass, with WM/LTM UUIDs pre-allocated for the chain step. The
// parallel wmEmbeddings slice is what writeWMCache + emitStructuralEdges
// expect downstream — kept as a separate return so the existing code paths
// don't have to grow new fields on the survivor struct.
func (h *Handler) selectFastSurvivors(
	ctx context.Context,
	pending []pendingFastMemory,
	vecs [][]float32,
	fac fastAddContext,
) ([]fastSurvivor, [][]float32) {
	survivors := make([]fastSurvivor, 0, len(pending))
	wmEmbeddings := make([][]float32, 0, len(pending))
	for j, p := range pending {
		embedding := vecs[j]
		if h.isDuplicate(ctx, embedding, fac.cubeID, fac.agentID, fac.sessionID) {
			continue
		}
		survivors = append(survivors, fastSurvivor{
			mem:       p.mem,
			embedding: embedding,
			info:      mergeInfo(fac.info, p.hash),
			wmID:      uuid.New().String(),
			ltmID:     uuid.New().String(),
		})
		wmEmbeddings = append(wmEmbeddings, embedding)
	}
	return survivors, wmEmbeddings
}

// buildFastNodes constructs the WM and LTM MemoryInsertNode pair for a memory.
//
// Three no-LLM fields lifted to top-level properties (since 2026-04-29) — only
// for per-message rows; window-mode rows preserve their pre-existing shape
// because the fields are not meaningfully derivable from a multi-message
// window (mixed speakers / multiple chat_times / LLM-grade reasoning needed
// for soft-graph links):
//   - attributed_to     ← mem.Sources[0]["role"] (search idx_memory_attributed_to)
//   - event_dates       ← regex YYYY-MM-DD over content + chat_time anchor (F7)
//   - linked_memory_ids ← [nextLTMID] when set, else nil (F12 multi-hop, forward
//     TIMELINE_NEXT chain pre-materialised in JSONB)
//
// Per-message rows also stamp kind=fast_msg (distinct from atomic_fact and
// the migration default paragraph_legacy) and lift per-msg metadata
// (chat_time/uuid/agent_id/role) into properties.info — both for filter
// pushdown parity with the raw path. Empty values omit the corresponding
// property so the GIN/partial indexes (migration 0022 / 0024) skip the row.
func (h *Handler) buildFastNodes(
	s fastSurvivor,
	fac fastAddContext,
	nextLTMID string,
) ([]db.MemoryInsertNode, addResponseItem, error) {
	embeddingStr := db.FormatVector(s.embedding)

	// Per-message gating: window-mode rows aggregate multiple speakers /
	// chat_times / messages into one extractedMemory. The no-LLM fields
	// don't have a single canonical value in that shape, so we skip them
	// to preserve the pre-2026-04-30 byte-for-byte property layout.
	perMsg := isPerMessageFastMemory(s.mem)
	var (
		attributedTo string
		eventDates   []string
		linked       []string
		kind         string
	)
	if perMsg {
		applyPerMessageFastInfo(s.info, s.mem)
		attributedTo = attributedToForFastMemory(s.mem)
		eventDates = extractEventDatesForFastMemory(s.mem)
		if nextLTMID != "" {
			linked = []string{nextLTMID}
		}
		kind = fastMsgKind
	}

	wmJSON, err := marshalProps(buildNodeProps(memoryNodeProps{
		ID: s.wmID, Memory: s.mem.Text, MemoryType: "WorkingMemory",
		UserName: fac.cubeID, UserID: fac.userID, AgentID: fac.agentID, SessionID: fac.sessionID,
		Mode: modeFast, Now: fac.now, CreatedAt: fac.now,
		Info: s.info, CustomTags: fac.customTags, Sources: s.mem.Sources, Background: "",
		Key:             fac.key,
		ObservationDate: fac.observationDate,
		ExtractionState: extractionStateExtracted,
		AttributedTo:    attributedTo,
		EventDates:      eventDates,
		LinkedMemoryIDs: linked,
		Kind:            kind,
	}))
	if err != nil {
		return nil, addResponseItem{}, fmt.Errorf("marshal wm properties: %w", err)
	}

	ltJSON, err := marshalProps(buildNodeProps(memoryNodeProps{
		ID: s.ltmID, Memory: s.mem.Text, MemoryType: s.mem.MemoryType,
		UserName: fac.cubeID, UserID: fac.userID, AgentID: fac.agentID, SessionID: fac.sessionID,
		Mode: modeFast, Now: fac.now, CreatedAt: fac.now,
		Info: s.info, CustomTags: fac.customTags, Sources: s.mem.Sources, Background: workingBinding(s.wmID),
		Key:             fac.key,
		ObservationDate: fac.observationDate,
		ExtractionState: extractionStateExtracted,
		AttributedTo:    attributedTo,
		EventDates:      eventDates,
		LinkedMemoryIDs: linked,
		Kind:            kind,
	}))
	if err != nil {
		return nil, addResponseItem{}, fmt.Errorf("marshal lt properties: %w", err)
	}

	nodes := []db.MemoryInsertNode{
		{ID: s.wmID, PropertiesJSON: wmJSON, EmbeddingVec: embeddingStr},
		{ID: s.ltmID, PropertiesJSON: ltJSON, EmbeddingVec: embeddingStr},
	}
	item := addResponseItem{Memory: s.mem.Text, MemoryID: s.ltmID, MemoryType: s.mem.MemoryType, CubeID: fac.cubeID}
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

// fastGranularityEnv selects the fast-mode extractor.
//   - "" / "per_message" / "msg" — one row per message (default since 2026-04-30).
//   - "window" — legacy sliding-window. Honors req.WindowChars override.
//
// Per-message default lifted recall on cat-1 simple-fact LoCoMo questions
// where window-averaged embeddings hid individual statements ("3 kids",
// "Oliver Luna Bailey"). The window option stays for A/B + workloads where
// turn-level context matters more than per-fact granularity (e.g. summaries
// of long meetings).
const fastGranularityEnv = "MEMDB_FAST_GRANULARITY"

func selectFastExtractor(req *fullAddRequest) []extractedMemory {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(fastGranularityEnv)))
	switch mode {
	case "window", "windowed", "sliding":
		return extractFastMemories(req.Messages, windowSizeFor(req))
	default:
		// Per-message is the default. Empty env or any unrecognised value
		// (including the explicit "per_message"/"msg") routes here.
		return extractFastMemoriesPerMessage(req.Messages)
	}
}
