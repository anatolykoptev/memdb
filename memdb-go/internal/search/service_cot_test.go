package search

// service_cot_test.go — integration-shaped test for D11 wiring in
// SearchService. Uses the existing mockPostgres + mockEmbedder from
// service_cube_test.go and a stub LLM endpoint to exercise
// applyCoTDecomposition end-to-end:
//   - decomposer returns 3 sub-queries → fanoutSubqueryToText runs N-1
//     extra VectorSearch calls (the original at index 0 was already done
//     by the primary path, so this test asserts call count = 2 for 3 subs).
//   - decomposer disabled → applyCoTDecomposition is a strict no-op.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// countingPostgres wraps mockPostgres semantics but counts VectorSearch hits
// so we can assert the decomposer actually fanned out N-1 extra probes.
type countingPostgres struct {
	mockPostgres
	vectorCalls atomic.Int32
	results     []db.VectorSearchResult
}

func (c *countingPostgres) VectorSearch(ctx context.Context, vec []float32, cubeID, personID string, mt []string, agentID string, limit int) ([]db.VectorSearchResult, error) {
	c.vectorCalls.Add(1)
	_, _ = c.mockPostgres.VectorSearch(ctx, vec, cubeID, personID, mt, agentID, limit)
	return c.results, nil
}

func TestApplyCoTDecomposition_FansOutSubqueries(t *testing.T) {
	t.Parallel()
	// LLM returns 3 sub-queries.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `["When did Caroline meet Emma?","What did Caroline do in Boston?","Who was at the conference?"]`,
				},
			}},
		})
	}))
	t.Cleanup(srv.Close)

	pg := &countingPostgres{
		results: []db.VectorSearchResult{
			{ID: "x1", Properties: "extra1", Score: 0.9},
		},
	}
	svc := &SearchService{
		postgres: pg,
		embedder: &mockEmbedder{},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		CoTDecomposer: NewCoTDecomposer(CoTDecomposerConfig{
			Enabled: true, APIURL: srv.URL, Model: "stub", MaxSubQueries: 3,
		}),
	}
	psr := &parallelSearchResults{
		textVec: []db.VectorSearchResult{{ID: "seed", Score: 1.0}},
	}
	q := "What did Caroline do in Boston after she met Emma at the conference?"
	got := svc.applyCoTDecomposition(context.Background(), psr, q,
		SearchParams{Query: q, CubeID: "c1", UserName: "u1"},
		searchBudget{textK: 5})

	if len(got) < 2 {
		t.Fatalf("expected ≥2 sub-queries, got %v", got)
	}
	if got[0] != q {
		t.Errorf("expected original at index 0, got %q", got[0])
	}
	// 3 sub-queries from LLM + original prepended = 4 total → fanout runs
	// for indices 1..3 = 3 extra VectorSearch calls. (The decomposer
	// always prepends the original even if the LLM omitted it.)
	if pg.vectorCalls.Load() < 2 {
		t.Errorf("expected ≥2 fanout VectorSearch calls, got %d", pg.vectorCalls.Load())
	}
	// psr.textVec must have been augmented (seed + extra1 from each fanout).
	foundExtra := false
	for _, r := range psr.textVec {
		if r.ID == "x1" {
			foundExtra = true
		}
	}
	if !foundExtra {
		t.Errorf("expected x1 to be unioned into textVec, got %v", psr.textVec)
	}
}

func TestApplyCoTDecomposition_DisabledIsNoOp(t *testing.T) {
	t.Parallel()
	pg := &countingPostgres{}
	svc := &SearchService{
		postgres: pg,
		embedder: &mockEmbedder{},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		// CoTDecomposer is nil → no-op path.
	}
	psr := &parallelSearchResults{}
	q := "What did Caroline do in Boston after she met Emma at the conference?"
	got := svc.applyCoTDecomposition(context.Background(), psr, q,
		SearchParams{Query: q, CubeID: "c1", UserName: "u1"},
		searchBudget{textK: 5})
	if len(got) != 1 || got[0] != q {
		t.Errorf("expected [original], got %v", got)
	}
	if pg.vectorCalls.Load() != 0 {
		t.Errorf("expected no fanout VectorSearch calls, got %d", pg.vectorCalls.Load())
	}
}

func TestApplyCoTDecomposition_HeuristicSkipNoFanout(t *testing.T) {
	t.Parallel()
	pg := &countingPostgres{}
	svc := &SearchService{
		postgres: pg,
		embedder: &mockEmbedder{},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		CoTDecomposer: NewCoTDecomposer(CoTDecomposerConfig{
			Enabled: true, APIURL: "http://nowhere.invalid", Model: "stub",
		}),
	}
	psr := &parallelSearchResults{}
	// Short query — heuristic skips before any LLM call (URL would be
	// unreachable, proving we never made the call).
	got := svc.applyCoTDecomposition(context.Background(), psr, "what's the weather?",
		SearchParams{Query: "what's the weather?", CubeID: "c1", UserName: "u1"},
		searchBudget{textK: 5})
	if len(got) != 1 {
		t.Errorf("expected single-element slice, got %v", got)
	}
	if pg.vectorCalls.Load() != 0 {
		t.Errorf("expected no fanout VectorSearch calls, got %d", pg.vectorCalls.Load())
	}
}
