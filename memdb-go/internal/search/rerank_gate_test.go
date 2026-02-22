package search

import "testing"

// --- Backward-compatible shouldLLMRerank tests ---

func TestShouldLLMRerank_TooFewResults(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "memory": "x", "metadata": map[string]any{"relativity": 0.5}},
		{"id": "b", "memory": "y", "metadata": map[string]any{"relativity": 0.4}},
	}
	if shouldLLMRerank(items) {
		t.Error("expected false for 2 items (< 4)")
	}
}

func TestShouldLLMRerank_ThreeResults(t *testing.T) {
	items := make([]map[string]any, 3)
	for i := range items {
		items[i] = map[string]any{"id": "x", "metadata": map[string]any{"relativity": 0.5}}
	}
	if shouldLLMRerank(items) {
		t.Error("expected false for exactly 3 items")
	}
}

func TestShouldLLMRerank_HighTopCosine(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.95}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.60}},
	}
	if shouldLLMRerank(items) {
		t.Error("expected false when top result relativity > 0.93")
	}
}

func TestShouldLLMRerank_ShouldRerank(t *testing.T) {
	// Medium spread (0.85-0.70 = 0.15) → should rerank.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.75}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.70}},
	}
	if !shouldLLMRerank(items) {
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
	if !shouldLLMRerank(items) {
		t.Error("expected true when metadata is missing (can't determine high confidence)")
	}
}

func TestShouldLLMRerank_EmptySlice(t *testing.T) {
	if shouldLLMRerank(nil) {
		t.Error("expected false for nil slice")
	}
	if shouldLLMRerank([]map[string]any{}) {
		t.Error("expected false for empty slice")
	}
}

func TestShouldLLMRerank_ExactlyAtThreshold(t *testing.T) {
	// Exactly 0.93 should NOT trigger high-confidence skip (> 0.93, not >=).
	// Spread 0.93-0.80 = 0.13 → medium-spread → rerank.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.93}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.88}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.84}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.80}},
	}
	if !shouldLLMRerank(items) {
		t.Error("expected true when relativity == 0.93 exactly (threshold is strict >)")
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
	if !shouldLLMRerank(items) {
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
	if !shouldLLMRerank(items) {
		t.Error("expected true when metadata exists but relativity is missing")
	}
}

// --- RerankDecision / rerankStrategy tests ---

func TestRerankStrategy_ClusteredScores(t *testing.T) {
	// Spread < 0.05 → clustered, rerank all.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.82}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.81}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.79}},
	}
	d := rerankStrategy(items)
	if !d.ShouldRerank {
		t.Error("expected rerank for clustered scores (spread < 0.05)")
	}
	if d.Reason != "clustered" {
		t.Errorf("expected reason 'clustered', got %q", d.Reason)
	}
	if d.TopK != 0 {
		t.Errorf("expected TopK=0 for clustered (rerank all), got %d", d.TopK)
	}
}

func TestRerankStrategy_WideSpread(t *testing.T) {
	// Spread > 0.25 → skip (clear separation).
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.90}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.60}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.50}},
	}
	d := rerankStrategy(items)
	if d.ShouldRerank {
		t.Error("expected skip for wide spread (> 0.25)")
	}
	if d.Reason != "wide-spread" {
		t.Errorf("expected reason 'wide-spread', got %q", d.Reason)
	}
}

func TestRerankStrategy_MediumSpread(t *testing.T) {
	// Spread 0.05–0.25 → rerank with TopK cap.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.78}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.73}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.70}},
	}
	d := rerankStrategy(items)
	if !d.ShouldRerank {
		t.Error("expected rerank for medium spread")
	}
	if d.TopK != rerankTopKCap {
		t.Errorf("expected TopK=%d for medium spread, got %d", rerankTopKCap, d.TopK)
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
	d := rerankStrategy(items)
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
	d := rerankStrategy(items)
	if d.ShouldRerank {
		t.Error("expected skip for high top cosine")
	}
	if d.Reason != "high-confidence" {
		t.Errorf("expected reason 'high-confidence', got %q", d.Reason)
	}
}

func TestRerankStrategy_ExactlyAtSpreadBoundaries(t *testing.T) {
	// Spread ~0.06 → above clustered threshold (0.05), below wide (0.25) → medium-spread.
	// Note: using 0.06 gap instead of 0.05 to avoid float64 rounding at boundary.
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.86}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.84}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.82}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.80}},
	}
	d := rerankStrategy(items)
	if !d.ShouldRerank {
		t.Error("spread ~0.06 should be medium-spread (not clustered)")
	}
	if d.Reason != "medium-spread" {
		t.Errorf("expected 'medium-spread', got %q", d.Reason)
	}

	// Spread exactly 0.25 → NOT > 0.25, so not wide-spread. → medium-spread.
	items2 := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.75}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.65}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.60}},
	}
	d2 := rerankStrategy(items2)
	if !d2.ShouldRerank {
		t.Error("spread exactly 0.25 should be medium-spread (not wide-spread)")
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
	d := rerankStrategy(items)
	if !d.ShouldRerank {
		t.Error("expected rerank for missing metadata (clustered)")
	}
	if d.Reason != "clustered" {
		t.Errorf("expected 'clustered', got %q", d.Reason)
	}
}
