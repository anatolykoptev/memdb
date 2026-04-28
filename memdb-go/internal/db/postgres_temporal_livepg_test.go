//go:build livepg

package db_test

// postgres_temporal_livepg_test.go — F7 followup: SearchMemoriesByDateRange
// benchmark + smoke test against a live Postgres + memos_graph schema.
//
// Skipped by default (the file gates on the livepg tag). Run with:
//   MEMDB_TEST_POSTGRES_URL=<dsn> go test -tags=livepg -bench=. -run=^$ \
//       ./memdb-go/internal/db/...
//
// The benchmark exists primarily to surface the per-row jsonb_array_elements_text
// scan cost called out in the postgres_temporal.go LIMITATION comment — i.e.
// to detect regressions when the GIN partial index from migration 0024 is
// dropped or the predicate stops being sargable. It does NOT seed data; it
// runs against whatever event_dates rows the target tenant has.

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// setupTemporalBench is the *testing.B equivalent of setupCubesTest. Kept
// local to this file since BenchmarkSearchMemoriesByDateRange is the only
// benchmark in the package today.
func setupTemporalBench(b *testing.B) (*db.Postgres, func()) {
	b.Helper()
	url := os.Getenv("MEMDB_TEST_POSTGRES_URL")
	if url == "" {
		b.Skip("MEMDB_TEST_POSTGRES_URL not set; skipping postgres benchmark")
	}
	ctx := context.Background()
	pg, err := db.NewPostgres(ctx, url, slog.Default())
	if err != nil {
		b.Fatalf("connect postgres: %v", err)
	}
	cleanup := func() { pg.Close() }
	return pg, cleanup
}

// BenchmarkSearchMemoriesByDateRange measures the wall-clock cost of one
// date-range scan for the smoke tenant. The benchmark is read-only — no
// rows are inserted. It is bounded to b.N iterations of a fixed query.
func BenchmarkSearchMemoriesByDateRange(b *testing.B) {
	pg, cleanup := setupTemporalBench(b)
	defer cleanup()
	ctx := context.Background()

	// Use a wide range so any tenant with event_dates returns rows.
	const userName = "bench-temporal"
	const start = "2000-01-01"
	const end = "2099-12-31"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := pg.SearchMemoriesByDateRange(ctx, userName, start, end, 100); err != nil {
			b.Fatalf("SearchMemoriesByDateRange: %v", err)
		}
	}
}

// TestSearchMemoriesByDateRange_Smoke is a livepg sanity check that the SQL
// compiles + binds against a real planner. Returns 0 rows (or any rows) —
// the assertion is no error.
func TestSearchMemoriesByDateRange_Smoke(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := pg.SearchMemoriesByDateRange(ctx, "smoke-no-such-user", "2024-01-01", "2024-12-31", 10); err != nil {
		t.Fatalf("SearchMemoriesByDateRange: %v", err)
	}
}
