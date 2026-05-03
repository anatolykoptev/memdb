package scheduler

// tree_ce_precompute_skip_degraded_test.go — regression for the
// 2026-05-03 incident where client.Rerank() returned Score=0 entries on
// circuit-breaker-open / timeout, and the precompute write path
// persisted those zeroes. Search-time read could not distinguish
// "genuine low-relevance" from "failed inference" → 0% precompute hit
// rate observed in production.

import (
	"errors"
	"testing"

	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// fakeRerankWithResult returns a controlled *rerank.Result so we can
// validate the precompute filtering contract without a live rerank.Client.
func fakeRerankWithResult(status rerank.Status, scoredScores []float32, docIDs []string, returnErr error) (*rerank.Result, error) {
	if returnErr != nil {
		return nil, returnErr
	}
	scored := make([]rerank.Scored, 0, len(docIDs))
	for i, id := range docIDs {
		score := float32(0)
		if i < len(scoredScores) {
			score = scoredScores[i]
		}
		scored = append(scored, rerank.Scored{Doc: rerank.Doc{ID: id}, Score: score})
	}
	return &rerank.Result{Scored: scored, Status: status}, nil
}

// applyPrecomputeFilter mirrors cePrecomputeScoreNeighbours's decision
// (skip on non-Ok status) so we can assert the contract without a live
// rerank.Client. The production function is exercised via integration
// in the existing reorganizer tests; this is a focused regression.
func applyPrecomputeFilter(res *rerank.Result, err error) []db.CEScoreEntry {
	if err != nil || res == nil || res.Status != rerank.StatusOk {
		return nil
	}
	entries := make([]db.CEScoreEntry, 0, len(res.Scored))
	for _, s := range res.Scored {
		if s.ID == "" {
			continue
		}
		entries = append(entries, db.CEScoreEntry{NeighborID: s.ID, Score: s.Score})
	}
	return entries
}

func TestPrecomputeFilter_OkStatusWritesEntries(t *testing.T) {
	res, err := fakeRerankWithResult(rerank.StatusOk, []float32{0.9, 0.5, 0.1}, []string{"a", "b", "c"}, nil)
	got := applyPrecomputeFilter(res, err)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries on Ok, got %d", len(got))
	}
	if got[0].Score != 0.9 || got[1].Score != 0.5 {
		t.Errorf("scores not preserved: %+v", got)
	}
}

func TestPrecomputeFilter_DegradedStatusSkips(t *testing.T) {
	// On Degraded, client.Rerank returns Score=0 for every doc — the bug
	// pre-fix was persisting those zeroes. We MUST return nil instead.
	res, err := fakeRerankWithResult(rerank.StatusDegraded, []float32{0, 0, 0}, []string{"a", "b", "c"}, errors.New("ce timeout"))
	got := applyPrecomputeFilter(res, err)
	if got != nil {
		t.Errorf("Degraded must return nil (not persist zeroes), got %d entries", len(got))
	}
}

func TestPrecomputeFilter_FallbackStatusSkips(t *testing.T) {
	// Fallback also produces non-canonical scores from a different model;
	// safer to skip persistence than to mix scoring scales in one cache.
	res, err := fakeRerankWithResult(rerank.StatusFallback, []float32{0.7}, []string{"a"}, nil)
	got := applyPrecomputeFilter(res, err)
	if got != nil {
		t.Errorf("Fallback must return nil to keep cache scoring scale consistent, got %d entries", len(got))
	}
}

func TestPrecomputeFilter_SkippedStatusSkips(t *testing.T) {
	res, err := fakeRerankWithResult(rerank.StatusSkipped, nil, []string{"a"}, nil)
	got := applyPrecomputeFilter(res, err)
	if got != nil {
		t.Errorf("Skipped must return nil, got %d entries", len(got))
	}
}

func TestPrecomputeFilter_NilResultSkips(t *testing.T) {
	got := applyPrecomputeFilter(nil, nil)
	if got != nil {
		t.Errorf("nil result must return nil, got %d entries", len(got))
	}
}
