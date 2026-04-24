//go:build livepg

// Package scheduler — tree_relation_phase_livepg_test.go: end-to-end test for
// the D3 relation-detection phase against a live Postgres + AGE 1.7 + pgvector.
//
// Motivation: PR #58 shipped runRelationPhase with stub-based unit tests, but
// empirical smoke-testing could not reproduce parents_collected ≥ 2 because
// short raw text collapses through e5-large into one union-find cluster. The
// write path (CreateMemoryEdgeWithConfidence → memos_graph.memory_edges with
// confidence + rationale columns) therefore stayed unverified against real
// AGE/pgvector. This test closes that gap by feeding synthetic orthogonal
// embeddings so multiple parents reliably form, then asserts edges land with
// the D3 marker columns populated.
//
// Gating:
//   - build tag `livepg` keeps this file excluded from the default test binary
//     (plain `go test ./...` must still pass in CI / local loops).
//   - MEMDB_LIVE_PG_DSN must be set or the test t.Skip's. Format:
//     postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/scheduler/... \
//	    -run TestLivePG_RunRelationPhase_WritesEdges -count=1 -v

package scheduler

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestLivePG_RunRelationPhase_WritesEdges(t *testing.T) {
	// Belt-and-braces D3 gates — RunTreeReorgForCube is gate-agnostic per its
	// docstring, but runRelationPhase reads MEMDB_D3_RELATION_DETECTION live
	// on every call, so the env flip is load-bearing.
	t.Setenv("MEMDB_D3_RELATION_DETECTION", "true")
	t.Setenv("MEMDB_REORG_HIERARCHY", "true")
	t.Setenv("MEMDB_D3_MIN_CLUSTER_RAW", "2")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	pg := openLivePG(ctx, t, logger)

	cubeID := "test-livepg-relation-" + uuid.New().String()
	t.Logf("livepg test cube_id=%s", cubeID)

	// Double-cleanup: t.Cleanup for success path, defer for panic / Fatal paths
	// where t.Cleanup may not fire cleanly under some harness config. A
	// sync.Once guards against re-running the DELETEs against an already
	// closed pool (the second invocation would log spurious "closed pool"
	// errors that mask real failures).
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			cleanupLivepgCube(cleanupCtx, t, pg, cubeID)
			pg.Close()
		})
	}
	t.Cleanup(cleanup)
	defer cleanup()

	// ---- Fixture: 6 raw memories, 3 orthogonal clusters × 2 each ----
	rawMems := makeLivepgRawMemories()
	insertNodes, err := buildLivepgInsertNodes(cubeID, rawMems)
	if err != nil {
		t.Fatalf("build insert nodes: %v", err)
	}
	if err := pg.InsertMemoryNodes(ctx, insertNodes); err != nil {
		t.Fatalf("insert raw memories: %v", err)
	}
	t.Logf("inserted %d raw memories", len(insertNodes))

	// ---- Mock LLM (tier summariser + relation detector dispatcher) ----
	llmSrv := livepgMockServer(t)
	defer llmSrv.Close()

	// ---- Construct Reorganizer with real DB + mock LLM + 1024-dim embedder ---
	reorg := &Reorganizer{
		postgres:  pg,
		embedder:  &livepgEmbedder{},
		llmClient: testLLMClient(llmSrv.URL),
		logger:    logger,
	}

	// ---- Exercise ----
	reorg.RunTreeReorgForCube(ctx, cubeID)

	// ---- Assertions ----
	assertLivepgRelationEdges(ctx, t, pg, cubeID)
	assertLivepgConsolidatedInto(ctx, t, pg, cubeID)
}

// assertLivepgRelationEdges validates the D3 relation path: edges written
// with relation IN the relation-detector vocabulary, confidence IS NOT NULL
// AND = 0.9 (mock value), rationale = 'synthetic' (mock value), and the count
// stays within the expected top-k budget envelope.
func assertLivepgRelationEdges(ctx context.Context, t *testing.T, pg *db.Postgres, cubeID string) {
	t.Helper()
	const q = `
SELECT e.relation, e.confidence, COALESCE(e.rationale, '')
FROM memos_graph.memory_edges e
WHERE (e.from_id IN (
		SELECT properties->>('id'::text)
		FROM memos_graph."Memory"
		WHERE properties->>('user_name'::text) = $1
	) OR e.to_id IN (
		SELECT properties->>('id'::text)
		FROM memos_graph."Memory"
		WHERE properties->>('user_name'::text) = $1
	))
  AND e.relation IN ('CAUSES','CONTRADICTS','SUPPORTS','RELATED')`
	rows, err := pg.Pool().Query(ctx, q, cubeID)
	if err != nil {
		t.Fatalf("query relation edges: %v", err)
	}
	defer rows.Close()

	type edge struct {
		Relation   string
		Confidence *float64
		Rationale  string
	}
	var edges []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.Relation, &e.Confidence, &e.Rationale); err != nil {
			t.Fatalf("scan edge row: %v", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate edges: %v", err)
	}

	if len(edges) == 0 {
		t.Fatalf("no D3 relation edges written — expected ≥1 CAUSES edge")
	}
	matched := 0
	for _, e := range edges {
		if e.Confidence == nil {
			t.Errorf("relation edge has NULL confidence (rel=%s) — D3 path should always stamp it", e.Relation)
			continue
		}
		if *e.Confidence == 0.9 && e.Rationale == "synthetic" && e.Relation == "CAUSES" {
			matched++
		}
	}
	if matched == 0 {
		t.Fatalf("no edge matched mock signature (CAUSES @0.9 rationale=synthetic); got %+v", edges)
	}
	// With 3 parents × topK=3 (default), budget cap = 10 (default):
	// each parent has 2 other parents → 2 targets × 3 parents = 6 directed pairs.
	// Tolerate [3, 10] — 3 accommodates dedup / top-k interactions, 10 is the
	// hard max from maxRelationPairs default.
	if len(edges) < 3 || len(edges) > maxRelationPairs() {
		t.Errorf("relation edge count %d outside expected [3, %d] range",
			len(edges), maxRelationPairs())
	}
	t.Logf("relation edges written: %d (mock-signature matches: %d)", len(edges), matched)
}

// assertLivepgConsolidatedInto verifies the tier-promotion write path also
// fired: every raw memory that joined a cluster gets a CONSOLIDATED_INTO edge
// to its episodic parent. With 3 clusters × 2 memories we expect ≥ 6 edges.
func assertLivepgConsolidatedInto(ctx context.Context, t *testing.T, pg *db.Postgres, cubeID string) {
	t.Helper()
	const q = `
SELECT count(*)
FROM memos_graph.memory_edges
WHERE relation = 'CONSOLIDATED_INTO'
  AND from_id IN (
	SELECT properties->>('id'::text)
	FROM memos_graph."Memory"
	WHERE properties->>('user_name'::text) = $1
  )`
	var n int
	if err := pg.Pool().QueryRow(ctx, q, cubeID).Scan(&n); err != nil {
		t.Fatalf("count CONSOLIDATED_INTO: %v", err)
	}
	if n < 6 {
		t.Errorf("expected ≥ 6 CONSOLIDATED_INTO edges (3 clusters × 2 raw), got %d", n)
	}
	t.Logf("CONSOLIDATED_INTO edges: %d", n)
}
