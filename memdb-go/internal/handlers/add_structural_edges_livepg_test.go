//go:build livepg

// add_structural_edges_livepg_test.go — M8 Stream 10 end-to-end probe.
//
// What this exercises:
//   1. TestLivePG_StructuralEdges_RawSession — 5-message session; asserts
//      exact edge counts: SAME_SESSION==10 (5×4/2 pairs), TIMELINE_NEXT==4
//      (n-1 chain links).
//   2. TestLivePG_StructuralEdges_CapFires — 26-message session that exceeds
//      the sameSessionMaxPartners=20 cap. Asserts SAME_SESSION cap fires
//      (==20 partners for memory-26, not 25) AND TIMELINE_NEXT links memory-26
//      to memory-25 (the IMMEDIATE predecessor), not memory-1 (oldest).
//
// Gating (build tag + DSN env) mirrors the other livepg tests.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_StructuralEdges -count=1 -v

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

	// n=5 session: SAME_SESSION = n*(n-1)/2 = 10, TIMELINE_NEXT = n-1 = 4.
	// These are exact: if the query returns more, a bug double-emitted edges;
	// if fewer, the cap fired incorrectly or ordering omitted predecessors.
	if counts["SAME_SESSION"] != 10 {
		t.Errorf("SAME_SESSION = %d, want exactly 10 (5×4/2 pairs)", counts["SAME_SESSION"])
	}
	if counts["TIMELINE_NEXT"] != 4 {
		t.Errorf("TIMELINE_NEXT = %d, want exactly 4 (n-1 chain links)", counts["TIMELINE_NEXT"])
	}
}

// TestLivePG_StructuralEdges_CapFires inserts 26 memories in a single session
// and verifies:
//
//  1. SAME_SESSION cap fires: memory-26 gets exactly 20 SAME_SESSION partners
//     (capped at sameSessionMaxPartners), not 25.
//  2. TIMELINE_NEXT links memory-26 to memory-25 (the IMMEDIATE predecessor),
//     not to memory-1 (oldest), confirming ORDER BY DESC is in effect.
func TestLivePG_StructuralEdges_CapFires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	cube := fmt.Sprintf("livepg-capfires-%d", time.Now().UnixNano())
	session := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, cube)
	defer func() {
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

	const totalTurns = 26
	contents := make([]string, totalTurns)
	for i := range contents {
		contents[i] = fmt.Sprintf("Turn %d: some unique content about topic %d for the cap-fires test.", i+1, i+1)
	}

	// Insert all turns sequentially with monotonically-increasing timestamps
	// baked into the content (chat_time is derived from the insert order).
	var memoryIDs []string
	for i, content := range contents {
		req := &fullAddRequest{
			UserID:          strPtr(cube),
			Mode:            strPtr(modeRaw),
			SessionID:       strPtr(session),
			Messages:        []chatMessage{{Role: "user", Content: content}},
			WritableCubeIDs: []string{cube},
		}
		items, err := h.nativeRawAddForCube(ctx, req, cube)
		if err != nil {
			t.Fatalf("turn %d add failed: %v", i+1, err)
		}
		if len(items) != 1 {
			t.Fatalf("turn %d: items = %d, want 1", i+1, len(items))
		}
		memoryIDs = append(memoryIDs, items[0].MemoryID)
	}

	if len(memoryIDs) != totalTurns {
		t.Fatalf("memoryIDs = %d, want %d", len(memoryIDs), totalTurns)
	}

	pool := pg.Pool()
	if pool == nil {
		t.Fatal("pg pool nil")
	}

	lastID := memoryIDs[totalTurns-1]    // memory-26
	prevID := memoryIDs[totalTurns-2]    // memory-25 — expected TIMELINE_NEXT target
	firstID := memoryIDs[0]              // memory-1  — must NOT be the TIMELINE_NEXT target

	// Assert 1: SAME_SESSION cap fires for memory-26.
	// It should have exactly sameSessionMaxPartners = 20 outbound edges, not 25.
	const qSameSession = `
		SELECT COUNT(*)
		FROM memos_graph."memory_edges"
		WHERE from_id = $1
		  AND relation = 'SAME_SESSION'
	`
	var ssCount int
	if err := pool.QueryRow(ctx, qSameSession, lastID).Scan(&ssCount); err != nil {
		t.Fatalf("SAME_SESSION count query failed: %v", err)
	}
	t.Logf("SAME_SESSION edges from memory-26: %d (cap=%d)", ssCount, sameSessionMaxPartners)
	if ssCount != sameSessionMaxPartners {
		t.Errorf("SAME_SESSION for memory-26 = %d, want exactly %d (cap)", ssCount, sameSessionMaxPartners)
	}

	// Assert 2: TIMELINE_NEXT from memory-26 points to memory-25.
	// This verifies ORDER BY DESC: the immediate predecessor must be selected,
	// not the oldest row in the session.
	const qTimeline = `
		SELECT to_id
		FROM memos_graph."memory_edges"
		WHERE from_id = $1
		  AND relation = 'TIMELINE_NEXT'
		LIMIT 1
	`
	var toID string
	if err := pool.QueryRow(ctx, qTimeline, lastID).Scan(&toID); err != nil {
		t.Fatalf("TIMELINE_NEXT query failed: %v", err)
	}
	t.Logf("TIMELINE_NEXT from memory-26 → %s (want=%s, must_not=%s)", toID, prevID, firstID)
	if toID != prevID {
		t.Errorf("TIMELINE_NEXT from memory-26 → %s, want %s (memory-25, immediate predecessor); got %s instead",
			toID, prevID, toID)
		if toID == firstID {
			t.Errorf("  REGRESSION: linked to memory-1 (oldest), ORDER BY ASC bug is present")
		}
	}
}

