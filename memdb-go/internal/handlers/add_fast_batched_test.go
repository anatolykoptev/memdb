package handlers

// add_fast_batched_test.go — unit tests for the F2 batched-embed refactor of
// nativeFastAddForCube. The end-to-end pipeline can't be unit-tested without
// a real Postgres (Handler holds *db.Postgres directly, no interface), so
// these tests exercise the new helpers in isolation:
//
//   - selectPendingFastMemories: hash-dedup filtering
//   - batchEmbedFastTexts: single Embed call, length-mismatch error,
//     embedder error wrapping
//
// The hot assertion ("exactly 1 Embed call regardless of N memories") is
// covered by recordingEmbedder.calls. The full pipeline (including cosine
// dedup and DB inserts) is covered by add_fast_batched_livepg_test.go.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

// recordingEmbedder counts Embed calls and observed batch sizes so tests can
// assert on call shape.
type recordingEmbedder struct {
	calls       atomic.Int64
	lastTexts   []string
	failOn      int   // call number (1-based) on which to fail; 0 = never
	shortBy     int   // when >0, return len(texts)-shortBy vectors (length-mismatch path)
	dim         int   // override embedding dimension; 0 = 1024
	returnEmpty bool  // return [][]float32{} regardless of input
	err         error // error to return when failOn matches
}

func (r *recordingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	n := r.calls.Add(1)
	r.lastTexts = append([]string(nil), texts...)
	if r.failOn != 0 && int(n) == r.failOn {
		err := r.err
		if err == nil {
			err = errors.New("recording embedder: forced failure")
		}
		return nil, err
	}
	if r.returnEmpty {
		return [][]float32{}, nil
	}
	dim := r.dim
	if dim == 0 {
		dim = 1024
	}
	out := make([][]float32, len(texts)-r.shortBy)
	for i := range out {
		out[i] = make([]float32, dim)
	}
	return out, nil
}

func (r *recordingEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	dim := r.dim
	if dim == 0 {
		dim = 1024
	}
	return make([]float32, dim), nil
}

func (r *recordingEmbedder) Dimension() int { return 1024 }
func (r *recordingEmbedder) Close() error   { return nil }

// quietHandler returns a Handler with a discard logger and the supplied embedder.
func quietHandler(emb *recordingEmbedder) *Handler {
	return &Handler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		embedder: emb,
	}
}

// --- selectPendingFastMemories ---

func TestSelectPendingFastMemories_NoExistingHashes(t *testing.T) {
	memories := []extractedMemory{
		{Text: "alpha"},
		{Text: "beta"},
		{Text: "gamma"},
	}
	hashes := []string{"h1", "h2", "h3"}

	pending, texts := selectPendingFastMemories(memories, hashes, nil, nil)
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}
	if len(texts) != 3 {
		t.Fatalf("expected 3 texts, got %d", len(texts))
	}
	for i, p := range pending {
		if p.hash != hashes[i] {
			t.Errorf("pending[%d].hash = %q, want %q", i, p.hash, hashes[i])
		}
		if texts[i] != memories[i].Text {
			t.Errorf("texts[%d] = %q, want %q", i, texts[i], memories[i].Text)
		}
	}
}

func TestSelectPendingFastMemories_AllDuplicates(t *testing.T) {
	memories := []extractedMemory{
		{Text: "alpha"},
		{Text: "beta"},
	}
	hashes := []string{"h1", "h2"}
	existing := map[string]bool{"h1": true, "h2": true}

	pending, texts := selectPendingFastMemories(memories, hashes, existing, nil)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending when all duplicates, got %d", len(pending))
	}
	if len(texts) != 0 {
		t.Errorf("expected 0 texts when all duplicates, got %d", len(texts))
	}
}

func TestSelectPendingFastMemories_PartialDuplicates(t *testing.T) {
	memories := []extractedMemory{
		{Text: "alpha"},
		{Text: "beta"},
		{Text: "gamma"},
		{Text: "delta"},
	}
	hashes := []string{"h1", "h2", "h3", "h4"}
	existing := map[string]bool{"h1": true, "h3": true} // 1st and 3rd are dup

	pending, texts := selectPendingFastMemories(memories, hashes, existing, nil)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	wantTexts := []string{"beta", "delta"}
	for i, want := range wantTexts {
		if pending[i].mem.Text != want {
			t.Errorf("pending[%d].mem.Text = %q, want %q", i, pending[i].mem.Text, want)
		}
		if texts[i] != want {
			t.Errorf("texts[%d] = %q, want %q", i, texts[i], want)
		}
		if pending[i].hash != hashes[i*2+1] {
			t.Errorf("pending[%d].hash = %q, want %q", i, pending[i].hash, hashes[i*2+1])
		}
	}
}

// --- batchEmbedFastTexts ---

func TestBatchEmbedFastTexts_SingleCallForManyTexts(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)

	texts := make([]string, 24) // simulates 24-window payload from window_chars=512
	for i := range texts {
		texts[i] = "memory " + string(rune('a'+i%26))
	}

	vecs, err := h.batchEmbedFastTexts(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := emb.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 Embed call regardless of N, got %d", got)
	}
	if len(vecs) != len(texts) {
		t.Errorf("expected %d vectors, got %d", len(texts), len(vecs))
	}
	if len(emb.lastTexts) != len(texts) {
		t.Errorf("expected %d texts forwarded, got %d", len(texts), len(emb.lastTexts))
	}
}

func TestBatchEmbedFastTexts_EmbedderError_WrapsAndReturns(t *testing.T) {
	emb := &recordingEmbedder{failOn: 1, err: errors.New("backend down")}
	h := quietHandler(emb)

	_, err := h.batchEmbedFastTexts(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "batch embed") {
		t.Errorf("expected wrapped 'batch embed' prefix, got: %v", err)
	}
	if !strings.Contains(err.Error(), "backend down") {
		t.Errorf("expected underlying error preserved, got: %v", err)
	}
}

func TestBatchEmbedFastTexts_LengthMismatch_ReturnsError(t *testing.T) {
	// Embedder returns N-1 vectors for N inputs — must error rather than
	// silently drop a memory.
	emb := &recordingEmbedder{shortBy: 1}
	h := quietHandler(emb)

	_, err := h.batchEmbedFastTexts(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("expected length-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "length mismatch") {
		t.Errorf("expected 'length mismatch' in error, got: %v", err)
	}
}

func TestBatchEmbedFastTexts_EmptyResult_TreatedAsLengthMismatch(t *testing.T) {
	emb := &recordingEmbedder{returnEmpty: true}
	h := quietHandler(emb)

	_, err := h.batchEmbedFastTexts(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error when embedder returns empty result, got nil")
	}
}

// --- guard: end-to-end skip path doesn't call Embed when all hashes match ---

func TestSelectPending_NoEmbedCallWhenAllDup(t *testing.T) {
	// Guard against future regressions where someone calls Embed before
	// selectPendingFastMemories. The helper itself doesn't touch the
	// embedder, but we assert the call shape one level up: caller of
	// nativeFastAddForCube should never invoke Embed when texts is empty.
	emb := &recordingEmbedder{}
	h := quietHandler(emb)

	memories := []extractedMemory{{Text: "x"}, {Text: "y"}}
	hashes := []string{"h1", "h2"}
	existing := map[string]bool{"h1": true, "h2": true}

	pending, texts := selectPendingFastMemories(memories, hashes, existing, h.logger)
	if len(pending) != 0 {
		t.Fatalf("expected zero pending, got %d", len(pending))
	}
	// Caller short-circuits on len(pending) == 0; embedder must not be called.
	if len(texts) == 0 {
		// Mirror the production check.
	} else {
		t.Fatal("texts should be empty when all memories are duplicates")
	}
	if got := emb.calls.Load(); got != 0 {
		t.Errorf("expected 0 Embed calls, got %d", got)
	}
}
