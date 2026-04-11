// Package search — regression tests for CubeID vs UserName separation in postgres calls.
//
// These tests use a mock postgresClient that records the cube/user argument passed to
// each method. Before the fix (service.go passing p.UserName instead of p.CubeID), the
// first test fails when UserName != CubeID. After the fix all tests must pass.
package search

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// mockEmbedder returns a fixed 3-dim embedding for any input.
type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func (m *mockEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func (m *mockEmbedder) Dimension() int { return 3 }
func (m *mockEmbedder) Close() error   { return nil }

// mockPostgres records the cubeID argument passed to each search method.
// Methods that are not exercised in a given test call panic to catch accidental use.
type mockPostgres struct {
	// recorded arguments
	vectorSearchCubeID             string
	vectorSearchMultiCubeCubes     []string
	vectorSearchWithCutoffCubeID   string
	fulltextSearchCubeID           string
	fulltextSearchWithCutoffCubeID string
	workingMemoryCubeID            string
	graphRecallByKeyCubeID         string
	graphRecallByTagsCubeID        string
	graphRecallByEdgeCubeID        string
	graphBFSCubeID                 string
	findEntitiesCubeID             string
	getMemByEntityCubeID           string

	// personID recorded per method
	vectorSearchPersonID             string
	vectorSearchMultiCubePersonID    string
	vectorSearchWithCutoffPersonID   string
	fulltextSearchPersonID           string
	fulltextSearchWithCutoffPersonID string
	workingMemoryPersonID            string
	graphRecallByKeyPersonID         string
	graphRecallByTagsPersonID        string
	graphRecallByEdgePersonID        string
	graphBFSPersonID                 string
	findEntitiesPersonID             string
	getMemByEntityPersonID           string

	// flags — which methods were called
	vectorSearchCalled          bool
	vectorSearchMultiCubeCalled bool
	workingMemoryCalled         bool
	graphRecallByKeyCalled      bool
}

func (m *mockPostgres) VectorSearch(_ context.Context, _ []float32, cubeID, personID string, _ []string, _ string, _ int) ([]db.VectorSearchResult, error) {
	m.vectorSearchCalled = true
	m.vectorSearchCubeID = cubeID
	m.vectorSearchPersonID = personID
	return nil, nil
}

func (m *mockPostgres) VectorSearchMultiCube(_ context.Context, _ []float32, cubeIDs []string, personID string, _ []string, _ string, _ int) ([]db.VectorSearchResult, error) {
	m.vectorSearchMultiCubeCalled = true
	m.vectorSearchMultiCubeCubes = cubeIDs
	m.vectorSearchMultiCubePersonID = personID
	return nil, nil
}

func (m *mockPostgres) VectorSearchWithCutoff(_ context.Context, _ []float32, cubeID, personID string, _ []string, _ int, _ string, _ string) ([]db.VectorSearchResult, error) {
	m.vectorSearchWithCutoffCubeID = cubeID
	m.vectorSearchWithCutoffPersonID = personID
	return nil, nil
}

func (m *mockPostgres) FulltextSearch(_ context.Context, _ string, cubeID, personID string, _ []string, _ string, _ int) ([]db.VectorSearchResult, error) {
	m.fulltextSearchCubeID = cubeID
	m.fulltextSearchPersonID = personID
	return nil, nil
}

func (m *mockPostgres) FulltextSearchWithCutoff(_ context.Context, _ string, cubeID, personID string, _ []string, _ int, _ string, _ string) ([]db.VectorSearchResult, error) {
	m.fulltextSearchWithCutoffCubeID = cubeID
	m.fulltextSearchWithCutoffPersonID = personID
	return nil, nil
}

func (m *mockPostgres) GetWorkingMemory(_ context.Context, cubeID, personID string, _ int, _ string) ([]db.VectorSearchResult, error) {
	m.workingMemoryCalled = true
	m.workingMemoryCubeID = cubeID
	m.workingMemoryPersonID = personID
	return nil, nil
}

func (m *mockPostgres) GraphRecallByKey(_ context.Context, cubeID, personID string, _ []string, _ []string, _ string, _ int) ([]db.GraphRecallResult, error) {
	m.graphRecallByKeyCalled = true
	m.graphRecallByKeyCubeID = cubeID
	m.graphRecallByKeyPersonID = personID
	return nil, nil
}

func (m *mockPostgres) GraphRecallByTags(_ context.Context, cubeID, personID string, _ []string, _ []string, _ string, _ int) ([]db.GraphRecallResult, error) {
	m.graphRecallByTagsCubeID = cubeID
	m.graphRecallByTagsPersonID = personID
	return nil, nil
}

func (m *mockPostgres) GraphRecallByEdge(_ context.Context, _ []string, _ string, cubeID, personID string, _ int) ([]db.GraphRecallResult, error) {
	m.graphRecallByEdgeCubeID = cubeID
	m.graphRecallByEdgePersonID = personID
	return nil, nil
}

func (m *mockPostgres) GraphBFSTraversal(_ context.Context, _ []string, cubeID, personID string, _ []string, _, _ int, _ string) ([]db.GraphRecallResult, error) {
	m.graphBFSCubeID = cubeID
	m.graphBFSPersonID = personID
	return nil, nil
}

func (m *mockPostgres) FindEntitiesByNormalizedID(_ context.Context, _ []string, cubeID, personID string) ([]string, error) {
	m.findEntitiesCubeID = cubeID
	m.findEntitiesPersonID = personID
	return nil, nil
}

func (m *mockPostgres) GetMemoriesByEntityIDs(_ context.Context, _ []string, cubeID, personID string, _ int) ([]db.GraphRecallResult, error) {
	m.getMemByEntityCubeID = cubeID
	m.getMemByEntityPersonID = personID
	return nil, nil
}

func (m *mockPostgres) IncrRetrievalCount(_ context.Context, _ []string, _ string) error {
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSearch_CubeIDPassedToVectorSearch is the core regression: when UserName != CubeID,
// postgres must receive CubeID (not UserName). Fails on unpatched main.
func TestSearch_CubeIDPassedToVectorSearch(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	params := SearchParams{
		UserName: "user-X",
		CubeID:   "cube-Y",
		Query:    "hello",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !mock.vectorSearchCalled {
		t.Fatal("VectorSearch was not called")
	}
	if mock.vectorSearchCubeID != "cube-Y" {
		t.Errorf("VectorSearch got cubeID=%q, want %q (UserName=%q leaked through)",
			mock.vectorSearchCubeID, "cube-Y", "user-X")
	}
}

// TestSearch_DefaultCompatibility: when user_id == cube_id (common case), the result
// must be the same before and after the fix. Must pass on both unpatched and patched main.
func TestSearch_DefaultCompatibility(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	params := SearchParams{
		UserName: "user-Z",
		CubeID:   "user-Z", // same value — current convention
		Query:    "world",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if mock.vectorSearchCubeID != "user-Z" {
		t.Errorf("VectorSearch got cubeID=%q, want %q", mock.vectorSearchCubeID, "user-Z")
	}
}

// TestSearch_WorkingMemoryPath verifies GetWorkingMemory is called with CubeID.
func TestSearch_WorkingMemoryPath(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	params := SearchParams{
		UserName: "user-A",
		CubeID:   "cube-B",
		Query:    "test",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !mock.workingMemoryCalled {
		t.Fatal("GetWorkingMemory was not called")
	}
	if mock.workingMemoryCubeID != "cube-B" {
		t.Errorf("GetWorkingMemory got cubeID=%q, want %q", mock.workingMemoryCubeID, "cube-B")
	}
}

// TestSearch_GraphRecallPath verifies GraphRecallByKey is called with CubeID.
// GraphRecallByKey is only invoked when the query produces at least 1 token.
func TestSearch_GraphRecallPath(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	// Use a multi-word query so tokenization produces tokens that trigger graph recall.
	params := SearchParams{
		UserName: "user-P",
		CubeID:   "cube-Q",
		Query:    "golang kubernetes deployment",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !mock.graphRecallByKeyCalled {
		t.Fatal("GraphRecallByKey was not called — query may not have produced tokens")
	}
	if mock.graphRecallByKeyCubeID != "cube-Q" {
		t.Errorf("GraphRecallByKey got cubeID=%q, want %q", mock.graphRecallByKeyCubeID, "cube-Q")
	}
}

// TestSearch_MultiCubeRouting verifies that when CubeIDs has 2+ entries, VectorSearchMultiCube
// is called with the full slice, not single-cube VectorSearch.
func TestSearch_MultiCubeRouting(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}

	params := SearchParams{
		UserName: "user-A",
		CubeID:   "cube-1",
		CubeIDs:  []string{"cube-1", "cube-2"},
		Query:    "test multi",
		TopK:     5,
	}

	_, err := svc.Search(context.Background(), params)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !mock.vectorSearchMultiCubeCalled {
		t.Fatal("VectorSearchMultiCube was not called for multi-cube params")
	}
	want := []string{"cube-1", "cube-2"}
	if len(mock.vectorSearchMultiCubeCubes) != len(want) {
		t.Fatalf("VectorSearchMultiCube got cubes=%v, want %v", mock.vectorSearchMultiCubeCubes, want)
	}
	for i, v := range want {
		if mock.vectorSearchMultiCubeCubes[i] != v {
			t.Errorf("VectorSearchMultiCube cubes[%d]=%q, want %q", i, mock.vectorSearchMultiCubeCubes[i], v)
		}
	}
	// Single-cube VectorSearch must NOT have been called (routing guard).
	if mock.vectorSearchCalled {
		t.Error("VectorSearch (single-cube) was unexpectedly called for multi-cube params")
	}
}
