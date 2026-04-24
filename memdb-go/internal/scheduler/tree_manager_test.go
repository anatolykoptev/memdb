package scheduler

// tree_manager_test.go — end-to-end tree reorganizer tests.
//
// Covers:
//   1. Happy path: 10 raw memories with pre-clustered embeddings (2 × 5)
//      produces 2 episodic parents + 10 CONSOLIDATED_INTO edges.
//   2. Disabled by default: TreeHierarchyEnabled() returns false unless env set.
//   3. clusterByCosine correctness: disjoint groups are separated; below-size
//      clusters are dropped.
//
// All external services (Postgres, LLM, embedder) are stubbed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// ---- treeStub: in-memory Postgres stub for D3 interface additions ----------

type treeStub struct {
	mu       sync.Mutex
	rawMems  []db.HierarchyMemory
	epMems   []db.HierarchyMemory
	inserted []db.MemoryInsertNode
	edges    []treeEdge
	setHier  []treeHier
	events   []treeEvent
}

type treeEdge struct {
	From, To, Relation string
	Confidence         float64
	Rationale          string
}
type treeHier struct {
	ID, Level, ParentID string
}
type treeEvent struct {
	EventID, CubeID, ParentID, Tier string
	ChildIDs                        []string
}

func (s *treeStub) ListMemoriesByHierarchyLevel(_ context.Context, _, level string, _ int) ([]db.HierarchyMemory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch level {
	case hierarchyLevelRaw:
		return s.rawMems, nil
	case hierarchyLevelEpisodic:
		return s.epMems, nil
	}
	return nil, nil
}

func (s *treeStub) CreateMemoryEdge(_ context.Context, from, to, rel, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edges = append(s.edges, treeEdge{From: from, To: to, Relation: rel})
	return nil
}
func (s *treeStub) CreateMemoryEdgeWithConfidence(_ context.Context, from, to, rel, _, _ string, conf float64, rat string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edges = append(s.edges, treeEdge{From: from, To: to, Relation: rel, Confidence: conf, Rationale: rat})
	return nil
}
func (s *treeStub) SetHierarchyLevel(_ context.Context, id, level, parent, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setHier = append(s.setHier, treeHier{ID: id, Level: level, ParentID: parent})
	return nil
}
func (s *treeStub) InsertTreeConsolidationEvent(_ context.Context, ev, cube, parent string, children []string, tier, _, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cpy := make([]string, len(children))
	copy(cpy, children)
	s.events = append(s.events, treeEvent{EventID: ev, CubeID: cube, ParentID: parent, ChildIDs: cpy, Tier: tier})
	return nil
}
func (s *treeStub) InsertMemoryNodes(_ context.Context, nodes []db.MemoryInsertNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserted = append(s.inserted, nodes...)
	return nil
}

// no-op stubs for unused surface — keeps treeStub satisfying reorgPostgres.
func (s *treeStub) FindNearDuplicates(_ context.Context, _ string, _ float64, _ int) ([]db.DuplicatePair, error) {
	return nil, nil
}
func (s *treeStub) FindNearDuplicatesByIDs(_ context.Context, _ string, _ []string, _ float64, _ int) ([]db.DuplicatePair, error) {
	return nil, nil
}
func (s *treeStub) FindNearDuplicatesHNSW(_ context.Context, _ string, _ float64, _, _ int) ([]db.DuplicatePair, error) {
	return nil, nil
}
func (s *treeStub) FindNearDuplicatesHNSWByIDs(_ context.Context, _ string, _ []string, _ float64, _, _ int) ([]db.DuplicatePair, error) {
	return nil, nil
}
func (s *treeStub) UpdateMemoryNodeFull(_ context.Context, _, _, _, _ string) error { return nil }
func (s *treeStub) SoftDeleteMerged(_ context.Context, _, _, _ string) error        { return nil }
func (s *treeStub) DeleteByPropertyIDs(_ context.Context, _ []string, _ string) (int64, error) {
	return 0, nil
}
func (s *treeStub) InvalidateEdgesByMemoryID(_ context.Context, _, _ string) error       { return nil }
func (s *treeStub) InvalidateEntityEdgesByMemoryID(_ context.Context, _, _ string) error { return nil }
func (s *treeStub) UpsertEntityNodeWithEmbedding(_ context.Context, _, _, _, _, _ string) (string, error) {
	return "", nil
}
func (s *treeStub) UpsertEntityEdge(_ context.Context, _, _, _, _, _, _, _ string) error {
	return nil
}
func (s *treeStub) GetMemoryByPropertyIDs(_ context.Context, _ []string, _ string) ([]db.MemNode, error) {
	return nil, nil
}
func (s *treeStub) GetMemoriesByPropertyIDs(_ context.Context, _ []string) ([]map[string]any, error) {
	return nil, nil
}
func (s *treeStub) FilterExistingContentHashes(_ context.Context, _ []string, _ string) (map[string]bool, error) {
	return nil, nil
}
func (s *treeStub) VectorSearch(_ context.Context, _ []float32, _, _ string, _ []string, _ string, _ int) ([]db.VectorSearchResult, error) {
	return nil, nil
}
func (s *treeStub) SearchLTMByVector(_ context.Context, _, _ string, _ float64, _ int) ([]db.LTMSearchResult, error) {
	return nil, nil
}
func (s *treeStub) CountWorkingMemory(_ context.Context, _ string) (int64, error) { return 0, nil }
func (s *treeStub) GetWorkingMemoryOldestFirst(_ context.Context, _ string, _ int) ([]db.MemNode, error) {
	return nil, nil
}
func (s *treeStub) DecayAndArchiveImportance(_ context.Context, _ string, _, _ float64, _ string) (int64, error) {
	return 0, nil
}

// compile-time interface check.
var _ reorgPostgres = (*treeStub)(nil)

// ---- treeEmbedder: returns a fixed unit vector for any summary ----

type treeEmbedder struct{}

func (e *treeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}
func (e *treeEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}
func (e *treeEmbedder) Dimension() int { return 3 }
func (e *treeEmbedder) Close() error   { return nil }

// ---- tree-aware mock LLM: returns a summary payload ----

func tierMockServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `{"summary":"Cluster summary."}`}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// makeHierarchyMems returns n memories whose embeddings share groupVec
// (normalised) plus tiny jitter, so cosineBetween within the group stays ≥ 0.99.
func makeHierarchyMems(prefix string, n int, groupVec []float32) []db.HierarchyMemory {
	out := make([]db.HierarchyMemory, n)
	for i := 0; i < n; i++ {
		v := make([]float32, len(groupVec))
		copy(v, groupVec)
		// small jitter on one axis — keeps cosine in the high-0.99 range.
		v[0] += float32(i) * 1e-4
		out[i] = db.HierarchyMemory{
			ID:        fmt.Sprintf("%s-%d", prefix, i),
			Text:      fmt.Sprintf("mem-%s-%d", prefix, i),
			Embedding: v,
		}
	}
	return out
}

// ---- Test 1: happy path (2 clusters × 5 → 2 episodic parents + 10 edges) ----

func TestTreeManager_RawToEpisodic_Happy(t *testing.T) {
	var calls atomic.Int32
	srv := tierMockServer(t, &calls)
	defer srv.Close()

	groupA := []float32{1, 0, 0, 0}
	groupB := []float32{0, 1, 0, 0}
	raw := append(makeHierarchyMems("a", 5, groupA), makeHierarchyMems("b", 5, groupB)...)

	stub := &treeStub{rawMems: raw}
	r := &Reorganizer{
		postgres:  stub,
		embedder:  &treeEmbedder{},
		llmClient: testLLMClient(srv.URL),
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	r.RunTreeReorgForCube(context.Background(), "cube-test")

	// 2 episodic parents inserted (one per cluster)
	if len(stub.inserted) != 2 {
		t.Errorf("expected 2 tier-parent inserts, got %d", len(stub.inserted))
	}
	// 10 CONSOLIDATED_INTO edges (5 per cluster)
	consolidated := 0
	for _, e := range stub.edges {
		if e.Relation == "CONSOLIDATED_INTO" {
			consolidated++
		}
	}
	if consolidated != 10 {
		t.Errorf("expected 10 CONSOLIDATED_INTO edges, got %d", consolidated)
	}
	// 10 SetHierarchyLevel calls (one per child)
	if len(stub.setHier) != 10 {
		t.Errorf("expected 10 SetHierarchyLevel calls, got %d", len(stub.setHier))
	}
	// 2 audit events (one per consolidation)
	if len(stub.events) != 2 {
		t.Errorf("expected 2 audit events, got %d", len(stub.events))
	}
	// LLM called exactly twice (once per cluster)
	if calls.Load() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", calls.Load())
	}
}

// ---- Test 2: env flag default OFF ------------------------------------------

func TestTreeHierarchyEnabled_DefaultFalse(t *testing.T) {
	t.Setenv("MEMDB_REORG_HIERARCHY", "")
	if TreeHierarchyEnabled() {
		t.Error("TreeHierarchyEnabled should be false when env unset")
	}
	t.Setenv("MEMDB_REORG_HIERARCHY", "true")
	if !TreeHierarchyEnabled() {
		t.Error("TreeHierarchyEnabled should be true when env=true")
	}
}

// ---- Test 3: clusterByCosine correctness -----------------------------------

func TestClusterByCosine_SplitsDisjointGroups(t *testing.T) {
	groupA := []float32{1, 0, 0}
	groupB := []float32{0, 1, 0}
	raw := append(makeHierarchyMems("a", 4, groupA), makeHierarchyMems("b", 4, groupB)...)

	clusters := clusterByCosine(raw, 0.7, 3)
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	for _, c := range clusters {
		if len(c) != 4 {
			t.Errorf("expected cluster size 4, got %d", len(c))
		}
	}
}

func TestClusterByCosine_DropsBelowMinSize(t *testing.T) {
	groupA := []float32{1, 0, 0}
	groupB := []float32{0, 1, 0}
	raw := append(makeHierarchyMems("a", 5, groupA), makeHierarchyMems("b", 2, groupB)...)

	clusters := clusterByCosine(raw, 0.7, 3)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster (size-2 dropped), got %d", len(clusters))
	}
	if len(clusters[0]) != 5 {
		t.Errorf("expected remaining cluster size 5, got %d", len(clusters[0]))
	}
}
