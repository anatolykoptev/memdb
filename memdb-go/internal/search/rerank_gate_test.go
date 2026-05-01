package search

import (
	"context"
	"testing"
)

// TestRerankStrategy_EnvOverrides verifies that the four gate thresholds
// can be tuned at runtime via env. Set MEMDB_RERANK_TOP_COSINE_THRESHOLD
// to 0.5 — anything above 0.5 should now skip as high-confidence even if
// the default (0.85) would have let it pass.
func TestRerankStrategy_EnvOverrides(t *testing.T) {
	t.Setenv("MEMDB_RERANK_TOP_COSINE_THRESHOLD", "0.5")
	t.Setenv("MEMDB_RERANK_CLUSTERED_SPREAD", "0.20")
	t.Setenv("MEMDB_RERANK_WIDE_SPREAD", "0.50")
	t.Setenv("MEMDB_RERANK_TOPK_CAP", "12")

	// Top 0.70 > 0.50 (overridden) → high-confidence skip.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.60}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.55}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.50}},
	}
	d := rerankStrategy(context.Background(), items)
	if d.ShouldRerank || d.Reason != "high-confidence" {
		t.Fatalf("env override of TOP_COSINE_THRESHOLD ignored: got %+v", d)
	}

	// Top 0.40 (below override 0.50), spread 0.10 (below override clustered=0.20) → clustered, rerank-all.
	items2 := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.40}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.35}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.32}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.30}},
	}
	d2 := rerankStrategy(context.Background(), items2)
	if !d2.ShouldRerank || d2.Reason != "clustered" {
		t.Fatalf("env override of CLUSTERED_SPREAD ignored: got %+v", d2)
	}

	// Medium-spread → TopK cap = 12 (overridden).
	items3 := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.45}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.20}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.15}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.10}},
	}
	d3 := rerankStrategy(context.Background(), items3)
	if !d3.ShouldRerank || d3.Reason != "medium-spread" || d3.TopK != 12 {
		t.Fatalf("env override of TOPK_CAP ignored: got %+v", d3)
	}
}

// --- Backward-compatible shouldLLMRerank tests ---

func TestShouldLLMRerank_TooFewResults(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "memory": "x", "metadata": map[string]any{"relativity": 0.5}},
		{"id": "b", "memory": "y", "metadata": map[string]any{"relativity": 0.4}},
	}
	if shouldLLMRerank(context.Background(), items) {
		t.Error("expected false for 2 items (< 4)")
	}
}

func TestShouldLLMRerank_ThreeResults(t *testing.T) {
	items := make([]map[string]any, 3)
	for i := range items {
		items[i] = map[string]any{"id": "x", "metadata": map[string]any{"relativity": 0.5}}
	}
	if shouldLLMRerank(context.Background(), items) {
		t.Error("expected false for exactly 3 items")
	}
}

func TestShouldLLMRerank_HighTopCosine(t *testing.T) {
	// Top relativity 0.95 > defaultRerankTopCosineThreshold (0.85) → high-confidence skip.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.95}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.60}},
	}
	if shouldLLMRerank(context.Background(), items) {
		t.Errorf("expected false when top relativity > defaultRerankTopCosineThreshold (%.2f)", defaultRerankTopCosineThreshold)
	}
}

func TestShouldLLMRerank_ShouldRerank(t *testing.T) {
	// Top 0.84 (≤ 0.85 high-conf threshold), spread 0.14 → medium-spread (in [0.10, 0.30]) → rerank.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.84}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.79}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.74}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.70}},
	}
	if !shouldLLMRerank(context.Background(), items) {
		t.Error("expected true for medium spread results")
	}
}

func TestShouldLLMRerank_NoMetadata(t *testing.T) {
	items := []map[string]any{
		{"id": "a"},
		{"id": "b"},
		{"id": "c"},
		{"id": "d"},
	}
	if !shouldLLMRerank(context.Background(), items) {
		t.Error("expected true when metadata is missing (can't determine high confidence)")
	}
}

func TestShouldLLMRerank_EmptySlice(t *testing.T) {
	if shouldLLMRerank(context.Background(), nil) {
		t.Error("expected false for nil slice")
	}
	if shouldLLMRerank(context.Background(), []map[string]any{}) {
		t.Error("expected false for empty slice")
	}
}

func TestShouldLLMRerank_ExactlyAtThreshold(t *testing.T) {
	// Top exactly 0.85 should NOT trigger high-confidence skip (gate uses
	// strict > comparison). Spread 0.85-0.72 = 0.13 → medium-spread → rerank.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.76}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.72}},
	}
	if !shouldLLMRerank(context.Background(), items) {
		t.Errorf("expected true when relativity == defaultRerankTopCosineThreshold (%.2f) exactly (threshold is strict >)", defaultRerankTopCosineThreshold)
	}
}

func TestShouldLLMRerank_ExactlyFourItems(t *testing.T) {
	// Medium spread (0.80-0.65 = 0.15) → rerank.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.75}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.65}},
	}
	if !shouldLLMRerank(context.Background(), items) {
		t.Error("expected true for exactly 4 items with medium spread")
	}
}

func TestShouldLLMRerank_MetadataWithoutRelativity(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"memory_type": "LongTermMemory"}},
		{"id": "b", "metadata": map[string]any{}},
		{"id": "c", "metadata": map[string]any{}},
		{"id": "d", "metadata": map[string]any{}},
	}
	if !shouldLLMRerank(context.Background(), items) {
		t.Error("expected true when metadata exists but relativity is missing")
	}
}

// --- RerankDecision / rerankStrategy tests ---

func TestRerankStrategy_ClusteredScores(t *testing.T) {
	// Top below high-conf threshold (0.85) and spread < 0.10 → clustered, rerank all.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.82}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.81}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.79}},
	}
	d := rerankStrategy(context.Background(), items)
	if !d.ShouldRerank {
		t.Errorf("expected rerank for clustered scores (spread < %.2f)", defaultRerankClusteredSpread)
	}
	if d.Reason != "clustered" {
		t.Errorf("expected reason 'clustered', got %q", d.Reason)
	}
	if d.TopK != 0 {
		t.Errorf("expected TopK=0 for clustered (rerank all), got %d", d.TopK)
	}
}

func TestRerankStrategy_WideSpread(t *testing.T) {
	// Top below high-conf threshold (0.85), spread > 0.30 → wide-spread skip.
	// Use 0.85 top to dodge the high-confidence cut and still produce > 0.30 spread.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.65}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.55}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.45}},
	}
	d := rerankStrategy(context.Background(), items)
	if d.ShouldRerank {
		t.Errorf("expected skip for wide spread (> %.2f)", defaultRerankWideSpread)
	}
	if d.Reason != "wide-spread" {
		t.Errorf("expected reason 'wide-spread', got %q", d.Reason)
	}
}

func TestRerankStrategy_MediumSpread(t *testing.T) {
	// Top 0.85 (== high-conf threshold, so NOT skipped — strict >),
	// spread 0.15 (in [0.10, 0.30]) → rerank with TopK cap.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.78}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.73}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.70}},
	}
	d := rerankStrategy(context.Background(), items)
	if !d.ShouldRerank {
		t.Error("expected rerank for medium spread")
	}
	if d.TopK != defaultRerankTopKCap {
		t.Errorf("expected TopK=%d for medium spread, got %d", defaultRerankTopKCap, d.TopK)
	}
	if d.Reason != "medium-spread" {
		t.Errorf("expected reason 'medium-spread', got %q", d.Reason)
	}
}

func TestRerankStrategy_TooFewResults(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.70}},
	}
	d := rerankStrategy(context.Background(), items)
	if d.ShouldRerank {
		t.Error("expected skip for <4 results")
	}
	if d.Reason != "too-few" {
		t.Errorf("expected reason 'too-few', got %q", d.Reason)
	}
}

func TestRerankStrategy_HighTopCosine(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.95}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.60}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.50}},
	}
	d := rerankStrategy(context.Background(), items)
	if d.ShouldRerank {
		t.Error("expected skip for high top cosine")
	}
	if d.Reason != "high-confidence" {
		t.Errorf("expected reason 'high-confidence', got %q", d.Reason)
	}
}

func TestRerankStrategy_ExactlyAtSpreadBoundaries(t *testing.T) {
	// Spread ~0.12 (above clustered threshold 0.10, below wide 0.30) on a
	// top score below the high-conf threshold (0.85) → medium-spread.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.84}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.78}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.74}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.72}},
	}
	d := rerankStrategy(context.Background(), items)
	if !d.ShouldRerank {
		t.Error("spread 0.12 should be medium-spread (not clustered)")
	}
	if d.Reason != "medium-spread" {
		t.Errorf("expected 'medium-spread', got %q", d.Reason)
	}

	// Spread exactly defaultRerankWideSpread (0.30) → NOT > 0.30, so still medium.
	items2 := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.75}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.65}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.55}},
	}
	d2 := rerankStrategy(context.Background(), items2)
	if !d2.ShouldRerank {
		t.Errorf("spread exactly %.2f should be medium-spread (gate is strict >)", defaultRerankWideSpread)
	}
	if d2.Reason != "medium-spread" {
		t.Errorf("expected 'medium-spread', got %q", d2.Reason)
	}
}

func TestRerankStrategy_NoMetadata_Clustered(t *testing.T) {
	// All relativity=0 → spread=0 → clustered.
	items := []map[string]any{
		{"id": "a"},
		{"id": "b"},
		{"id": "c"},
		{"id": "d"},
	}
	d := rerankStrategy(context.Background(), items)
	if !d.ShouldRerank {
		t.Error("expected rerank for missing metadata (clustered)")
	}
	if d.Reason != "clustered" {
		t.Errorf("expected 'clustered', got %q", d.Reason)
	}
}
