package search

import (
	"context"
	"testing"
)

func TestSearch_PassesPersonIDToVectorSearch(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}
	params := SearchParams{UserName: "krolik", CubeID: "dozor-facts", Query: "test", TopK: 5}
	_, _ = svc.Search(context.Background(), params)

	if !mock.vectorSearchCalled {
		t.Fatal("VectorSearch not called")
	}
	if mock.vectorSearchCubeID != "dozor-facts" {
		t.Errorf("cubeID: got %q want dozor-facts", mock.vectorSearchCubeID)
	}
	if mock.vectorSearchPersonID != "krolik" {
		t.Errorf("personID: got %q want krolik", mock.vectorSearchPersonID)
	}
}

func TestSearch_PassesPersonIDToFulltextSearch(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}
	params := SearchParams{UserName: "alice", CubeID: "cube-A", Query: "hello world", TopK: 5}
	_, _ = svc.Search(context.Background(), params)

	if mock.fulltextSearchPersonID != "alice" {
		t.Errorf("fulltext personID: got %q want alice", mock.fulltextSearchPersonID)
	}
}

func TestSearch_PassesPersonIDToWorkingMemory(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}
	params := SearchParams{UserName: "bob", CubeID: "cube-B", Query: "test", TopK: 5}
	_, _ = svc.Search(context.Background(), params)

	if !mock.workingMemoryCalled {
		t.Fatal("GetWorkingMemory not called")
	}
	if mock.workingMemoryPersonID != "bob" {
		t.Errorf("working_memory personID: got %q want bob", mock.workingMemoryPersonID)
	}
}

func TestSearch_MultiCubePassesPersonID(t *testing.T) {
	mock := &mockPostgres{}
	svc := &SearchService{postgres: mock, embedder: &mockEmbedder{}, logger: discardLogger()}
	params := SearchParams{
		UserName: "multi-person",
		CubeID:   "cube-1",
		CubeIDs:  []string{"cube-1", "cube-2"},
		Query:    "test multi",
		TopK:     5,
	}
	_, _ = svc.Search(context.Background(), params)

	if !mock.vectorSearchMultiCubeCalled {
		t.Fatal("VectorSearchMultiCube not called")
	}
	if mock.vectorSearchMultiCubePersonID != "multi-person" {
		t.Errorf("multi-cube personID: got %q want multi-person", mock.vectorSearchMultiCubePersonID)
	}
}
