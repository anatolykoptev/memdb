package search

// phase35_test.go — unit tests for Phase 3.5 search features:
//   3.5.2 collectResultIDs — extracts IDs from formatted search buckets

import "testing"

func TestCollectResultIDs_Basic(t *testing.T) {
	bucket := []map[string]any{
		{"metadata": map[string]any{"id": "aaa", "memory": "fact 1"}},
		{"metadata": map[string]any{"id": "bbb", "memory": "fact 2"}},
	}
	ids := collectResultIDs(bucket)
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d: %v", len(ids), ids)
	}
}

func TestCollectResultIDs_Dedup(t *testing.T) {
	b1 := []map[string]any{
		{"metadata": map[string]any{"id": "aaa"}},
	}
	b2 := []map[string]any{
		{"metadata": map[string]any{"id": "aaa"}}, // duplicate
		{"metadata": map[string]any{"id": "bbb"}},
	}
	ids := collectResultIDs(b1, b2)
	if len(ids) != 2 {
		t.Fatalf("expected 2 unique ids, got %d: %v", len(ids), ids)
	}
}

func TestCollectResultIDs_MissingMetadata(t *testing.T) {
	bucket := []map[string]any{
		{"score": 0.9}, // no metadata
		{"metadata": map[string]any{"id": "aaa"}},
	}
	ids := collectResultIDs(bucket)
	if len(ids) != 1 || ids[0] != "aaa" {
		t.Fatalf("expected only 'aaa', got %v", ids)
	}
}

func TestCollectResultIDs_EmptyBuckets(t *testing.T) {
	ids := collectResultIDs(nil, []map[string]any{})
	if len(ids) != 0 {
		t.Fatalf("expected empty slice, got %v", ids)
	}
}
