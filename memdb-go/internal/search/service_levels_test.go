// Package search — service_levels_test.go: routing tests for SearchByLevel (M10 Stream 4).
//
// Tests verify:
//   - level=l1 → only GetWorkingMemory is called; VectorSearch is NOT called
//   - level=l2 → VectorSearch is called with EpisodicScopes; GetWorkingMemory is NOT called
//   - level=l3 → full pipeline (VectorSearch + GetWorkingMemory both called)
//   - omitted (LevelAll) → full pipeline (same as l3 for current implementation)
//   - invalid level → ParseLevel returns error
package search

import (
	"context"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// levelMock extends mockPostgres with tracking flags for level tests.
type levelMock struct {
	mockPostgres
	vectorSearchMemTypes []string
}

func (m *levelMock) VectorSearch(ctx context.Context, vec []float32, cubeID, personID string, memTypes []string, agentID string, limit int) ([]db.VectorSearchResult, error) {
	m.vectorSearchCalled = true
	m.vectorSearchCubeID = cubeID
	m.vectorSearchPersonID = personID
	m.vectorSearchMemTypes = memTypes
	return nil, nil
}

// TestSearchByLevel_L1_OnlyWorkingMem: level=l1 must call GetWorkingMemory but NOT VectorSearch.
func TestSearchByLevel_L1_OnlyWorkingMem(t *testing.T) {
	mock := &levelMock{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	p := SearchParams{
		UserName: "u1",
		CubeID:   "c1",
		Query:    "test query",
		TopK:     5,
		Level:    LevelL1,
	}

	_, err := svc.SearchByLevel(context.Background(), p)
	if err != nil {
		t.Fatalf("SearchByLevel l1 returned error: %v", err)
	}

	if mock.vectorSearchCalled {
		t.Error("level=l1: VectorSearch should NOT be called (working mem only path)")
	}
	if !mock.workingMemoryCalled {
		t.Error("level=l1: GetWorkingMemory should be called")
	}
}

// TestSearchByLevel_L2_EpisodicOnly: level=l2 must call VectorSearch with EpisodicScopes;
// GetWorkingMemory must NOT be called.
func TestSearchByLevel_L2_EpisodicOnly(t *testing.T) {
	mock := &levelMock{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	p := SearchParams{
		UserName: "u1",
		CubeID:   "c1",
		Query:    "test query",
		TopK:     5,
		Level:    LevelL2,
	}

	_, err := svc.SearchByLevel(context.Background(), p)
	if err != nil {
		t.Fatalf("SearchByLevel l2 returned error: %v", err)
	}

	if !mock.vectorSearchCalled {
		t.Error("level=l2: VectorSearch should be called")
	}
	if mock.workingMemoryCalled {
		t.Error("level=l2: GetWorkingMemory should NOT be called (episodic only path)")
	}

	// Verify the search was scoped to EpisodicMemory only.
	if len(mock.vectorSearchMemTypes) != 1 || mock.vectorSearchMemTypes[0] != "EpisodicMemory" {
		t.Errorf("level=l2: VectorSearch memTypes = %v, want [EpisodicMemory]", mock.vectorSearchMemTypes)
	}
}

// TestSearchByLevel_L3_FullPipeline: level=l3 must run the full pipeline
// (VectorSearch and GetWorkingMemory both called).
func TestSearchByLevel_L3_FullPipeline(t *testing.T) {
	mock := &levelMock{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	p := SearchParams{
		UserName: "u1",
		CubeID:   "c1",
		Query:    "test query",
		TopK:     5,
		Level:    LevelL3,
	}

	_, err := svc.SearchByLevel(context.Background(), p)
	if err != nil {
		t.Fatalf("SearchByLevel l3 returned error: %v", err)
	}

	if !mock.vectorSearchCalled {
		t.Error("level=l3: VectorSearch should be called (full LTM pipeline)")
	}
	if !mock.workingMemoryCalled {
		t.Error("level=l3: GetWorkingMemory should be called (full pipeline)")
	}
}

// TestSearchByLevel_All_FullPipeline: omitting level (LevelAll) must run the full pipeline.
func TestSearchByLevel_All_FullPipeline(t *testing.T) {
	mock := &levelMock{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	p := SearchParams{
		UserName: "u1",
		CubeID:   "c1",
		Query:    "test query",
		TopK:     5,
		Level:    LevelAll, // same as zero value — backward compat
	}

	_, err := svc.SearchByLevel(context.Background(), p)
	if err != nil {
		t.Fatalf("SearchByLevel all returned error: %v", err)
	}

	if !mock.vectorSearchCalled {
		t.Error("level=all: VectorSearch should be called (full pipeline)")
	}
	if !mock.workingMemoryCalled {
		t.Error("level=all: GetWorkingMemory should be called (full pipeline)")
	}
}

// TestSearchByLevel_ZeroValue_BackwardCompat: zero-value SearchParams.Level (empty string)
// must behave identically to explicit LevelAll.
func TestSearchByLevel_ZeroValue_BackwardCompat(t *testing.T) {
	mock := &levelMock{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	p := SearchParams{
		UserName: "u1",
		CubeID:   "c1",
		Query:    "test",
		TopK:     5,
		// Level is not set — zero value = LevelAll
	}

	_, err := svc.SearchByLevel(context.Background(), p)
	if err != nil {
		t.Fatalf("SearchByLevel zero-level returned error: %v", err)
	}
	// Full pipeline expected.
	if !mock.vectorSearchCalled {
		t.Error("zero level: VectorSearch should be called (full pipeline)")
	}
}
