package handlers

// add_structural_edges_test.go — unit tests for the M8 Stream 10 structural-edge
// emitter. The Handler-bound orchestrator emitStructuralEdges hits Postgres
// (VectorSearch + bulk insert) so its end-to-end behaviour lives in
// add_structural_edges_livepg_test.go. Here we exercise the pure helpers:
//
//   - buildSameSessionEdges: fan-out + cap accounting
//   - buildTimelineNextEdges: ordering, multi-new-memory chains, dt encoding
//   - buildSimilarCosineEdgesFromResults: half-open score interval, top-K
//   - excludeNewMemoryIDs / encodeTimelineRationale / timelineDeltaSeconds
//
// Every test is hermetic — no DB, no embedder, no logger.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestBuildSameSessionEdges_NoCap(t *testing.T) {
	newMems := []newMemoryRef{{ID: "n1"}, {ID: "n2"}}
	neighbors := []db.SessionMemoryNeighbor{
		{ID: "p1"}, {ID: "p2"}, {ID: "p3"},
	}
	edges, capped := buildSameSessionEdges(newMems, neighbors, 20)
	if capped != 0 {
		t.Fatalf("capped = %d, want 0", capped)
	}
	if len(edges) != 6 {
		t.Fatalf("edges = %d, want 6 (2 new × 3 partners)", len(edges))
	}
	for _, e := range edges {
		if e.Relation != db.EdgeSameSession {
			t.Errorf("relation = %q, want %q", e.Relation, db.EdgeSameSession)
		}
		if e.Confidence != 1.0 {
			t.Errorf("confidence = %v, want 1.0", e.Confidence)
		}
	}
}

func TestBuildSameSessionEdges_CapTrips(t *testing.T) {
	newMems := []newMemoryRef{{ID: "n1"}}
	neighbors := make([]db.SessionMemoryNeighbor, 25)
	for i := range neighbors {
		neighbors[i] = db.SessionMemoryNeighbor{ID: "p" + itoa(i)}
	}
	edges, capped := buildSameSessionEdges(newMems, neighbors, 20)
	if len(edges) != 20 {
		t.Fatalf("edges = %d, want 20 (cap)", len(edges))
	}
	if capped != 5 {
		t.Fatalf("capped = %d, want 5 (25 - 20)", capped)
	}
}

func TestBuildSameSessionEdges_EmptyInputs(t *testing.T) {
	cases := []struct {
		name string
		new  []newMemoryRef
		nb   []db.SessionMemoryNeighbor
	}{
		{"no_new", nil, []db.SessionMemoryNeighbor{{ID: "p1"}}},
		{"no_neighbors", []newMemoryRef{{ID: "n1"}}, nil},
		{"both_empty", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edges, capped := buildSameSessionEdges(c.new, c.nb, 20)
			if len(edges) != 0 || capped != 0 {
				t.Errorf("expected no edges/no cap, got edges=%d capped=%d", len(edges), capped)
			}
		})
	}
}

func TestBuildTimelineNextEdges_ChainsBatch(t *testing.T) {
	// Two existing rows + three new rows, intermixed timestamps.
	neighbors := []db.SessionMemoryNeighbor{
		{ID: "old1", CreatedAt: "2026-04-26T10:00:00.000000"},
		{ID: "old2", CreatedAt: "2026-04-26T10:00:30.000000"},
	}
	newMems := []newMemoryRef{
		{ID: "new1", CreatedAt: "2026-04-26T10:01:00.000000"},
		{ID: "new2", CreatedAt: "2026-04-26T10:01:15.000000"},
		{ID: "new3", CreatedAt: "2026-04-26T10:02:00.000000"},
	}
	edges := buildTimelineNextEdges(newMems, neighbors)

	// Expected chain with at-least-one-new endpoint:
	//   new1 -> old2  (15+15=30s? actually 10:01:00 - 10:00:30 = 30s)
	//   new2 -> new1  (15s)
	//   new3 -> new2  (45s)
	if len(edges) != 3 {
		t.Fatalf("edges = %d, want 3, got=%+v", len(edges), edges)
	}
	want := []struct{ from, to string }{
		{"new1", "old2"},
		{"new2", "new1"},
		{"new3", "new2"},
	}
	for i, w := range want {
		if edges[i].FromID != w.from || edges[i].ToID != w.to {
			t.Errorf("edges[%d] = %s->%s, want %s->%s", i, edges[i].FromID, edges[i].ToID, w.from, w.to)
		}
		if edges[i].Relation != db.EdgeTimelineNext {
			t.Errorf("edges[%d].Relation = %q", i, edges[i].Relation)
		}
		if !strings.Contains(edges[i].Rationale, `"dt_seconds":`) {
			t.Errorf("edges[%d].Rationale missing dt_seconds: %q", i, edges[i].Rationale)
		}
	}
	// Spot-check dt parsing.
	var payload map[string]int64
	if err := json.Unmarshal([]byte(edges[2].Rationale), &payload); err != nil {
		t.Fatalf("rationale not JSON: %v", err)
	}
	if payload["dt_seconds"] != 45 {
		t.Errorf("dt_seconds for new3->new2 = %d, want 45", payload["dt_seconds"])
	}
}

func TestBuildTimelineNextEdges_SkipsExistingPairs(t *testing.T) {
	// Two existing rows, one new — the existing pair must NOT be re-emitted.
	neighbors := []db.SessionMemoryNeighbor{
		{ID: "old1", CreatedAt: "2026-04-26T10:00:00.000000"},
		{ID: "old2", CreatedAt: "2026-04-26T10:00:30.000000"},
	}
	newMems := []newMemoryRef{
		{ID: "new1", CreatedAt: "2026-04-26T10:01:00.000000"},
	}
	edges := buildTimelineNextEdges(newMems, neighbors)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1 (new1->old2 only); got %+v", len(edges), edges)
	}
	if edges[0].FromID != "new1" || edges[0].ToID != "old2" {
		t.Errorf("edge = %s->%s, want new1->old2", edges[0].FromID, edges[0].ToID)
	}
}

func TestBuildTimelineNextEdges_NoNew(t *testing.T) {
	if got := buildTimelineNextEdges(nil, []db.SessionMemoryNeighbor{{ID: "x"}}); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestBuildSimilarCosineEdges_HalfOpenInterval(t *testing.T) {
	results := []db.VectorSearchResult{
		{ID: "self", Score: 0.99},  // skipped (== newID)
		{ID: "exact", Score: 0.95}, // skipped (>= hi)
		{ID: "high", Score: 0.93},  // accepted
		{ID: "mid", Score: 0.88},   // accepted
		{ID: "lowedge", Score: 0.85}, // skipped (<= lo)
		{ID: "low", Score: 0.50},   // skipped
	}
	edges := buildSimilarCosineEdgesFromResults("self", results, 0.85, 0.95, 5)
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2, got %+v", len(edges), edges)
	}
	if edges[0].ToID != "high" || edges[1].ToID != "mid" {
		t.Errorf("edges = %v, want [high, mid]", []string{edges[0].ToID, edges[1].ToID})
	}
	if edges[0].Confidence != 0.93 {
		t.Errorf("confidence = %v, want 0.93", edges[0].Confidence)
	}
	for _, e := range edges {
		if e.Relation != db.EdgeSimilarCosineHigh {
			t.Errorf("relation = %q", e.Relation)
		}
	}
}

func TestBuildSimilarCosineEdges_TopKCap(t *testing.T) {
	results := []db.VectorSearchResult{
		{ID: "a", Score: 0.94},
		{ID: "b", Score: 0.93},
		{ID: "c", Score: 0.92},
		{ID: "d", Score: 0.91},
		{ID: "e", Score: 0.90},
		{ID: "f", Score: 0.89}, // would be the 6th, must be dropped
	}
	edges := buildSimilarCosineEdgesFromResults("self", results, 0.85, 0.95, 5)
	if len(edges) != 5 {
		t.Fatalf("edges = %d, want 5 (cap)", len(edges))
	}
}

func TestBuildSimilarCosineEdges_EmptyInputs(t *testing.T) {
	if r := buildSimilarCosineEdgesFromResults("", []db.VectorSearchResult{{ID: "x", Score: 0.9}}, 0.85, 0.95, 5); r != nil {
		t.Errorf("expected nil for empty newID, got %+v", r)
	}
	if r := buildSimilarCosineEdgesFromResults("n", nil, 0.85, 0.95, 5); r != nil {
		t.Errorf("expected nil for empty results, got %+v", r)
	}
	if r := buildSimilarCosineEdgesFromResults("n", []db.VectorSearchResult{{ID: "x", Score: 0.9}}, 0.85, 0.95, 0); r != nil {
		t.Errorf("expected nil for maxPartners=0, got %+v", r)
	}
}

func TestExcludeNewMemoryIDs(t *testing.T) {
	neighbors := []db.SessionMemoryNeighbor{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	out := excludeNewMemoryIDs(neighbors, []newMemoryRef{{ID: "b"}})
	if len(out) != 2 {
		t.Fatalf("out = %d, want 2", len(out))
	}
	for _, n := range out {
		if n.ID == "b" {
			t.Errorf("expected id 'b' filtered, but present in %+v", out)
		}
	}
}

func TestTimelineDeltaSeconds(t *testing.T) {
	cases := []struct {
		name   string
		prev   string
		cur    string
		want   int64
	}{
		{"microsec_format", "2026-04-26T10:00:00.000000", "2026-04-26T10:00:42.000000", 42},
		{"second_format", "2026-04-26T10:00:00", "2026-04-26T10:01:30", 90},
		{"reverse_order_abs", "2026-04-26T10:01:00.000000", "2026-04-26T10:00:00.000000", 60},
		{"unparsable", "garbage", "also-garbage", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := timelineDeltaSeconds(c.prev, c.cur); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestEncodeTimelineRationale(t *testing.T) {
	r := encodeTimelineRationale(42)
	var p map[string]int64
	if err := json.Unmarshal([]byte(r), &p); err != nil {
		t.Fatalf("rationale invalid JSON: %v", err)
	}
	if p["dt_seconds"] != 42 {
		t.Errorf("dt_seconds = %d, want 42", p["dt_seconds"])
	}
}

func TestFastBatchLTMRefs_LayoutMatchesPairs(t *testing.T) {
	nodes := []db.MemoryInsertNode{
		{ID: "wm0"}, {ID: "ltm0"},
		{ID: "wm1"}, {ID: "ltm1"},
		{ID: "wm2"}, {ID: "ltm2"},
	}
	embs := [][]float32{{0.1}, {0.2}, {0.3}}
	refs := fastBatchLTMRefs(nodes, embs, "ts")
	if len(refs) != 3 {
		t.Fatalf("refs = %d, want 3", len(refs))
	}
	wantIDs := []string{"ltm0", "ltm1", "ltm2"}
	for i, r := range refs {
		if r.ID != wantIDs[i] {
			t.Errorf("refs[%d].ID = %q, want %q", i, r.ID, wantIDs[i])
		}
		if len(r.Embedding) == 0 {
			t.Errorf("refs[%d] has no embedding", i)
		}
		if r.CreatedAt != "ts" {
			t.Errorf("refs[%d].CreatedAt = %q", i, r.CreatedAt)
		}
	}
}

func TestRawBatchRefs(t *testing.T) {
	nodes := []db.MemoryInsertNode{{ID: "a"}, {ID: ""}, {ID: "c"}}
	embs := [][]float32{{0.1}, {0.2}, {0.3}}
	refs := rawBatchRefs(nodes, embs, "ts")
	if len(refs) != 2 {
		t.Fatalf("refs = %d, want 2 (skip empty ID)", len(refs))
	}
	if refs[0].ID != "a" || refs[1].ID != "c" {
		t.Errorf("refs IDs = [%q,%q]", refs[0].ID, refs[1].ID)
	}
}

// TestEmitStructuralEdges_CapFiredPath exercises the cap-fired branch of the
// structural-edge orchestrator at the pure-helper level — no DB required.
// Simulates adding memory-26 into a session that already has 25 neighbors:
//   - buildSameSessionEdges must cap at sameSessionMaxPartners (20), not 25.
//   - buildTimelineNextEdges must link to the most-recent neighbor (last in
//     DESC-returned slice = index 0) rather than the oldest one.
//
// This mirrors the live assertion in TestLivePG_StructuralEdges_CapFires but
// runs entirely hermetically, confirming the cap and ordering logic in the
// pure helpers without touching Postgres.
func TestEmitStructuralEdges_CapFiredPath(t *testing.T) {
	const numExisting = 25

	// Build a pool of 25 existing neighbors with monotonically-increasing
	// timestamps. GetSessionMemoryNeighborsRecent returns DESC, so the
	// most-recent neighbor is first in the slice.
	neighbors := make([]db.SessionMemoryNeighbor, numExisting)
	for i := 0; i < numExisting; i++ {
		neighbors[i] = db.SessionMemoryNeighbor{
			ID:        fmt.Sprintf("existing-%02d", i+1),
			CreatedAt: fmt.Sprintf("2026-04-26T10:%02d:00.000000", i), // 10:00 .. 10:24
		}
	}
	// DESC ordering: most-recent is neighbors[0] (existing-25 = 10:24),
	// oldest is neighbors[24] (existing-01 = 10:00).
	// Reverse to simulate DESC: highest index = earliest minute.
	// Re-order: index 0 = most recent (minute 24), index 24 = oldest (minute 0).
	for i, j := 0, len(neighbors)-1; i < j; i, j = i+1, j-1 {
		neighbors[i], neighbors[j] = neighbors[j], neighbors[i]
	}
	// Now neighbors[0] = existing-25 (10:24), neighbors[24] = existing-01 (10:00).

	newMem := newMemoryRef{
		ID:        "new-26",
		CreatedAt: "2026-04-26T10:25:00.000000",
	}
	newMems := []newMemoryRef{newMem}

	// --- SAME_SESSION cap ---
	edges, capped := buildSameSessionEdges(newMems, neighbors, sameSessionMaxPartners)
	if len(edges) != sameSessionMaxPartners {
		t.Errorf("SAME_SESSION edges = %d, want %d (cap)", len(edges), sameSessionMaxPartners)
	}
	expectedCapped := (numExisting - sameSessionMaxPartners) * len(newMems)
	if capped != expectedCapped {
		t.Errorf("capped = %d, want %d", capped, expectedCapped)
	}

	// --- TIMELINE_NEXT most-recent predecessor ---
	// buildTimelineNextEdges merges newMems + neighbors and sorts ASC internally.
	// After the sort, new-26 (10:25) is last; its predecessor is existing-25 (10:24).
	tlEdges := buildTimelineNextEdges(newMems, neighbors)
	// Find the edge whose from is new-26.
	var linkToID string
	for _, e := range tlEdges {
		if e.FromID == newMem.ID {
			linkToID = e.ToID
			break
		}
	}
	if linkToID == "" {
		t.Fatalf("no TIMELINE_NEXT edge from new-26; got edges=%+v", tlEdges)
	}
	// Most-recent existing neighbor has CreatedAt 10:24 → ID existing-25.
	wantPredecessor := neighbors[0].ID // existing-25 (most recent after DESC inversion)
	if linkToID != wantPredecessor {
		t.Errorf("TIMELINE_NEXT new-26 → %s, want %s (most-recent predecessor)", linkToID, wantPredecessor)
		// Diagnose if it linked to the oldest row instead.
		oldestID := neighbors[len(neighbors)-1].ID
		if linkToID == oldestID {
			t.Errorf("  REGRESSION: linked to oldest neighbor %s — ORDER BY ASC bug in query", oldestID)
		}
	}
}

// itoa is a tiny digits helper so the cap test doesn't pull in strconv per-line.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
