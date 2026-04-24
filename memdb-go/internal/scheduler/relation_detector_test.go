package scheduler

// relation_detector_test.go — D3 tests for DetectRelationPair.
//
// Verifies:
//   - CAUSES / CONTRADICTS / SUPPORTS / RELATED → memory_edges row written with
//     confidence + rationale.
//   - NONE → no edge written.
//   - confidence < threshold → no edge written.
//   - Edge constants from db package are used (not raw strings).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func relationMockServer(t *testing.T, relation string, confidence float64, rationale string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := fmt.Sprintf(`{"relation":%q,"confidence":%g,"rationale":%q}`, relation, confidence, rationale)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func newRelationStubReorganizer(t *testing.T, srv *httptest.Server) (*Reorganizer, *treeStub) {
	stub := &treeStub{}
	r := &Reorganizer{
		postgres:  stub,
		llmClient: testLLMClient(srv.URL),
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	return r, stub
}

func TestDetectRelationPair_Causes_WritesEdge(t *testing.T) {
	srv := relationMockServer(t, "CAUSES", 0.9, "A directly causes B.")
	defer srv.Close()
	r, stub := newRelationStubReorganizer(t, srv)

	rel, conf, err := r.DetectRelationPair(context.Background(), "mem-a", "It rained all night.", "mem-b", "The lawn flooded.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != db.EdgeCauses {
		t.Errorf("relation = %q, want %q", rel, db.EdgeCauses)
	}
	if conf != 0.9 {
		t.Errorf("confidence = %v, want 0.9", conf)
	}
	if len(stub.edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(stub.edges))
	}
	e := stub.edges[0]
	if e.From != "mem-a" || e.To != "mem-b" || e.Relation != db.EdgeCauses {
		t.Errorf("edge fields wrong: %+v", e)
	}
	if e.Confidence != 0.9 {
		t.Errorf("confidence in edge = %v, want 0.9", e.Confidence)
	}
	if e.Rationale == "" {
		t.Errorf("expected rationale, got empty string")
	}
}

func TestDetectRelationPair_None_NoEdge(t *testing.T) {
	srv := relationMockServer(t, "NONE", 0.1, "")
	defer srv.Close()
	r, stub := newRelationStubReorganizer(t, srv)

	rel, _, err := r.DetectRelationPair(context.Background(), "mem-a", "text A", "mem-b", "text B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != "" {
		t.Errorf("expected empty relation, got %q", rel)
	}
	if len(stub.edges) != 0 {
		t.Errorf("expected 0 edges for NONE, got %d", len(stub.edges))
	}
}

func TestDetectRelationPair_LowConfidence_NoEdge(t *testing.T) {
	// Below minRelationConfidence threshold (0.55).
	srv := relationMockServer(t, "RELATED", 0.3, "vague link")
	defer srv.Close()
	r, stub := newRelationStubReorganizer(t, srv)

	rel, conf, err := r.DetectRelationPair(context.Background(), "mem-a", "text A", "mem-b", "text B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != "" {
		t.Errorf("expected no relation written for low confidence, got %q", rel)
	}
	if conf != 0.3 {
		t.Errorf("confidence = %v, want 0.3", conf)
	}
	if len(stub.edges) != 0 {
		t.Errorf("expected 0 edges when confidence < threshold, got %d", len(stub.edges))
	}
}

func TestDetectRelationPair_Contradicts_MapsToEdgeConst(t *testing.T) {
	srv := relationMockServer(t, "CONTRADICTS", 0.75, "Incompatible facts.")
	defer srv.Close()
	r, stub := newRelationStubReorganizer(t, srv)

	rel, _, _ := r.DetectRelationPair(context.Background(), "mem-a", "User lives in NYC.", "mem-b", "User moved to Berlin.")
	if rel != db.EdgeContradicts {
		t.Errorf("relation = %q, want %q", rel, db.EdgeContradicts)
	}
	if len(stub.edges) != 1 || stub.edges[0].Relation != db.EdgeContradicts {
		t.Errorf("expected 1 CONTRADICTS edge, got %+v", stub.edges)
	}
}

func TestDetectRelationPair_Supports_MapsToEdgeConst(t *testing.T) {
	srv := relationMockServer(t, "SUPPORTS", 0.8, "Evidence for B.")
	defer srv.Close()
	r, stub := newRelationStubReorganizer(t, srv)

	rel, _, _ := r.DetectRelationPair(context.Background(), "mem-a", "Sales rose.", "mem-b", "Marketing launched a campaign.")
	if rel != db.EdgeSupports {
		t.Errorf("relation = %q, want %q", rel, db.EdgeSupports)
	}
	if len(stub.edges) != 1 || stub.edges[0].Relation != db.EdgeSupports {
		t.Errorf("expected 1 SUPPORTS edge, got %+v", stub.edges)
	}
}

func TestDetectRelationPair_EmptyInputs_NoOp(t *testing.T) {
	srv := relationMockServer(t, "CAUSES", 0.9, "")
	defer srv.Close()
	r, stub := newRelationStubReorganizer(t, srv)

	_, _, err := r.DetectRelationPair(context.Background(), "", "", "mem-b", "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.edges) != 0 {
		t.Errorf("expected no edges for empty from id, got %d", len(stub.edges))
	}
}

func TestNormalizeRelation_Vocabulary(t *testing.T) {
	cases := map[string]string{
		"CAUSES":      db.EdgeCauses,
		"cause":       db.EdgeCauses,
		"CONTRADICTS": db.EdgeContradicts,
		"SUPPORTS":    db.EdgeSupports,
		"RELATED":     db.EdgeRelated,
		"NONE":        "",
		"gibberish":   "",
		"":            "",
	}
	for in, want := range cases {
		if got := normalizeRelation(in); got != want {
			t.Errorf("normalizeRelation(%q) = %q, want %q", in, got, want)
		}
	}
}
