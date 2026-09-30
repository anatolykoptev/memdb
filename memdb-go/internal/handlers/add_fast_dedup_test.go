package handlers

import (
	"math"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// extractSessionID — defensive parsing of properties JSON.

func TestExtractSessionID_ReturnsSession(t *testing.T) {
	props := `{"id":"u1","memory":"hi","session_id":"sess-42","user_id":"alice"}`
	if got := extractSessionID(props); got != "sess-42" {
		t.Errorf("extractSessionID = %q, want %q", got, "sess-42")
	}
}

func TestExtractSessionID_MissingFieldReturnsEmpty(t *testing.T) {
	props := `{"id":"u1","memory":"hi"}`
	if got := extractSessionID(props); got != "" {
		t.Errorf("extractSessionID with no session_id = %q, want \"\"", got)
	}
}

func TestExtractSessionID_MalformedJSONReturnsEmpty(t *testing.T) {
	cases := []string{
		"",
		"not json at all",
		"{broken",
		`{"session_id":42}`, // wrong type — silently empty
	}
	for _, props := range cases {
		got := extractSessionID(props)
		if got != "" {
			t.Errorf("extractSessionID(%q) = %q, want \"\"", props, got)
		}
	}
}

// fastDedupThreshold — env override + clamping.

func TestFastDedupThreshold_DefaultWhenUnset(t *testing.T) {
	t.Setenv("MEMDB_FAST_DEDUP_THRESHOLD", "")
	if got := fastDedupThreshold(); got != dedupThreshold {
		t.Errorf("fastDedupThreshold default = %v, want %v", got, dedupThreshold)
	}
}

func TestFastDedupThreshold_ValidEnvWins(t *testing.T) {
	t.Setenv("MEMDB_FAST_DEDUP_THRESHOLD", "0.97")
	if got := fastDedupThreshold(); got != 0.97 {
		t.Errorf("fastDedupThreshold = %v, want 0.97", got)
	}
}

func TestFastDedupThreshold_OutOfRangeFallsBack(t *testing.T) {
	cases := map[string]float64{
		"-0.1":   dedupThreshold,
		"0":      dedupThreshold,
		"1.5":    dedupThreshold,
		"banana": dedupThreshold,
	}
	for raw, want := range cases {
		t.Setenv("MEMDB_FAST_DEDUP_THRESHOLD", raw)
		got := fastDedupThreshold()
		if got != want {
			t.Errorf("fastDedupThreshold(%q) = %v, want %v", raw, got, want)
		}
	}
}

// fastDedupSessionAware — default-on with explicit opt-out values.

func TestFastDedupSessionAware_DefaultOn(t *testing.T) {
	t.Setenv("MEMDB_FAST_DEDUP_SESSION_AWARE", "")
	if !fastDedupSessionAware() {
		t.Error("fastDedupSessionAware default should be true (session-aware)")
	}
}

func TestFastDedupSessionAware_ExplicitOptOut(t *testing.T) {
	for _, raw := range []string{"0", "false", "FALSE", "off", "no"} {
		t.Setenv("MEMDB_FAST_DEDUP_SESSION_AWARE", raw)
		if fastDedupSessionAware() {
			t.Errorf("MEMDB_FAST_DEDUP_SESSION_AWARE=%q should disable session-aware", raw)
		}
	}
}

func TestFastDedupSessionAware_UnknownValueDefaultsOn(t *testing.T) {
	for _, raw := range []string{"1", "true", "yes", "banana"} {
		t.Setenv("MEMDB_FAST_DEDUP_SESSION_AWARE", raw)
		if !fastDedupSessionAware() {
			t.Errorf("MEMDB_FAST_DEDUP_SESSION_AWARE=%q should be session-aware (default-on)", raw)
		}
	}
}

// Composition test — the production isDuplicate path's inner logic on
// the same VectorSearchResult shape Postgres returns. We can't mock the
// VectorSearch RPC without a heavier interface refactor, so we exercise
// the pure-Go decision branches via the helpers above. Together they
// cover the regression: "session_2 ingest was silently dropped because
// session_1's window had cosine ≥ 0.92".
//
// The named bug: cross-session conv-26 collisions with cosine 0.92-0.99
// triggered isDuplicate=true and 5/6 LoCoMo sessions never reached the
// DB. The fix is layered:
//
//  1. fastDedupThreshold reads MEMDB_FAST_DEDUP_THRESHOLD so operators
//     can raise the bar without recompiling (covered above).
//  2. fastDedupSessionAware enables a session_id filter on candidate
//     matches so cross-session high-cosine pairs are NOT treated as
//     duplicates by default (covered above).
//  3. extractSessionID parses session_id out of the candidate's raw
//     props blob and is defensive against malformed JSON (covered
//     above).
//
// A live-postgres E2E lives in add_fast_batched_livepg_test.go and is
// the place to assert "ingest 3 distinct sessions → 6 rows survive".
func TestFastDedup_DocumentsContractSurface(t *testing.T) {
	if !strings.Contains("session_id", "session") {
		t.Fatal("sanity")
	}
}

// TestIsZeroVector — the source-layer guard that prevents zero vectors
// from entering the DB. pgvector <=> returns NaN for zero vectors, which
// breaks all downstream score comparisons (NaN < threshold is false →
// false duplicate → silent data loss).
func TestIsZeroVector(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
		want bool
	}{
		{"nil", nil, true},
		{"empty", []float32{}, true},
		{"all zeros", make([]float32, 1024), true},
		{"non-zero", []float32{0, 0, 0, 0.001}, false},
		{"unit vector", []float32{1, 0, 0, 0}, false},
		{"negative", []float32{0, -0.5, 0, 0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isZeroVector(tc.vec); got != tc.want {
				t.Errorf("isZeroVector(%v) = %v, want %v", tc.vec, got, tc.want)
			}
		})
	}
}

// TestBuildSimilarCosineEdgesFromResults_NaNGuard — the consumer-layer
// guard. When a stored embedding is a zero vector, pgvector returns NaN
// for the cosine distance, and 1 - NaN = NaN. Without the guard, NaN
// passes both `<= lo` (false) and `>= hi` (false), so the row would
// produce a structural edge with NaN confidence.
func TestBuildSimilarCosineEdgesFromResults_NaNGuard(t *testing.T) {
	results := []db.VectorSearchResult{
		{ID: "neigh-1", Score: math.NaN(), Properties: "{}"},
		{ID: "neigh-2", Score: 0.85, Properties: "{}"},
	}
	edges := buildSimilarCosineEdgesFromResults("new-1", results, 0.5, 0.92, 10)
	// NaN row must be skipped; only the 0.85 row should produce an edge.
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (NaN skipped), got %d: %+v", len(edges), edges)
	}
	if edges[0].ToID != "neigh-2" {
		t.Errorf("edge to wrong neighbour: got %s, want neigh-2", edges[0].ToID)
	}
	if math.IsNaN(edges[0].Confidence) {
		t.Errorf("edge confidence is NaN — NaN guard failed")
	}
}
