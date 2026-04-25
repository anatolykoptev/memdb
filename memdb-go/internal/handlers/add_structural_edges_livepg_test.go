//go:build livepg

// add_structural_edges_livepg_test.go — M8 Stream 10 end-to-end probe.
//
// What this exercises:
//   1. nativeRawAddForCube called once per turn for a 5-message conversation
//      with a stable session_id, against a live MEMDB_LIVE_PG_DSN.
//   2. After all 5 inserts, queries memos_graph.memory_edges and asserts the
//      structural edges landed:
//         SAME_SESSION         ≥ 4 (each new memory connects to ≥1 prior)
//         TIMELINE_NEXT        ≥ 4 (chain of 5 turns has 4 next-links)
//      SIMILAR_COSINE_HIGH count is content-dependent so we do not pin it.
//   3. Captures end-to-end latency for the structural-edge tail to feed the
//      "< 5ms per call" budget assertion (advisory only — t.Logf, no fail).
//
// Gating (build tag + DSN env) mirrors the other livepg tests.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_StructuralEdges_RawSession -count=1 -v

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLivePG_StructuralEdges_RawSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	cube := fmt.Sprintf("livepg-structedges-%d", time.Now().UnixNano())
	session := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, cube)
	defer func() {
		// Sweep edges whose endpoints belong to this cube. memory_edges has
		// no FK to Memory, so we have to clean explicitly. Cheap on a
		// freshly-created cube — bounded by len(memoryIDs).
		pool := pg.Pool()
		if pool == nil {
			return
		}
		const delEdges = `DELETE FROM memos_graph."memory_edges"
			WHERE from_id IN (
				SELECT properties->>('id'::text) FROM memos_graph."Memory"
				WHERE properties->>('user_name'::text) = $1
			)`
		if _, err := pool.Exec(ctx, delEdges, cube); err != nil {
			t.Logf("livepg cleanup memory_edges (cube=%s): %v", cube, err)
		}
	}()

	h := &Handler{
		logger:   logger,
		postgres: pg,
		embedder: &stubEmbedder{},
	}

	turns := []string{
		"User wakes up at six and brews coffee.",
		"They write Go code for two hours straight on the plan-of-record.",
		"Lunch break: leftover pizza and a quick walk around the block.",
		"Afternoon meeting with the team about the M8 milestone.",
		"They finish the day reviewing pull requests and shipping a release.",
	}

	var memoryIDs []string
	totalDur := time.Duration(0)
	for i, content := range turns {
		req := &fullAddRequest{
			UserID:          strPtr(cube),
			Mode:            strPtr(modeRaw),
			SessionID:       strPtr(session),
			Messages:        []chatMessage{{Role: "user", Content: content}},
			WritableCubeIDs: []string{cube},
		}
		start := time.Now()
		items, err := h.nativeRawAddForCube(ctx, req, cube)
		totalDur += time.Since(start)
		if err != nil {
			t.Fatalf("turn %d add failed: %v", i, err)
		}
		if len(items) != 1 {
			t.Fatalf("turn %d: items = %d, want 1", i, len(items))
		}
		memoryIDs = append(memoryIDs, items[0].MemoryID)
	}

	if len(memoryIDs) != 5 {
		t.Fatalf("memoryIDs = %d, want 5", len(memoryIDs))
	}

	// Query edges sourced from any of the 5 new IDs.
	pool := pg.Pool()
	if pool == nil {
		t.Fatal("pg pool nil")
	}
	const q = `
		SELECT relation, COUNT(*)
		FROM memos_graph."memory_edges"
		WHERE from_id = ANY($1)
		GROUP BY relation
	`
	rows, err := pool.Query(ctx, q, memoryIDs)
	if err != nil {
		t.Fatalf("edge count query failed: %v", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var rel string
		var n int
		if err := rows.Scan(&rel, &n); err != nil {
			t.Fatalf("edge count scan: %v", err)
		}
		counts[rel] = n
	}
	t.Logf("structural edges by relation: %+v", counts)
	t.Logf("end-to-end add latency (5 turns total): %s (avg %s/turn)",
		totalDur, totalDur/time.Duration(len(turns)))

	if counts["SAME_SESSION"] < 4 {
		t.Errorf("SAME_SESSION = %d, want >= 4", counts["SAME_SESSION"])
	}
	if counts["TIMELINE_NEXT"] < 4 {
		t.Errorf("TIMELINE_NEXT = %d, want >= 4", counts["TIMELINE_NEXT"])
	}
}

