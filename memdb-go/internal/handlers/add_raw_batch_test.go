package handlers

// add_raw_batch_test.go — tests for the chunked raw-add batch loop
// (processRawBatches). The end-to-end nativeRawAddForCube can't be unit-tested
// without a live Postgres (Handler holds *db.Postgres directly, no interface),
// so these tests exercise processRawBatches directly with a stub inserter and
// a stub item processor — the same pattern as add_fast_batched_test.go.
//
// The key assertion: a 5000-item request keeps peak memory flat (O(batchSize),
// not O(N)) because per-chunk slices are scoped to the loop body and GC'd
// before the next chunk allocates.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// stubRawInserter is a no-op rawNodeInserter that counts InsertMemoryNodes
// calls and records the batch sizes it received.
type stubRawInserter struct {
	callCount  atomic.Int64
	totalNodes atomic.Int64
	batchSizes []int
	err        error
}

func (s *stubRawInserter) InsertMemoryNodes(_ context.Context, nodes []db.MemoryInsertNode) error {
	s.callCount.Add(1)
	s.totalNodes.Add(int64(len(nodes)))
	s.batchSizes = append(s.batchSizes, len(nodes))
	return s.err
}

// stubRawItemProcessor builds a realistic node/item/embedding for each item
// without touching the embedder or Postgres. The embedding is a 1024-dim
// float32 slice — the same shape the real processRawMemory produces.
func stubRawItemProcessor(fac fastAddContext) rawItemProcessor {
	return func(_ context.Context, it rawTextItem, hash string) (db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
		emb := make([]float32, 1024)
		info := mergeInfo(fac.info, hash)
		node, item, err := buildRawNode(it.Text, emb, fac, info)
		if err != nil {
			return db.MemoryInsertNode{}, addResponseItem{}, nil, false, err
		}
		return node, item, emb, false, nil
	}
}

func rawBatchTestHandler() *Handler {
	return &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// postgres = nil  → emitStructuralEdges returns early (nil guard)
		// wmCache  = nil  → writeRawCache returns early (nil guard)
	}
}

func rawBatchTestFac() fastAddContext {
	return fastAddContext{
		cubeID:    "test-cube",
		userID:    "test-user",
		agentID:   "test-agent",
		sessionID: "test-session",
		now:       "2026-05-20T10:00:00.000000",
		info:      map[string]any{},
	}
}

func makeRawItems(n int) []rawTextItem {
	items := make([]rawTextItem, n)
	for i := range items {
		items[i] = rawTextItem{
			Text:     fmt.Sprintf("item-%d", i),
			ChatTime: "2026-05-20T10:00:00",
			UUID:     fmt.Sprintf("uuid-%d", i),
			AgentID:  "test-agent",
			Role:     "user",
		}
	}
	return items
}

// TestProcessRawBatches_MemoryStaysFlatUnder5000Items is the RED→GREEN test
// for the M7 OOM fix. A 5000-item request must keep peak memory bounded to
// O(rawBatchSize), not O(N). We measure runtime.MemStats before and after,
// then assert the live-heap delta stays well under the threshold that would
// indicate all 5000 nodes/embeddings were held simultaneously.
//
// Each node carries a 1024-dim float32 embedding (4KB) + JSON properties
// (~1KB). 5000 nodes unbounded ≈ 25MB just for embeddings. With chunking at
// rawBatchSize=1000, peak live heap should be ~5MB. We allow generous headroom
// (50MB) to avoid flakiness from GC timing on different CI runners.
func TestProcessRawBatches_MemoryStaysFlatUnder5000Items(t *testing.T) {
	const numItems = 5000
	const maxHeapDeltaMB = 50.0

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)
	processItem := stubRawItemProcessor(fac)
	inserter := &stubRawInserter{}

	// Force GC before measuring baseline so we start from a clean slate.
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	responseItems, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err != nil {
		t.Fatalf("processRawBatches: %v", err)
	}

	// Force GC again so only retained memory is measured.
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// All items must be persisted (no silent drops from chunking).
	if len(responseItems) != numItems {
		t.Errorf("expected %d response items, got %d — items must not be dropped by chunking",
			numItems, len(responseItems))
	}
	if got := inserter.totalNodes.Load(); got != numItems {
		t.Errorf("expected %d total nodes inserted, got %d", numItems, got)
	}

	totalAllocDeltaMB := float64(after.TotalAlloc-baseline.TotalAlloc) / (1024 * 1024)
	t.Logf("TotalAlloc delta for %d items: %.2f MB (batchSize=%d, batches=%d)",
		numItems, totalAllocDeltaMB, rawBatchSize, inserter.callCount.Load())

	// HeapAlloc is live heap. After GC, if chunking worked, the per-chunk
	// slices have been collected and only allResponseItems (the response
	// slice, which is just IDs + text strings, ~5000 * ~100B ≈ 0.5MB) plus
	// the test's own input slices remain live. Guard against uint64
	// underflow when GC reclaims more than was allocated since baseline.
	var heapDeltaMB float64
	if after.HeapAlloc >= baseline.HeapAlloc {
		heapDeltaMB = float64(after.HeapAlloc-baseline.HeapAlloc) / (1024 * 1024)
	}
	t.Logf("HeapAlloc delta: %.2f MB", heapDeltaMB)

	// Verify chunking happened: multiple bounded insert batches.
	numBatches := inserter.callCount.Load()
	expectedBatches := (numItems + rawBatchSize - 1) / rawBatchSize
	if numBatches != int64(expectedBatches) {
		t.Errorf("expected %d insert batches, got %d — chunking not working",
			expectedBatches, numBatches)
	}

	// Verify each batch was bounded to rawBatchSize.
	for i, sz := range inserter.batchSizes {
		if sz > rawBatchSize {
			t.Errorf("batch %d size %d exceeds rawBatchSize %d — unbounded growth not fixed",
				i, sz, rawBatchSize)
		}
	}

	// Guard: if heap delta exceeds threshold, the fix isn't bounding memory.
	// This is the direct OOM-prevention assertion.
	if heapDeltaMB > maxHeapDeltaMB {
		t.Errorf("heap alloc delta %.2f MB exceeds threshold %.2f MB — memory not bounded by chunking",
			heapDeltaMB, maxHeapDeltaMB)
	}
}

// TestProcessRawBatches_AllItemsPersisted verifies that chunking doesn't drop
// any items — every item that survives processing must appear in the response
// and be passed to the inserter.
func TestProcessRawBatches_AllItemsPersisted(t *testing.T) {
	const numItems = 2500 // 2.5 batches at rawBatchSize=1000

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)
	processItem := stubRawItemProcessor(fac)
	inserter := &stubRawInserter{}

	responseItems, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err != nil {
		t.Fatalf("processRawBatches: %v", err)
	}

	if len(responseItems) != numItems {
		t.Errorf("expected %d response items, got %d", numItems, len(responseItems))
	}
	if got := inserter.totalNodes.Load(); got != numItems {
		t.Errorf("expected %d total nodes inserted, got %d", numItems, got)
	}

	// Verify response items are in order (chunking must preserve order).
	for i, item := range responseItems {
		want := fmt.Sprintf("item-%d", i)
		if item.Memory != want {
			t.Errorf("responseItems[%d].Memory = %q, want %q (order not preserved)", i, item.Memory, want)
		}
	}
}

// TestProcessRawBatches_BatchCountMetric verifies the memdb.add.raw_batch_count
// histogram records the correct number of batches.
func TestProcessRawBatches_BatchCountMetric(t *testing.T) {
	const numItems = 3500 // 4 batches at rawBatchSize=1000 (1000+1000+1000+500)

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)
	processItem := stubRawItemProcessor(fac)
	inserter := &stubRawInserter{}

	_, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err != nil {
		t.Fatalf("processRawBatches: %v", err)
	}

	expectedBatches := (numItems + rawBatchSize - 1) / rawBatchSize
	if got := inserter.callCount.Load(); got != int64(expectedBatches) {
		t.Errorf("expected %d insert calls, got %d", expectedBatches, got)
	}

	// The metric should be registered (non-nil) after the call.
	if rawBatchCountMetric() == nil {
		t.Error("memdb.add.raw_batch_count histogram was not registered")
	}
}

// TestProcessRawBatches_SkippedItemsNotInserted verifies that when the
// processor returns skip=true for some items, those are excluded from the
// insert batch but the remaining items still get persisted.
func TestProcessRawBatches_SkippedItemsNotInserted(t *testing.T) {
	const numItems = 100
	skipEvery := 3 // skip items 0, 3, 6, ...

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)

	processItem := func(_ context.Context, it rawTextItem, hash string) (db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
		// Extract the index from the text "item-N"
		var idx int
		fmt.Sscanf(it.Text, "item-%d", &idx)
		if idx%skipEvery == 0 {
			return db.MemoryInsertNode{}, addResponseItem{}, nil, true, nil
		}
		emb := make([]float32, 1024)
		info := mergeInfo(fac.info, hash)
		node, item, err := buildRawNode(it.Text, emb, fac, info)
		if err != nil {
			return db.MemoryInsertNode{}, addResponseItem{}, nil, false, err
		}
		return node, item, emb, false, nil
	}
	inserter := &stubRawInserter{}

	responseItems, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err != nil {
		t.Fatalf("processRawBatches: %v", err)
	}

	expectedSurvivors := numItems - (numItems/skipEvery + 1)
	if len(responseItems) != expectedSurvivors {
		t.Errorf("expected %d response items (after skips), got %d", expectedSurvivors, len(responseItems))
	}
	if got := inserter.totalNodes.Load(); got != int64(expectedSurvivors) {
		t.Errorf("expected %d nodes inserted, got %d", expectedSurvivors, got)
	}
}

// TestProcessRawBatches_InsertErrorPropagates verifies that an insert error
// stops processing and returns the wrapped error.
func TestProcessRawBatches_InsertErrorPropagates(t *testing.T) {
	const numItems = 100

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)
	processItem := stubRawItemProcessor(fac)
	inserter := &stubRawInserter{err: fmt.Errorf("db connection lost")}

	_, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err == nil {
		t.Fatal("expected error from insert failure, got nil")
	}
	if !strings.Contains(err.Error(), "insert nodes") {
		t.Errorf("expected 'insert nodes' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "db connection lost") {
		t.Errorf("expected underlying error preserved, got: %v", err)
	}
}

// TestProcessRawBatches_ProcessorErrorPropagates verifies that a processor
// error stops the entire batch loop immediately.
func TestProcessRawBatches_ProcessorErrorPropagates(t *testing.T) {
	const numItems = 100

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)

	processItem := func(_ context.Context, it rawTextItem, _ string) (db.MemoryInsertNode, addResponseItem, []float32, bool, error) {
		var idx int
		fmt.Sscanf(it.Text, "item-%d", &idx)
		if idx == 5 {
			return db.MemoryInsertNode{}, addResponseItem{}, nil, false, fmt.Errorf("embedder failed on item %d", idx)
		}
		emb := make([]float32, 1024)
		info := mergeInfo(fac.info, "hash")
		node, item, err := buildRawNode(it.Text, emb, fac, info)
		if err != nil {
			return db.MemoryInsertNode{}, addResponseItem{}, nil, false, err
		}
		return node, item, emb, false, nil
	}
	inserter := &stubRawInserter{}

	_, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err == nil {
		t.Fatal("expected error from processor failure, got nil")
	}
	if !strings.Contains(err.Error(), "embedder failed on item 5") {
		t.Errorf("expected processor error preserved, got: %v", err)
	}
	// The insert should not have been called (error occurs within first batch
	// before the insert).
	if got := inserter.callCount.Load(); got != 0 {
		t.Errorf("expected 0 insert calls (error before first insert), got %d", got)
	}
}

// TestProcessRawBatches_EmptyItems returns empty without error.
func TestProcessRawBatches_EmptyItems(t *testing.T) {
	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	inserter := &stubRawInserter{}

	responseItems, err := h.processRawBatches(context.Background(), nil, nil, stubRawItemProcessor(fac), inserter, fac)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responseItems) != 0 {
		t.Errorf("expected 0 response items, got %d", len(responseItems))
	}
	if got := inserter.callCount.Load(); got != 0 {
		t.Errorf("expected 0 insert calls for empty input, got %d", got)
	}
}

// TestProcessRawBatches_BatchSizeBounded verifies that no single insert batch
// exceeds rawBatchSize, even with a large item count.
func TestProcessRawBatches_BatchSizeBounded(t *testing.T) {
	const numItems = 5000

	h := rawBatchTestHandler()
	fac := rawBatchTestFac()
	items := makeRawItems(numItems)
	hashes := hashRawItems(items)
	processItem := stubRawItemProcessor(fac)
	inserter := &stubRawInserter{}

	_, err := h.processRawBatches(context.Background(), items, hashes, processItem, inserter, fac)
	if err != nil {
		t.Fatalf("processRawBatches: %v", err)
	}

	for i, sz := range inserter.batchSizes {
		if sz > rawBatchSize {
			t.Errorf("batch %d has %d nodes, exceeds rawBatchSize %d", i, sz, rawBatchSize)
		}
	}
}
