//go:build livepg

// Package search — multihop_livepg_test.go: end-to-end test for the M8 D2
// fix against a live Postgres + AGE 1.7 + pgvector. Verifies that
// expandViaGraph walks memory_edges and pulls in a 2-hop neighbour with a
// score that beats a cold seed when the neighbour's embedding aligns with
// the query — the exact regression the M7 cat-2 0.091 measurement
// surfaced.
//
// Gating:
//   - build tag `livepg` keeps this file out of the default test binary.
//   - MEMDB_LIVE_PG_DSN must be set or the test t.Skip's. Format:
//     postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./memdb-go/internal/search/... \
//	    -run TestLivePG_ExpandViaGraph_TwoHop -count=1 -v

package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestLivePG_ExpandViaGraph_TwoHop(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	t.Setenv("MEMDB_D2_MAX_HOP", "2")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	dsn := os.Getenv("MEMDB_LIVE_PG_DSN")
	if dsn == "" {
		t.Skip("MEMDB_LIVE_PG_DSN not set; skipping live-Postgres D2 test")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pg, err := db.NewPostgres(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}

	cubeID := "test-d2-multihop-" + uuid.New().String()
	t.Logf("livepg D2 test cube_id=%s", cubeID)

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			cleanupD2Cube(cleanupCtx, t, pg, cubeID)
			pg.Close()
		})
	}
	t.Cleanup(cleanup)
	defer cleanup()

	// Fixture: 3 nodes, 3 orthogonal embeddings. Edges A → B → C form a
	// 2-hop chain. Query embedding aligns with C (the 2-hop target).
	// pgvector column is fixed at 1024 dims (multilingual-e5-large).
	dim := 1024
	embA := unitVecD2(0, dim) // axis-0
	embB := unitVecD2(1, dim) // axis-1
	embC := unitVecD2(2, dim) // axis-2
	queryVec := unitVecD2(2, dim)

	idA := uuid.New().String()
	idB := uuid.New().String()
	idC := uuid.New().String()
	if err := insertD2Node(ctx, pg, cubeID, idA, "A: hub", embA); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	if err := insertD2Node(ctx, pg, cubeID, idB, "B: middle", embB); err != nil {
		t.Fatalf("insert B: %v", err)
	}
	if err := insertD2Node(ctx, pg, cubeID, idC, "C: target", embC); err != nil {
		t.Fatalf("insert C: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := pg.CreateMemoryEdge(ctx, idA, idB, db.EdgeRelated, now, now); err != nil {
		t.Fatalf("edge A→B: %v", err)
	}
	if err := pg.CreateMemoryEdge(ctx, idB, idC, db.EdgeRelated, now, now); err != nil {
		t.Fatalf("edge B→C: %v", err)
	}

	// Seed = A only. 2-hop walk must reach B (hop 1) and C (hop 2).
	seeds := []MergedResult{{ID: idA, Score: 0.01}}
	got := expandViaGraph(ctx, pg, logger, seeds, queryVec, cubeID, cubeID, "")

	var b, c *MergedResult
	for i := range got {
		switch got[i].ID {
		case idB:
			b = &got[i]
		case idC:
			c = &got[i]
		}
	}
	if b == nil {
		t.Fatalf("hop-1 neighbour B not in expansion: ids=%v", idsOf(got))
	}
	if c == nil {
		t.Fatalf("hop-2 neighbour C not in expansion: ids=%v", idsOf(got))
	}
	// C is query-aligned (cos=1, decay 0.64) → score ≈ 1.0 * 0.64 = 0.64
	// B is orthogonal to query (cos=0, norm 0.5, decay 0.8) → score ≈ 0.4
	if c.Score <= b.Score {
		t.Fatalf("query-aligned hop-2 C must outrank orthogonal hop-1 B: B=%v C=%v", b.Score, c.Score)
	}
	// And both must beat the tiny RRF seed (0.01) — pre-fix they did NOT.
	if !(c.Score > seeds[0].Score && b.Score > seeds[0].Score) {
		t.Fatalf("expansion items must beat tiny RRF seed: seed=%v B=%v C=%v",
			seeds[0].Score, b.Score, c.Score)
	}
	t.Logf("D2 fix verified: seed=%.4f B(hop1,orth)=%.4f C(hop2,aligned)=%.4f",
		seeds[0].Score, b.Score, c.Score)
}

// insertD2Node writes one Memory row with given ID + embedding under cubeID.
func insertD2Node(ctx context.Context, pg *db.Postgres, cubeID, id, text string, embedding []float32) error {
	now := time.Now().UTC().Format(time.RFC3339)
	props := map[string]any{
		"id":          id,
		"memory":      text,
		"memory_type": "LongTermMemory",
		"user_name":   cubeID,
		"user_id":     cubeID,
		"status":      "activated",
		"created_at":  now,
		"updated_at":  now,
		"confidence":  0.9,
		"source":      "d2-livepg-test",
	}
	raw, err := json.Marshal(props)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return pg.InsertMemoryNodes(ctx, []db.MemoryInsertNode{{
		ID:             id,
		PropertiesJSON: raw,
		EmbeddingVec:   db.FormatVector(embedding),
	}})
}

func unitVecD2(axis, dim int) []float32 {
	v := make([]float32, dim)
	v[axis%dim] = 1
	return v
}

func idsOf(items []MergedResult) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func cleanupD2Cube(ctx context.Context, t *testing.T, pg *db.Postgres, cubeID string) {
	t.Helper()
	pool := pg.Pool()
	if pool == nil {
		return
	}
	const delEdgesSQL = `
DELETE FROM memos_graph.memory_edges
WHERE from_id IN (
	SELECT properties->>('id'::text) FROM memos_graph."Memory"
	WHERE properties->>('user_name'::text) = $1)
   OR to_id IN (
	SELECT properties->>('id'::text) FROM memos_graph."Memory"
	WHERE properties->>('user_name'::text) = $1)`
	const delMemorySQL = `DELETE FROM memos_graph."Memory"
WHERE properties->>('user_name'::text) = $1`
	for _, step := range []struct {
		label string
		sql   string
	}{
		{"memory_edges", delEdgesSQL},
		{"Memory", delMemorySQL},
	} {
		if _, err := pool.Exec(ctx, step.sql, cubeID); err != nil {
			t.Logf("d2 livepg cleanup %s (cube=%s): %v", step.label, cubeID, err)
		}
	}
}
