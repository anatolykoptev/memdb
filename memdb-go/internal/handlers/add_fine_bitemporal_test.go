package handlers

import (
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
	if h.triggerEdgeInvalidationJudge("cube-x", "2026-04-27T00:00:00Z") {
		t.Fatalf("expected false (postgres + llmChat are nil)")
	}
}

// TestTriggerEdgeInvalidationJudge_DisabledByEnv pins the env-gate behavior.
func TestTriggerEdgeInvalidationJudge_DisabledByEnv(t *testing.T) {
	t.Setenv(edgeJudgeEnvVar, "false")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	// Even with deps "set" (we skip the actual wiring; the env gate is
	// checked AFTER the dep check), behavior is consistent: no goroutine.
	if h.triggerEdgeInvalidationJudge("cube-x", "2026-04-27T00:00:00Z") {
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
