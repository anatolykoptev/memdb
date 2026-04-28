// Package search — F12 stageLinkedExpand unit tests + microbenchmark.
//
// Coverage:
//   - disabled env-gate path returns input unchanged
//   - empty seeds short-circuit
//   - DB error soft-fails
//   - happy path: seed-preserving cap, expansion sorted by cosine, self-link
//     dropped, decay applied
//   - microbench measures the per-call wall time so the +50ms budget claim
//     in the PR body is auditable
package search

import (
	"context"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// mockLinkedPG embeds mockPostgres and overrides GetMemoriesByLinkedIDs.
type mockLinkedPG struct {
	mockPostgres
	rows    []db.VectorSearchResult
	err     error
	gotIDs  []string
	gotCube string
	calls   int
}

func (m *mockLinkedPG) GetMemoriesByLinkedIDs(_ context.Context, linkIDs []string, cubeID, _, _ string, _ int) ([]db.VectorSearchResult, error) {
	m.calls++
	m.gotIDs = append([]string(nil), linkIDs...)
	m.gotCube = cubeID
	return m.rows, m.err
}

func mkSeedsLinked(n int) []MergedResult {
	out := make([]MergedResult, n)
	for i := 0; i < n; i++ {
		out[i] = MergedResult{ID: rune2id(i), Score: 1.0 - float64(i)*0.01}
	}
	return out
}

func TestLinkedExpandEnabled_Default(t *testing.T) {
	t.Setenv("MEMDB_F12_LINKED", "")
	if !linkedExpandEnabled() {
		t.Fatal("default should be ON when env is empty")
	}
	t.Setenv("MEMDB_F12_LINKED", "false")
	if linkedExpandEnabled() {
		t.Fatal("should be OFF when env=false")
	}
	t.Setenv("MEMDB_F12_LINKED", "1")
	if !linkedExpandEnabled() {
		t.Fatal("should be ON when env=1")
	}
}

func TestLinkedExpand_EmptySeeds(t *testing.T) {
	pg := &mockLinkedPG{}
	got := expandViaLinkedIDs(context.Background(), pg, discardTestLogger(), nil, nil, "cube", "user", "")
	if got != nil {
		t.Fatalf("expected nil pass-through, got %v", got)
	}
	if pg.calls != 0 {
		t.Fatalf("DB should not be called on empty seeds")
	}
}

func TestLinkedExpand_DBError_SoftFail(t *testing.T) {
	pg := &mockLinkedPG{err: errSentinel}
	seeds := mkSeedsLinked(3)
	got := expandViaLinkedIDs(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")
	if len(got) != 3 {
		t.Fatalf("soft-fail violated: pool=%d want 3", len(got))
	}
}

func TestLinkedExpand_HappyPath_SeedsPreserved(t *testing.T) {
	pg := &mockLinkedPG{
		rows: []db.VectorSearchResult{
			{ID: "neighbor-A", Properties: `{"id":"neighbor-A"}`, Embedding: []float32{1, 0, 0}},
			{ID: "neighbor-B", Properties: `{"id":"neighbor-B"}`, Embedding: []float32{0, 1, 0}},
			{ID: rune2id(0), Properties: `{"id":"seed-0"}`}, // dup of a seed — must be dropped
		},
	}
	seeds := mkSeedsLinked(4)
	queryVec := []float32{1, 0, 0}
	got := expandViaLinkedIDs(context.Background(), pg, discardTestLogger(), seeds, queryVec, "cube", "user", "")
	// 4 seeds + 2 unique neighbours, capped at ceil(4*1.5)=6 → all fit
	if len(got) != 6 {
		t.Fatalf("pool size: got=%d want 6 (4 seeds + 2 neighbours)", len(got))
	}
	// First 4 entries must be the original seeds, in order.
	for i := 0; i < 4; i++ {
		if got[i].ID != seeds[i].ID {
			t.Fatalf("seed %d displaced: got %s want %s", i, got[i].ID, seeds[i].ID)
		}
	}
	// neighbor-A scores higher (cos against query=1.0 → 1.0 * 0.85)
	if got[4].ID != "neighbor-A" {
		t.Fatalf("expected neighbor-A first by cosine, got %s", got[4].ID)
	}
}

func TestLinkedExpand_CapTrimsExpansionsOnly(t *testing.T) {
	rows := make([]db.VectorSearchResult, 20)
	for i := range rows {
		rows[i] = db.VectorSearchResult{
			ID:         rune2id(100 + i),
			Properties: `{}`,
			Embedding:  []float32{float32(i), 0, 0},
		}
	}
	pg := &mockLinkedPG{rows: rows}
	seeds := mkSeedsLinked(4)
	got := expandViaLinkedIDs(context.Background(), pg, discardTestLogger(), seeds, []float32{1, 0, 0}, "cube", "user", "")
	// cap = ceil(4 * 1.5) = 6 — 4 seeds + 2 expansions
	if len(got) != 6 {
		t.Fatalf("cap mis-applied: got=%d want=6", len(got))
	}
}

// BenchmarkLinkedExpand measures per-call wall time. Used to validate the
// "+50ms p95" budget claim — DB call is mocked, so this is the in-process
// overhead floor (sort + cosine + alloc). Real stage_duration_ms is bounded
// by the GIN-indexed Postgres query, measured in production.
func BenchmarkLinkedExpand(b *testing.B) {
	rows := make([]db.VectorSearchResult, 50)
	for i := range rows {
		rows[i] = db.VectorSearchResult{
			ID:         rune2id(100 + i),
			Properties: `{}`,
			Embedding:  randVec(1024),
		}
	}
	pg := &mockLinkedPG{rows: rows}
	seeds := mkSeedsLinked(20)
	for i := range seeds {
		seeds[i].Embedding = randVec(1024)
	}
	queryVec := randVec(1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = expandViaLinkedIDs(context.Background(), pg, nil, seeds, queryVec, "cube", "user", "")
	}
}

func randVec(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(i%17) * 0.01
	}
	return out
}

var errSentinel = &mockErr{msg: "boom"}

type mockErr struct{ msg string }

func (e *mockErr) Error() string { return e.msg }
