//go:build livepg

package search_test

// ce_precompute_livepg_test.go — end-to-end smoke for the M10 Stream 6
// CE precompute persistence path. Verifies that the SQL written by
// db.SetCEScoresTopK round-trips through the agtype/jsonb layer
// untouched and that ClearCEScoresTopKForNeighbor cascade-deletes any
// row that listed the affected memory.
//
// Live D3 reorganizer wiring (rerank.Client + InsertMemoryNodes +
// RunTreeReorgForCube) is exercised by the scheduler livepg tests; this
// file scopes itself to the persistence contract that search-time
// lookup depends on.
//
// Requires a live PostgreSQL with MemDB migrations applied. Run:
//   MEMDB_TEST_POSTGRES_URL=<dsn> go test -tags=livepg ./memdb-go/internal/search/...

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func livepgConn(t *testing.T) *db.Postgres {
	t.Helper()
	url := os.Getenv("MEMDB_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("MEMDB_TEST_POSTGRES_URL not set; skipping livepg test")
	}
	pg, err := db.NewPostgres(context.Background(), url, slog.Default())
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() { pg.Close() })
	return pg
}

// insertRawMemory writes a single raw-tier Memory row directly via
// pgx.Pool, sidestepping the higher-level helpers so the test owns its
// fixture lifecycle without dragging in the scheduler import.
func insertRawMemory(t *testing.T, pg *db.Postgres, cubeID, memID, text string) {
	t.Helper()
	props := map[string]any{
		"id":              memID,
		"user_name":       cubeID,
		"memory":          text,
		"memory_type":     "LongTermMemory",
		"hierarchy_level": "raw",
		"status":          "activated",
		"created_at":      "2026-04-25T00:00:00Z",
		"updated_at":      "2026-04-25T00:00:00Z",
	}
	body, _ := json.Marshal(props)
	emb := "[" + dummyVec(1024) + "]"
	_, err := pg.Pool().Exec(context.Background(),
		`INSERT INTO memos_graph."Memory"(properties, embedding)
		 VALUES ($1::text::agtype, $2::halfvec(1024))`,
		string(body), emb,
	)
	if err != nil {
		t.Fatalf("insert raw memory %s: %v", memID, err)
	}
}

// dummyVec returns a comma-separated list of n zeros.
func dummyVec(n int) string {
	out := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '0')
	}
	return string(out)
}

func cleanupCube(t *testing.T, pg *db.Postgres, cubeID string) {
	t.Helper()
	_, _ = pg.Pool().Exec(context.Background(),
		`DELETE FROM memos_graph."Memory"
		 WHERE properties->>'user_name' = $1`, cubeID)
}

func TestCEPrecompute_PersistAndCascadeClear(t *testing.T) {
	pg := livepgConn(t)
	cubeID := "test-ce-precompute-" + t.Name()
	t.Cleanup(func() { cleanupCube(t, pg, cubeID) })
	cleanupCube(t, pg, cubeID)

	ctx := context.Background()

	insertRawMemory(t, pg, cubeID, "mem-a", "alpha text")
	insertRawMemory(t, pg, cubeID, "mem-b", "bravo text")
	insertRawMemory(t, pg, cubeID, "mem-c", "charlie text")

	entriesA := []db.CEScoreEntry{
		{NeighborID: "mem-c", Score: 0.91},
		{NeighborID: "mem-b", Score: 0.42},
	}
	if err := pg.SetCEScoresTopK(ctx, "mem-a", cubeID, entriesA); err != nil {
		t.Fatalf("set ce_score_topk on mem-a: %v", err)
	}
	entriesB := []db.CEScoreEntry{
		{NeighborID: "mem-c", Score: 0.88},
	}
	if err := pg.SetCEScoresTopK(ctx, "mem-b", cubeID, entriesB); err != nil {
		t.Fatalf("set ce_score_topk on mem-b: %v", err)
	}

	got := readCEScores(t, pg, "mem-a")
	if len(got) != 2 {
		t.Fatalf("mem-a: expected 2 entries, got %d", len(got))
	}
	if got[0].NeighborID != "mem-c" || got[0].Score < 0.90 || got[0].Score > 0.92 {
		t.Errorf("mem-a entry 0: expected mem-c≈0.91, got %+v", got[0])
	}

	if err := pg.ClearCEScoresTopKForNeighbor(ctx, "mem-c"); err != nil {
		t.Fatalf("cascade clear: %v", err)
	}
	if g := readCEScores(t, pg, "mem-a"); g != nil {
		t.Errorf("mem-a should have ce_score_topk cleared after cascade, got %+v", g)
	}
	if g := readCEScores(t, pg, "mem-b"); g != nil {
		t.Errorf("mem-b should have ce_score_topk cleared after cascade, got %+v", g)
	}
}

func TestCEPrecompute_ClearByID(t *testing.T) {
	pg := livepgConn(t)
	cubeID := "test-ce-precompute-clear-" + t.Name()
	t.Cleanup(func() { cleanupCube(t, pg, cubeID) })
	cleanupCube(t, pg, cubeID)

	ctx := context.Background()
	insertRawMemory(t, pg, cubeID, "mem-x", "xray text")

	if err := pg.SetCEScoresTopK(ctx, "mem-x", cubeID, []db.CEScoreEntry{
		{NeighborID: "mem-y", Score: 0.7},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := pg.ClearCEScoresTopK(ctx, "mem-x"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if g := readCEScores(t, pg, "mem-x"); g != nil {
		t.Errorf("expected ce_score_topk cleared, got %+v", g)
	}
}

// readCEScores reads the ce_score_topk array for a memory, returning nil
// when the key is absent.
func readCEScores(t *testing.T, pg *db.Postgres, memID string) []db.CEScoreEntry {
	t.Helper()
	var raw *string
	err := pg.Pool().QueryRow(context.Background(),
		`SELECT (properties::text::jsonb -> 'ce_score_topk')::text
		 FROM memos_graph."Memory"
		 WHERE properties->>'id' = $1
		 LIMIT 1`, memID).Scan(&raw)
	if err != nil {
		t.Fatalf("read ce_score_topk: %v", err)
	}
	if raw == nil {
		return nil
	}
	var entries []db.CEScoreEntry
	if err := json.Unmarshal([]byte(*raw), &entries); err != nil {
		t.Fatalf("unmarshal ce_score_topk: %v", err)
	}
	return entries
}
