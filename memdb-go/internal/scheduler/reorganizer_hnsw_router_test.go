package scheduler

// reorganizer_hnsw_router_test.go — verifies that findNearDuplicates/findNearDuplicatesByIDs
// route to HNSW or legacy methods based on the useHNSW flag.

import (
	"context"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// spyPostgres satisfies reorgPostgres and records which FindNearDuplicates variant was called.
// All other methods are no-ops.
type spyPostgres struct {
	legacyCalls int
	hnswCalls   int
}

func (s *spyPostgres) FindNearDuplicates(_ context.Context, _ string, _ float64, _ int) ([]db.DuplicatePair, error) {
	s.legacyCalls++
	return nil, nil
}
func (s *spyPostgres) FindNearDuplicatesByIDs(_ context.Context, _ string, _ []string, _ float64, _ int) ([]db.DuplicatePair, error) {
	s.legacyCalls++
	return nil, nil
}
func (s *spyPostgres) FindNearDuplicatesHNSW(_ context.Context, _ string, _ float64, _, _ int) ([]db.DuplicatePair, error) {
	s.hnswCalls++
	return nil, nil
}
func (s *spyPostgres) FindNearDuplicatesHNSWByIDs(_ context.Context, _ string, _ []string, _ float64, _, _ int) ([]db.DuplicatePair, error) {
	s.hnswCalls++
	return nil, nil
}

// no-op stubs for remaining interface methods
func (s *spyPostgres) InsertMemoryNodes(_ context.Context, _ []db.MemoryInsertNode) error {
	return nil
}
func (s *spyPostgres) UpdateMemoryNodeFull(_ context.Context, _, _, _, _ string) error { return nil }
func (s *spyPostgres) SoftDeleteMerged(_ context.Context, _, _, _ string) error        { return nil }
func (s *spyPostgres) DeleteByPropertyIDs(_ context.Context, _ []string, _ string) (int64, error) {
	return 0, nil
}
func (s *spyPostgres) CreateMemoryEdge(_ context.Context, _, _, _, _, _ string) error { return nil }
func (s *spyPostgres) InvalidateEdgesByMemoryID(_ context.Context, _, _ string) error { return nil }
func (s *spyPostgres) InvalidateEntityEdgesByMemoryID(_ context.Context, _, _ string) error {
	return nil
}
func (s *spyPostgres) UpsertEntityNodeWithEmbedding(_ context.Context, _, _, _, _, _ string) (string, error) {
	return "", nil
}
func (s *spyPostgres) UpsertEntityEdge(_ context.Context, _, _, _, _, _, _, _ string) error {
	return nil
}
func (s *spyPostgres) GetMemoryByPropertyIDs(_ context.Context, _ []string, _ string) ([]db.MemNode, error) {
	return nil, nil
}
func (s *spyPostgres) GetMemoriesByPropertyIDs(_ context.Context, _ []string) ([]map[string]any, error) {
	return nil, nil
}
func (s *spyPostgres) FilterExistingContentHashes(_ context.Context, _ []string, _ string) (map[string]bool, error) {
	return nil, nil
}
func (s *spyPostgres) VectorSearch(_ context.Context, _ []float32, _, _ string, _ []string, _ string, _ int) ([]db.VectorSearchResult, error) {
	return nil, nil
}
func (s *spyPostgres) SearchLTMByVector(_ context.Context, _, _ string, _ float64, _ int) ([]db.LTMSearchResult, error) {
	return nil, nil
}
func (s *spyPostgres) CountWorkingMemory(_ context.Context, _ string) (int64, error) { return 0, nil }
func (s *spyPostgres) GetWorkingMemoryOldestFirst(_ context.Context, _ string, _ int) ([]db.MemNode, error) {
	return nil, nil
}
func (s *spyPostgres) DecayAndArchiveImportance(_ context.Context, _ string, _, _ float64, _ string) (int64, error) {
	return 0, nil
}

func TestReorganizer_Router_LegacyByDefault(t *testing.T) {
	spy := &spyPostgres{}
	r := &Reorganizer{postgres: spy, useHNSW: false}

	_, _ = r.findNearDuplicates(context.Background(), "cube-x")
	_, _ = r.findNearDuplicatesByIDs(context.Background(), "cube-x", []string{"a"})

	if spy.hnswCalls != 0 {
		t.Errorf("useHNSW=false but hnswCalls=%d", spy.hnswCalls)
	}
	if spy.legacyCalls != 2 {
		t.Errorf("expected 2 legacy calls, got %d", spy.legacyCalls)
	}
}

func TestReorganizer_Router_HNSWWhenEnabled(t *testing.T) {
	spy := &spyPostgres{}
	r := &Reorganizer{postgres: spy, useHNSW: true}

	_, _ = r.findNearDuplicates(context.Background(), "cube-x")
	_, _ = r.findNearDuplicatesByIDs(context.Background(), "cube-x", []string{"a"})

	if spy.legacyCalls != 0 {
		t.Errorf("useHNSW=true but legacyCalls=%d", spy.legacyCalls)
	}
	if spy.hnswCalls != 2 {
		t.Errorf("expected 2 hnsw calls, got %d", spy.hnswCalls)
	}
}
