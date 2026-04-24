package scheduler

// tree_relation_phase_test.go — runRelationPhase + follow-up #2 metric tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// relationCountingServer returns {relation, confidence, rationale} and bumps
// calls on every request so tests can assert budget compliance.
func relationCountingServer(t *testing.T, relation string, confidence float64, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		content := fmt.Sprintf(`{"relation":%q,"confidence":%g,"rationale":"because"}`, relation, confidence)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// makeParents synthesises n parentInfos with distinct embeddings so cosine
// scores are deterministic. embedding[i] = [1-i*0.01, i*0.01, 0].
func makeParents(n int) []parentInfo {
	out := make([]parentInfo, n)
	for i := 0; i < n; i++ {
		out[i] = parentInfo{
			ID:        fmt.Sprintf("p-%d", i),
			Text:      fmt.Sprintf("parent text %d", i),
			Embedding: []float32{1 - float32(i)*0.01, float32(i) * 0.01, 0},
			Tier:      "semantic",
		}
	}
	return out
}

func newRelationReorg(t *testing.T, srv *httptest.Server, buf *bytes.Buffer) (*Reorganizer, *treeStub) {
	t.Helper()
	stub := &treeStub{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return &Reorganizer{
		postgres:  stub,
		llmClient: testLLMClient(srv.URL),
		logger:    slog.New(h),
	}, stub
}

// 1) Feature flag off → zero LLM calls, zero edges.
func TestRunRelationPhase_Disabled_NoLLMCalls(t *testing.T) {
	t.Setenv("MEMDB_D3_RELATION_DETECTION", "")
	var calls atomic.Int32
	srv := relationCountingServer(t, "CAUSES", 0.9, &calls)
	defer srv.Close()

	r, stub := newRelationReorg(t, srv, &bytes.Buffer{})
	r.runRelationPhase(context.Background(), "cube", makeParents(4))

	if calls.Load() != 0 {
		t.Errorf("expected 0 LLM calls when disabled, got %d", calls.Load())
	}
	if len(stub.edges) != 0 {
		t.Errorf("expected 0 edges when disabled, got %d", len(stub.edges))
	}
}

// 2) Budget cap — MEMDB_D3_MAX_RELATION_PAIRS=4 limits attempts.
func TestRunRelationPhase_TopK_RespectsBudget(t *testing.T) {
	t.Setenv("MEMDB_D3_RELATION_DETECTION", "true")
	t.Setenv("MEMDB_D3_MAX_RELATION_PAIRS", "4")
	t.Setenv("MEMDB_D3_RELATION_TOPK", "3")

	var calls atomic.Int32
	// NONE skips edge writes but still counts as an LLM call — isolates pair-budget.
	srv := relationCountingServer(t, "NONE", 0.1, &calls)
	defer srv.Close()

	r, _ := newRelationReorg(t, srv, &bytes.Buffer{})
	r.runRelationPhase(context.Background(), "cube", makeParents(5))

	got := calls.Load()
	if got > 4 {
		t.Errorf("budget exceeded: expected ≤ 4 LLM calls, got %d", got)
	}
	if got == 0 {
		t.Errorf("expected > 0 LLM calls, got 0 (phase gated off?)")
	}
}

// 3) Happy path — 3 parents, CAUSES @0.9 → all attempted pairs write edges.
func TestRunRelationPhase_WritesEdges(t *testing.T) {
	t.Setenv("MEMDB_D3_RELATION_DETECTION", "true")
	t.Setenv("MEMDB_D3_MAX_RELATION_PAIRS", "10")
	t.Setenv("MEMDB_D3_RELATION_TOPK", "2")

	srv := relationCountingServer(t, "CAUSES", 0.9, nil)
	defer srv.Close()

	r, stub := newRelationReorg(t, srv, &bytes.Buffer{})
	r.runRelationPhase(context.Background(), "cube", makeParents(3))

	// 3 parents × topK=2 = 6 directed pairs.
	if len(stub.edges) != 6 {
		t.Errorf("expected 6 CAUSES edges, got %d", len(stub.edges))
	}
	for _, e := range stub.edges {
		if e.Relation != db.EdgeCauses {
			t.Errorf("unexpected relation %q (want %q)", e.Relation, db.EdgeCauses)
		}
	}
}

// 4) Follow-up #2: edge-write failure is Warn-logged (not Debug) and cluster
// finishes with its remaining edges. Uses a stub that fails the first edge.
func TestPromoteCluster_EdgeWriteError_LogsWarn(t *testing.T) {
	t.Setenv("MEMDB_REORG_HIERARCHY", "true")

	var calls atomic.Int32
	srv := tierMockServer(t, &calls)
	defer srv.Close()

	// 5 raw mems, all in one cluster → 5 CONSOLIDATED_INTO attempts, first fails.
	group := []float32{1, 0, 0, 0}
	raw := makeHierarchyMems("a", 5, group)

	buf := &bytes.Buffer{}
	stub := &edgeFailStub{treeStub: treeStub{rawMems: raw}, failCount: 1}
	r := &Reorganizer{
		postgres:  stub,
		embedder:  &treeEmbedder{},
		llmClient: testLLMClient(srv.URL),
		logger:    slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	r.RunTreeReorgForCube(context.Background(), "cube-test")

	// 4 successful edges (5 attempted, 1 failed).
	consolidated := 0
	for _, e := range stub.edges {
		if e.Relation == "CONSOLIDATED_INTO" {
			consolidated++
		}
	}
	if consolidated != 4 {
		t.Errorf("expected 4 successful edges, got %d", consolidated)
	}
	// Audit event still written (cluster finished).
	if len(stub.events) != 1 {
		t.Errorf("expected 1 audit event, got %d", len(stub.events))
	}
	// Warn-level log present for the failed edge.
	logs := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("edge write failed")) {
		t.Errorf("expected 'edge write failed' in logs, got: %s", logs)
	}
	if !bytes.Contains(buf.Bytes(), []byte("level=WARN")) {
		t.Errorf("expected WARN-level log, got: %s", logs)
	}
	// Sanity: ensure we did not leak Debug on this path (old behaviour).
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if bytes.Contains(line, []byte("edge write failed")) && bytes.Contains(line, []byte("level=DEBUG")) {
			t.Errorf("edge-write failure regressed to DEBUG level: %s", line)
		}
	}
}

// edgeFailStub fails the first N CreateMemoryEdge calls, then succeeds.
// Embeds treeStub so all other surface remains identical.
type edgeFailStub struct {
	treeStub
	failCount int
	calls     int32
}

func (s *edgeFailStub) CreateMemoryEdge(ctx context.Context, from, to, rel, a, b string) error {
	n := atomic.AddInt32(&s.calls, 1)
	if int(n) <= s.failCount {
		return fmt.Errorf("synthetic edge-write failure %d", n)
	}
	return s.treeStub.CreateMemoryEdge(ctx, from, to, rel, a, b)
}
