package scheduler

// reorganizer_automerge_test.go — unit tests for the auto-merge fast path.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// autoMergeSpy extends spyPostgres (defined in reorganizer_hnsw_router_test.go)
// and records SoftDeleteMerged calls.
type autoMergeSpy struct {
	spyPostgres
	softDeleted []string // removeIDs passed to SoftDeleteMerged
}

func (s *autoMergeSpy) SoftDeleteMerged(_ context.Context, removeID, _, _ string) error {
	s.softDeleted = append(s.softDeleted, removeID)
	return nil
}

func TestAutoMergeCluster_PicksLongestText(t *testing.T) {
	spy := &autoMergeSpy{}
	r := &Reorganizer{
		postgres: spy,
		logger:   silentLogger(),
	}

	cluster := []memNode{
		{ID: "short", Text: "hi", MaxScore: 0.98},
		{ID: "long", Text: "longer text wins here", MaxScore: 0.98},
		{ID: "mid", Text: "medium text", MaxScore: 0.97},
	}

	if err := r.autoMergeCluster(context.Background(), "cube", cluster, "now"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "long" should be kept — all others soft-deleted
	for _, removed := range spy.softDeleted {
		if removed == "long" {
			t.Errorf("longest node %q should not be removed", removed)
		}
	}
	if len(spy.softDeleted) != 2 {
		t.Errorf("expected 2 soft-deletes, got %d: %v", len(spy.softDeleted), spy.softDeleted)
	}
}

func TestConsolidateCluster_AutoMergePathTaken(t *testing.T) {
	spy := &autoMergeSpy{}
	r := &Reorganizer{
		postgres: spy,
		logger:   silentLogger(),
		// no llmClient — would panic if LLM path taken
	}

	cluster := []memNode{
		{ID: "a1", Text: "user prefers dark mode", MaxScore: 0.99},
		{ID: "a2", Text: "user prefers dark mode setting", MaxScore: 0.98},
	}

	// clusterMinScore = 0.98 >= autoMergeThreshold(0.97) → must NOT call LLM
	if err := r.consolidateCluster(context.Background(), "cube", cluster, "now"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spy.softDeleted) == 0 {
		t.Error("expected at least one soft-delete from auto-merge path")
	}
}

func TestConsolidateCluster_LLMPathThresholdCheck(t *testing.T) {
	// Verify routing logic: score below threshold must NOT trigger auto-merge.
	// We check this by asserting clusterMinScore < autoMergeThreshold,
	// which means consolidateCluster would route to LLM (not auto-merge).
	// We don't actually call consolidateCluster here to avoid a nil-client panic.
	cluster := []memNode{
		{ID: "b1", Text: "user likes coffee", MaxScore: 0.90},
		{ID: "b2", Text: "user drinks tea in the morning", MaxScore: 0.86},
	}
	if clusterMinScore(cluster) >= autoMergeThreshold {
		t.Errorf("score %.2f should be below autoMergeThreshold %.2f — would wrongly skip LLM",
			clusterMinScore(cluster), autoMergeThreshold)
	}
}

func TestClusterMinScore(t *testing.T) {
	cluster := []memNode{
		{MaxScore: 0.99},
		{MaxScore: 0.97},
		{MaxScore: 0.98},
	}
	if got := clusterMinScore(cluster); got != 0.97 {
		t.Errorf("clusterMinScore = %v, want 0.97", got)
	}
}

func TestBuildClusters_PropagatesScore(t *testing.T) {
	pairs := []db.DuplicatePair{
		{IDa: "x", MemA: "text x", IDb: "y", MemB: "text y", Score: 0.98},
		{IDa: "y", MemA: "text y", IDb: "z", MemB: "text z", Score: 0.91},
	}
	clusters := buildClusters(pairs)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	for _, n := range clusters[0] {
		if n.MaxScore == 0 {
			t.Errorf("node %q has zero MaxScore", n.ID)
		}
	}
}
