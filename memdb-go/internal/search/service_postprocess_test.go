package search

import (
	"context"
	"testing"

	rerankpkg "github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
)

// TestRerankGate_PostCosineOrdering pins the C2 contract: the rerank
// gate (rerankStrategy) is evaluated on POST-cosine scores, not on the
// raw PolarDB-compressed input band.
//
// Construction: input items carry pre-cosine relativities tightly
// clustered in [0.85, 0.92] (PolarDB band) — rerankStrategy on this
// raw input would label them "clustered" (spread < 0.05) and trigger
// LLM rerank. After cosine rescores against orthogonal embeddings, the
// distribution widens to ~[0, 1] — the SAME gate then sees clearly
// separated items and chooses "wide-spread" → skip LLM. This is the
// pre-R3 behaviour we must preserve.
func TestRerankGate_PostCosineOrdering(t *testing.T) {
	// 4 items in a tight PolarDB band — would be "clustered" pre-cosine
	// (spread < 0.05 → rerankClusteredSpread).
	items := []map[string]any{
		{"id": "a", "memory": "alpha", "metadata": map[string]any{"relativity": 0.890}},
		{"id": "b", "memory": "beta", "metadata": map[string]any{"relativity": 0.880}},
		{"id": "c", "memory": "gamma", "metadata": map[string]any{"relativity": 0.870}},
		{"id": "d", "memory": "delta", "metadata": map[string]any{"relativity": 0.860}},
	}

	// Sanity: gate on the raw PolarDB band labels this clustered.
	rawDecision := rerankStrategy(items)
	if rawDecision.Reason != "clustered" {
		t.Fatalf("setup: expected 'clustered' on raw input, got %q (test no longer exercises C2)", rawDecision.Reason)
	}
	if !rawDecision.ShouldRerank {
		t.Fatalf("setup: expected raw input to trigger rerank")
	}

	// Embeddings: each item is aligned with one orthogonal axis. Query
	// is purely on axis 0 → item "a" gets cos=1, others cos=0.
	queryVec := []float32{1, 0, 0, 0}
	embByID := map[string][]float32{
		"a": {1, 0, 0, 0},
		"b": {0, 1, 0, 0},
		"c": {0, 0, 1, 0},
		"d": {0, 0, 0, 1},
	}

	// Run the prefix chain (cosine only — CE absent without RerankClient).
	prefix := rerankpkg.Chain{rerankpkg.Cosine{QueryVec: queryVec, EmbeddingsByID: embByID}}
	adapted := adaptItems(items)
	res, err := prefix.RerankWithTimings(context.Background(), "q", adapted)
	if err != nil {
		t.Fatalf("prefix chain err: %v", err)
	}
	if _, ok := res.Durations["cosine"]; !ok {
		t.Errorf("expected cosine duration entry, got %v", res.Durations)
	}
	rescored := unadaptItems(res.Items)

	// Post-cosine: top score ≈ 1.0, bottom ≈ 0.5 (after (cos+1)/2 normalisation).
	// Spread ≈ 0.5 → wide-spread → gate should skip LLM.
	postDecision := rerankStrategy(rescored)
	if postDecision.ShouldRerank {
		t.Errorf("expected gate to SKIP rerank on post-cosine scores (wide spread), got reason=%q", postDecision.Reason)
	}
	if postDecision.Reason != "wide-spread" && postDecision.Reason != "high-confidence" {
		t.Errorf("expected reason 'wide-spread' or 'high-confidence' post-cosine, got %q", postDecision.Reason)
	}

	// And the gate must have changed its mind — that's the C2 invariant.
	if rawDecision.Reason == postDecision.Reason {
		t.Errorf("C2 regression: gate produced same decision %q on raw and post-cosine input — gate is being called on raw scores, not post-cosine", rawDecision.Reason)
	}
}

// TestRerankPhases_PerStrategyDurations verifies C1 at the
// service_postprocess level: the prefix + suffix split exposes
// independent timings the slog pipeline line can report without
// double-counting.
func TestRerankPhases_PerStrategyDurations(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "memory": "x", "metadata": map[string]any{"relativity": 0.8}},
		{"id": "b", "memory": "y", "metadata": map[string]any{"relativity": 0.7}},
	}
	embByID := map[string][]float32{
		"a": {1, 0},
		"b": {0, 1},
	}
	queryVec := []float32{1, 0}

	prefix := rerankpkg.Chain{rerankpkg.Cosine{QueryVec: queryVec, EmbeddingsByID: embByID}}
	res := runRerankChainWithTimings(context.Background(), prefix, "q", items)
	if _, ok := res.durations["cosine"]; !ok {
		t.Errorf("expected cosine timing entry, got durations=%v", res.durations)
	}
	if len(res.items) != 2 {
		t.Errorf("expected 2 items back, got %d", len(res.items))
	}

	// Empty input short-circuits with an empty duration map — no chain call.
	empty := runRerankChainWithTimings(context.Background(), prefix, "q", nil)
	if len(empty.durations) != 0 {
		t.Errorf("expected empty durations on empty input, got %v", empty.durations)
	}
}
