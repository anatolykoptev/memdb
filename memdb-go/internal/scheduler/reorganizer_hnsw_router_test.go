package scheduler

// reorganizer_hnsw_router_test.go — verifies that findNearDuplicates/findNearDuplicatesByIDs
// route to HNSW or legacy methods based on the dup strategy (auto|legacy|hnsw).

import (
	"context"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// spyPostgres satisfies reorgPostgres and records which FindNearDuplicates variant was called.
// All other methods are no-ops. dupCount controls the value returned by
// CountMemoriesByUserAndTypes (used by the auto router).
type spyPostgres struct {
	legacyCalls int
	hnswCalls   int
	dupCount    int64 // value returned by CountMemoriesByUserAndTypes (default 0)
	countCalls  int
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
func (s *spyPostgres) CountMemoriesByUserAndTypes(_ context.Context, _ string, _ []string) (int64, error) {
	s.countCalls++
	return s.dupCount, nil
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
func (s *spyPostgres) ListMemoriesByHierarchyLevel(_ context.Context, _, _ string, _ int) ([]db.HierarchyMemory, error) {
	return nil, nil
}
func (s *spyPostgres) CreateMemoryEdgeWithConfidence(_ context.Context, _, _, _, _, _ string, _ float64, _ string) error {
	return nil
}
func (s *spyPostgres) InsertTreeConsolidationEvent(_ context.Context, _, _, _ string, _ []string, _, _, _, _ string) error {
	return nil
}
func (s *spyPostgres) SetHierarchyLevel(_ context.Context, _, _, _, _ string) error      { return nil }
func (s *spyPostgres) PromoteClusterChild(_ context.Context, _, _, _, _, _ string) error { return nil }
func (s *spyPostgres) ClearCEScoresTopK(_ context.Context, _ string) error               { return nil }
func (s *spyPostgres) ClearCEScoresTopKForNeighbor(_ context.Context, _ string) error    { return nil }
func (s *spyPostgres) UpsertWikiPage(_ context.Context, _ db.UpsertWikiPageParams) (db.WikiPage, error) {
	return db.WikiPage{}, nil
}

func TestReorganizer_Router_LegacyByDefault(t *testing.T) {
	spy := &spyPostgres{}
	r := &Reorganizer{postgres: spy, dupStrategy: DupLegacy, dupCrossover: defaultDupCrossover}

	_, _ = r.findNearDuplicates(context.Background(), "cube-x")
	_, _ = r.findNearDuplicatesByIDs(context.Background(), "cube-x", []string{"a"})

	if spy.hnswCalls != 0 {
		t.Errorf("dupStrategy=legacy but hnswCalls=%d", spy.hnswCalls)
	}
	if spy.legacyCalls != 2 {
		t.Errorf("expected 2 legacy calls, got %d", spy.legacyCalls)
	}
	if spy.countCalls != 0 {
		t.Errorf("explicit legacy should not count, got countCalls=%d", spy.countCalls)
	}
}

func TestReorganizer_Router_HNSWWhenEnabled(t *testing.T) {
	spy := &spyPostgres{}
	r := &Reorganizer{postgres: spy, dupStrategy: DupHNSW, dupCrossover: defaultDupCrossover}

	_, _ = r.findNearDuplicates(context.Background(), "cube-x")
	_, _ = r.findNearDuplicatesByIDs(context.Background(), "cube-x", []string{"a"})

	if spy.legacyCalls != 0 {
		t.Errorf("dupStrategy=hnsw but legacyCalls=%d", spy.legacyCalls)
	}
	if spy.hnswCalls != 2 {
		t.Errorf("expected 2 hnsw calls, got %d", spy.hnswCalls)
	}
	if spy.countCalls != 0 {
		t.Errorf("explicit hnsw should not count, got countCalls=%d", spy.countCalls)
	}
}

// TestReorganizer_Router_AutoRouting verifies the auto strategy counts
// candidate memories and picks legacy below the crossover, HNSW at/above it.
// The crossover comparison is centralized in resolveDupStrategy.
func TestReorganizer_Router_AutoRouting(t *testing.T) {
	t.Run("below crossover → legacy", func(t *testing.T) {
		spy := &spyPostgres{dupCount: 999}
		r := &Reorganizer{postgres: spy, dupStrategy: DupAuto, dupCrossover: 1000}

		_, _ = r.findNearDuplicates(context.Background(), "cube-x")
		_, _ = r.findNearDuplicatesByIDs(context.Background(), "cube-x", []string{"a"})

		if spy.countCalls != 2 {
			t.Errorf("auto should count once per call, got countCalls=%d", spy.countCalls)
		}
		if spy.hnswCalls != 0 {
			t.Errorf("count 999 < crossover 1000 but hnswCalls=%d", spy.hnswCalls)
		}
		if spy.legacyCalls != 2 {
			t.Errorf("expected 2 legacy calls, got %d", spy.legacyCalls)
		}
	})

	t.Run("at crossover → hnsw", func(t *testing.T) {
		spy := &spyPostgres{dupCount: 1000}
		r := &Reorganizer{postgres: spy, dupStrategy: DupAuto, dupCrossover: 1000}

		_, _ = r.findNearDuplicates(context.Background(), "cube-x")
		_, _ = r.findNearDuplicatesByIDs(context.Background(), "cube-x", []string{"a"})

		if spy.countCalls != 2 {
			t.Errorf("auto should count once per call, got countCalls=%d", spy.countCalls)
		}
		if spy.legacyCalls != 0 {
			t.Errorf("count 1000 >= crossover 1000 but legacyCalls=%d", spy.legacyCalls)
		}
		if spy.hnswCalls != 2 {
			t.Errorf("expected 2 hnsw calls, got %d", spy.hnswCalls)
		}
	})

	t.Run("above crossover → hnsw", func(t *testing.T) {
		spy := &spyPostgres{dupCount: 5000}
		r := &Reorganizer{postgres: spy, dupStrategy: DupAuto, dupCrossover: 1000}

		_, _ = r.findNearDuplicates(context.Background(), "cube-x")

		if spy.legacyCalls != 0 {
			t.Errorf("count 5000 > crossover 1000 but legacyCalls=%d", spy.legacyCalls)
		}
		if spy.hnswCalls != 1 {
			t.Errorf("expected 1 hnsw call, got %d", spy.hnswCalls)
		}
	})
}

// TestReorganizer_Router_AutoCountTypes verifies the auto router counts the
// expected memory types (LongTermMemory + UserMemory + EpisodicMemory).
func TestReorganizer_Router_AutoCountTypes(t *testing.T) {
	spy := &spyPostgres{dupCount: 0}
	r := &Reorganizer{postgres: spy, dupStrategy: DupAuto, dupCrossover: 1000}

	_, _ = r.findNearDuplicates(context.Background(), "cube-x")

	want := []string{"LongTermMemory", "UserMemory", "EpisodicMemory"}
	if len(dupRouterTypes) != len(want) {
		t.Fatalf("dupRouterTypes len = %d, want %d", len(dupRouterTypes), len(want))
	}
	for i, v := range dupRouterTypes {
		if v != want[i] {
			t.Errorf("dupRouterTypes[%d] = %q, want %q", i, v, want[i])
		}
	}
}
