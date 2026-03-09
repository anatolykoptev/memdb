package search

import (
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestParseProperties_Valid(t *testing.T) {
	props := ParseProperties(`{"memory":"hello","id":"123"}`)
	if props == nil {
		t.Fatal("expected non-nil props")
	}
	if props["memory"] != "hello" {
		t.Errorf("memory = %v, want hello", props["memory"])
	}
}

func TestParseProperties_Empty(t *testing.T) {
	if ParseProperties("") != nil {
		t.Error("expected nil for empty string")
	}
}

func TestParseProperties_Invalid(t *testing.T) {
	if ParseProperties("not json") != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestFilterByRelativity(t *testing.T) {
	items := []map[string]any{
		{"memory": "high", "metadata": map[string]any{"relativity": 0.9}},
		{"memory": "low", "metadata": map[string]any{"relativity": 0.5}},
		{"memory": "mid", "metadata": map[string]any{"relativity": 0.8}},
	}
	filtered := FilterByRelativity(items, 0.85)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 item, got %d", len(filtered))
	}
	if filtered[0]["memory"] != "high" {
		t.Errorf("expected 'high', got %v", filtered[0]["memory"])
	}
}

func TestFilterByRelativity_NoMetadata(t *testing.T) {
	items := []map[string]any{
		{"memory": "no meta"},
	}
	filtered := FilterByRelativity(items, 0.5)
	if len(filtered) != 0 {
		t.Errorf("expected 0 items, got %d", len(filtered))
	}
}

func TestFilterPrefByQuality_Short(t *testing.T) {
	items := []map[string]any{
		{"memory": "short"},
		{"memory": "This is a long enough preference entry that passes the quality filter."},
	}
	filtered := FilterPrefByQuality(items)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 item, got %d", len(filtered))
	}
}

func TestFilterPrefByQuality_MessageLeak(t *testing.T) {
	items := []map[string]any{
		{"memory": "user: this looks like a conversation message leak and should be filtered out"},
		{"memory": "assistant: another message leak that should be filtered out of results"},
		{"memory": "This is a legitimate preference about something the user likes a lot."},
	}
	filtered := FilterPrefByQuality(items)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 item, got %d", len(filtered))
	}
}

func TestDedupByText_Duplicates(t *testing.T) {
	items := []map[string]any{
		{"memory": "Hello World"},
		{"memory": "hello world"}, // case-insensitive duplicate
		{"memory": "Different"},
	}
	result := DedupByText(items)
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
}

func TestDedupByText_SingleItem(t *testing.T) {
	items := []map[string]any{{"memory": "only"}}
	result := DedupByText(items)
	if len(result) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result))
	}
}

func TestMergeVectorAndFulltext_VectorOnly(t *testing.T) {
	vec := []db.VectorSearchResult{
		{ID: "1", Properties: `{"memory":"a"}`, Score: 0.8},
		{ID: "2", Properties: `{"memory":"b"}`, Score: 0.6},
	}
	merged := MergeVectorAndFulltext(vec, nil)
	if len(merged) != 2 {
		t.Fatalf("expected 2, got %d", len(merged))
	}
	// RRF: rank-1 item scores higher than rank-2 item, both in (0, 1]
	if merged[0].Score <= 0 || merged[1].Score <= 0 {
		t.Errorf("expected positive RRF scores, got %f and %f", merged[0].Score, merged[1].Score)
	}
	if merged[0].Score <= merged[1].Score {
		t.Errorf("expected rank-1 score > rank-2 score, got %f <= %f", merged[0].Score, merged[1].Score)
	}
	// Highest ID should be "1" (rank 1 in vector list)
	if merged[0].ID != "1" {
		t.Errorf("expected ID '1' at rank 0, got %s", merged[0].ID)
	}
}

func TestMergeVectorAndFulltext_Boost(t *testing.T) {
	// Item "1" appears in both lists; item "2" only in vector.
	// RRF: item in both lists should score higher than item in one list.
	vec := []db.VectorSearchResult{
		{ID: "1", Properties: `{"memory":"a"}`, Score: 0.8},
		{ID: "2", Properties: `{"memory":"b"}`, Score: 0.7},
	}
	ft := []db.VectorSearchResult{
		{ID: "1", Properties: `{"memory":"a"}`, Score: 2.0},
	}
	merged := MergeVectorAndFulltext(vec, ft)
	if len(merged) != 2 {
		t.Fatalf("expected 2, got %d", len(merged))
	}
	// Item "1" appears in both lists so should have higher RRF score
	if merged[0].ID != "1" {
		t.Errorf("expected '1' (in both lists) at rank 0, got %s", merged[0].ID)
	}
	if merged[0].Score <= merged[1].Score {
		t.Errorf("item in both lists should score higher: %f <= %f", merged[0].Score, merged[1].Score)
	}
}

func TestFormatPrefResults_Dedup(t *testing.T) {
	results := []db.QdrantSearchResult{
		{ID: "a-1", Score: 0.9, Payload: map[string]any{"memory": "pref1"}},
		{ID: "a-1", Score: 0.8, Payload: map[string]any{"memory": "pref1 dup"}}, // duplicate ID
		{ID: "b-2", Score: 0.7, Payload: map[string]any{"memory": "pref2"}},
	}
	formatted := FormatPrefResults(results)
	if len(formatted) != 2 {
		t.Fatalf("expected 2, got %d", len(formatted))
	}
}

func TestFormatPrefResults_EmptyMemory(t *testing.T) {
	results := []db.QdrantSearchResult{
		{ID: "c-3", Score: 0.9, Payload: map[string]any{"something": "else"}}, // no memory field
	}
	formatted := FormatPrefResults(results)
	if len(formatted) != 0 {
		t.Fatalf("expected 0 (no memory field), got %d", len(formatted))
	}
}

func TestToSearchItems_Roundtrip(t *testing.T) {
	items := []map[string]any{
		{
			"id":       "1",
			"memory":   "test",
			"metadata": map[string]any{"relativity": 0.9},
		},
	}
	embByID := map[string][]float32{"1": {0.1, 0.2, 0.3}}
	searchItems := ToSearchItems(items, embByID, "text")
	if len(searchItems) != 1 {
		t.Fatalf("expected 1, got %d", len(searchItems))
	}
	if searchItems[0].Score != 0.9 {
		t.Errorf("score = %f, want 0.9", searchItems[0].Score)
	}
	if len(searchItems[0].Embedding) != 3 {
		t.Errorf("embedding len = %d, want 3", len(searchItems[0].Embedding))
	}
	back := FromSearchItems(searchItems)
	if len(back) != 1 || back[0]["memory"] != "test" {
		t.Errorf("roundtrip failed")
	}
}

func TestTrimSlice(t *testing.T) {
	items := make([]map[string]any, 10)
	trimmed := TrimSlice(items, 5)
	if len(trimmed) != 5 {
		t.Errorf("expected 5, got %d", len(trimmed))
	}
	// Under limit — should not change
	trimmed2 := TrimSlice(items[:3], 5)
	if len(trimmed2) != 3 {
		t.Errorf("expected 3, got %d", len(trimmed2))
	}
}
