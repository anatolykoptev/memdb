// Package search — empty_cube_test.go: M14.Y4.1 empty-cube fast-fail tests.
//
// Coverage:
//   - TestSearch_EmptyCube_FastFail: cube count = 0 → returns empty result
//     before any pipeline stage fires (d4, embed, DB fanout all absent).
//   - TestSearch_NonEmptyCube_NoFastFail: cube has entries → normal path,
//     fast-fail is not triggered and VectorSearch is called.
//   - TestSearch_EmptyCube_NilCubeID: CubeID="" → fast-fail skipped (guard),
//     normal pipeline proceeds.
package search

import (
	"context"
	"testing"
)

// emptyAwareMockPG wraps mockPostgres and controls what CubeHasEntries returns.
// All other methods delegate to the embedded mock (returns nil/empty/no error).
type emptyAwareMockPG struct {
	mockPostgres
	// hasEntries is returned by CubeHasEntries.
	hasEntries bool
}

func (m *emptyAwareMockPG) CubeHasEntries(_ context.Context, _ string) (bool, error) {
	return m.hasEntries, nil
}

// TestSearch_EmptyCube_FastFail verifies that when CubeHasEntries returns false,
// Search returns an empty result immediately without calling VectorSearch.
// This is the core regression guard for M14.Y4.1: 2-3 s wasted on empty cubes.
func TestSearch_EmptyCube_FastFail(t *testing.T) {
	mock := &emptyAwareMockPG{hasEntries: false}
	svc := &SearchService{
		postgres: mock,
		embedder: &mockEmbedder{},
		logger:   discardLogger(),
	}

	params := SearchParams{
		UserName: "user-new",
		CubeID:   "cube-empty",
		Query:    "any query",
		TopK:     5,
	}

	out, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if out == nil || out.Result == nil {
		t.Fatal("Search returned nil output for empty cube")
	}

	// Fast-fail must return empty slices (not nil) to satisfy JSON contract.
	if out.Result.TextMem == nil {
		t.Error("TextMem must be non-nil (empty slice) for empty-cube fast-fail")
	}
	if len(out.Result.TextMem) != 0 {
		t.Errorf("TextMem must be empty for empty cube, got %d buckets", len(out.Result.TextMem))
	}

	// d4_query_rewrite and embed_query must NOT have run: VectorSearch should
	// not have been called (embed only runs as part of pipeline).
	if mock.vectorSearchCalled {
		t.Error("VectorSearch must NOT be called on empty-cube fast-fail path")
	}
}

// TestSearch_NonEmptyCube_NoFastFail verifies that when CubeHasEntries returns true,
// the normal pipeline is executed and VectorSearch is called.
// Bytewise-parity guard: the fast-fail must be invisible to existing cubes.
func TestSearch_NonEmptyCube_NoFastFail(t *testing.T) {
	mock := &emptyAwareMockPG{hasEntries: true}
	svc := &SearchService{
		postgres: mock,
		embedder: &mockEmbedder{},
		logger:   discardLogger(),
	}

	params := SearchParams{
		UserName: "user-existing",
		CubeID:   "cube-nonempty",
		Query:    "some query",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	// Normal path: VectorSearch must have been called.
	if !mock.vectorSearchCalled {
		t.Error("VectorSearch must be called for non-empty cube (normal pipeline)")
	}
}

// TestSearch_EmptyCube_NilCubeID verifies that when CubeID is empty the fast-fail
// guard is skipped and the normal pipeline runs. An empty CubeID means the caller
// didn't supply a cube context — we don't want to accidentally fast-fail those.
func TestSearch_EmptyCube_NilCubeID(t *testing.T) {
	mock := &emptyAwareMockPG{hasEntries: false} // would return empty if queried
	svc := &SearchService{
		postgres: mock,
		embedder: &mockEmbedder{},
		logger:   discardLogger(),
	}

	params := SearchParams{
		UserName: "user-x",
		CubeID:   "", // empty CubeID — guard must skip CubeHasEntries
		Query:    "hello",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	// Pipeline ran: VectorSearch was called despite hasEntries=false,
	// because CubeID="" short-circuits the fast-fail guard.
	if !mock.vectorSearchCalled {
		t.Error("VectorSearch must be called when CubeID is empty (guard bypassed fast-fail)")
	}
}
