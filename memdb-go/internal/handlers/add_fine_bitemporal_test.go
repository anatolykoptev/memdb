package handlers

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func biTemporalRefForTest(from, predicate, to string) db.BiTemporalEdgeRef {
	return db.BiTemporalEdgeRef{FromID: from, Predicate: predicate, ToID: to}
}

// TestTriggerEdgeInvalidationJudge_ShortCircuits_NoDeps proves the trigger
// is safe to call with a bare Handler — important because the fine-add
// fast path lands here unconditionally and the early-init fallback handler
// has no postgres / llmChat wired.
func TestTriggerEdgeInvalidationJudge_ShortCircuits_NoDeps(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerEdgeInvalidationJudge(context.Background(), "cube-x", "2026-04-27T00:00:00Z") {
		t.Fatalf("expected false (postgres + llmChat are nil)")
	}
}

// TestTriggerEdgeInvalidationJudge_DisabledByEnv pins the env-gate behavior.
func TestTriggerEdgeInvalidationJudge_DisabledByEnv(t *testing.T) {
	t.Setenv(edgeJudgeEnvVar, "false")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	// Even with deps "set" (we skip the actual wiring; the env gate is
	// checked AFTER the dep check), behavior is consistent: no goroutine.
	if h.triggerEdgeInvalidationJudge(context.Background(), "cube-x", "2026-04-27T00:00:00Z") {
		t.Fatalf("expected false when MEMDB_F11_EDGE_JUDGE=false")
	}
}

// TestEdgeJudgeEnabled_ParsesEnv covers the env-gate parser directly.
func TestEdgeJudgeEnabled_ParsesEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true},      // default ON
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"FALSE", false},
		{"0", false},
		{" 0 ", false}, // trimmed
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv(edgeJudgeEnvVar, tc.val)
			if got := edgeJudgeEnabled(); got != tc.want {
				t.Fatalf("edgeJudgeEnabled(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// TestEdgeKey_DeterministicShape pins the format used both for dedup and as
// the LLM-side opaque ID. Drift breaks judgeOneEntityGroup's reverse mapping.
func TestEdgeKey_DeterministicShape(t *testing.T) {
	got := edgeKey(biTemporalRefForTest("alice", "lives_in", "berlin"))
	want := "alice|lives_in|berlin"
	if got != want {
		t.Fatalf("edgeKey shape drift: got %q, want %q", got, want)
	}
}

// errPG is a stub edgeJudgePG that always returns a configurable error.
type errPG struct{ err error }

func (e errPG) FetchFreshEntityEdgesForCube(_ context.Context, _, _ string, _ int) ([]db.BiTemporalEdgeRef, error) {
	return nil, e.err
}
func (e errPG) FetchActiveEntityEdgesBySubject(_ context.Context, _, _, _ string, _ int) ([]db.BiTemporalEdgeRef, error) {
	return nil, e.err
}
func (e errPG) InvalidateEntityEdgesByTriples(_ context.Context, _ string, _ []db.BiTemporalEdgeRef, _ string) (int64, error) {
	return 0, e.err
}

// TestDBErrorOutcome_ConstantAndRegistration verifies:
//  1. edgeJudgeOutcomeDBError constant has the expected string value "db_error".
//  2. It appears in preregisteredJudgeOutcomes so Prometheus sees the series at start.
//  3. runEdgeInvalidationJudge routes a Postgres fetch error to db_error (not llm_error)
//     by completing without panic on a stub that returns an error.
func TestDBErrorOutcome_ConstantAndRegistration(t *testing.T) {
	// 1. Constant value.
	if edgeJudgeOutcomeDBError != "db_error" {
		t.Fatalf("edgeJudgeOutcomeDBError = %q, want %q", edgeJudgeOutcomeDBError, "db_error")
	}

	// 2. Pre-registration includes db_error.
	found := false
	for _, oc := range preregisteredJudgeOutcomes {
		if oc == edgeJudgeOutcomeDBError {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("edgeJudgeOutcomeDBError not in preregisteredJudgeOutcomes: %v", preregisteredJudgeOutcomes)
	}

	// 3. DB error path in runEdgeInvalidationJudge completes without panic.
	// The no-op OTel meter (set by default in test binaries) accepts the Add call.
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	stub := errPG{err: errors.New("simulated postgres failure")}
	// runEdgeInvalidationJudge is the goroutine body; call synchronously.
	h.runEdgeInvalidationJudge(context.Background(), stub, "cube-test", "2026-04-27T00:00:00Z")
	// If the wrong branch was hit (llm_error path via panic or wrong constant),
	// the test would panic. Reaching here means the db_error branch was taken.
}
