package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
)

// newCEServerWithScores returns an httptest server that responds to
// `/v1/rerank` (and any path) with a Cohere-shaped payload using the
// caller-supplied scores. scores[i] becomes the relevance for the i-th
// document in the request.
func newCEServerWithScores(t *testing.T, scores []float64) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, 0, len(req.Documents))
		for i := range req.Documents {
			score := 0.0
			if i < len(scores) {
				score = scores[i]
			}
			results = append(results, map[string]any{
				"index":           i,
				"relevance_score": score,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	return ts, &calls
}

// mkBackendItems builds N stub items with id-i / text "memory text #i".
// Local helper so we don't share state with mkStubItems in staged_test.go.
func mkBackendItems(n int) []Item {
	out := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &stubItem{
			id:    "id-" + strconv.Itoa(i),
			score: 0.5,
			text:  "memory text #" + strconv.Itoa(i),
			meta:  map[string]any{},
		})
	}
	return out
}

// TestCEBackend_Stage2Refine_ReturnsTopKByScore checks the CE-only
// stage-2 path: top scoring doc IDs come back in score-desc order,
// capped to shortlistCap.
func TestCEBackend_Stage2Refine_ReturnsTopKByScore(t *testing.T) {
	// Five items; descending scores so the rerank server returns them
	// in the same order. shortlistCap=3 → top 3 IDs.
	srv, calls := newCEServerWithScores(t, []float64{0.9, 0.8, 0.7, 0.6, 0.5})
	defer srv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: srv.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	b := CEBackend{Client: client}
	items := mkBackendItems(5)
	ids, err := b.Stage2Refine(context.Background(), "q", items, 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got, want := atomic.LoadInt32(calls), int32(1); got != want {
		t.Fatalf("expected %d HTTP call, got %d", want, got)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d (%v)", len(ids), ids)
	}
	wantIDs := []string{"id-0", "id-1", "id-2"}
	for i, id := range wantIDs {
		if ids[i] != id {
			t.Errorf("position %d: want %q got %q (full %v)", i, id, ids[i], ids)
		}
	}
}

// TestCEBackend_Stage3Justify_ThresholdFilters checks the CE-only
// stage-3 path: items with score >= threshold are Relevant=true,
// below are Relevant=false. Justification stays empty by design.
func TestCEBackend_Stage3Justify_ThresholdFilters(t *testing.T) {
	// Three items; CEBackend.Stage3Justify scores ONLY the shortlist
	// subset, so the server must return scores indexed against that
	// subset (id-0=0.9 relevant, id-1=-0.5 below, id-2=2.0 relevant).
	srv, _ := newCEServerWithScores(t, []float64{0.9, -0.5, 2.0})
	defer srv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: srv.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	b := CEBackend{Client: client, CEThreshold: 0.0}
	items := mkBackendItems(3)
	out, err := b.Stage3Justify(context.Background(), "q", []string{"id-0", "id-1", "id-2"}, items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 justified items, got %d", len(out))
	}
	relByID := map[string]bool{}
	for _, j := range out {
		relByID[j.ID] = j.Relevant
		if j.Justification != "" {
			t.Errorf("CE backend should leave justification empty, got %q", j.Justification)
		}
	}
	if !relByID["id-0"] {
		t.Errorf("id-0 (score 0.9) should be relevant")
	}
	if relByID["id-1"] {
		t.Errorf("id-1 (score -0.5) should be NOT relevant under threshold 0.0")
	}
	if !relByID["id-2"] {
		t.Errorf("id-2 (score 2.0) should be relevant")
	}
}

// TestCEBackend_Stage3Justify_CustomThreshold flips the threshold to
// confirm the env knob actually moves the decision boundary.
func TestCEBackend_Stage3Justify_CustomThreshold(t *testing.T) {
	srv, _ := newCEServerWithScores(t, []float64{0.9, 0.5, 2.0})
	defer srv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: srv.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	// Threshold 1.0: only id-2 (score 2.0) survives.
	b := CEBackend{Client: client, CEThreshold: 1.0}
	out, err := b.Stage3Justify(context.Background(), "q", []string{"id-0", "id-1", "id-2"}, mkBackendItems(3))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	relevant := 0
	for _, j := range out {
		if j.Relevant {
			relevant++
		}
	}
	if relevant != 1 {
		t.Errorf("expected exactly 1 relevant under threshold 1.0, got %d", relevant)
	}
}

// TestCEBackend_NilClient_Errors confirms a CE backend without a wired
// client surfaces an error rather than silently returning nil — the
// strategy uses the error to fall through to "no rerank".
func TestCEBackend_NilClient_Errors(t *testing.T) {
	b := CEBackend{Client: nil}
	if _, err := b.Stage2Refine(context.Background(), "q", mkBackendItems(3), 2); err == nil {
		t.Errorf("expected error from nil client, got nil")
	}
	if _, err := b.Stage3Justify(context.Background(), "q", []string{"id-0"}, mkBackendItems(3)); err == nil {
		t.Errorf("expected error from nil client, got nil")
	}
}

// TestStaged_BackendCE_NoLLMCall is the headline regression test: with
// MEMDB_STAGED_BACKEND=ce and a working CE client, the LLM endpoint
// receives ZERO calls (the whole point of M12.6).
func TestStaged_BackendCE_NoLLMCall(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	t.Setenv("MEMDB_STAGED_BACKEND", "ce")

	// LLM server: any call here is a regression.
	llmCalls := int32(0)
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llmCalls, 1)
		t.Errorf("LLM must NOT be called when CE backend is active")
	}))
	defer llmSrv.Close()

	// CE server: 20 input docs, descending scores. Returns top-10 in stage 2,
	// then re-ranks the shortlist in stage 3.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, 0, len(req.Documents))
		// Descending scores so id-0 is most relevant, id-19 least.
		for i := range req.Documents {
			results = append(results, map[string]any{
				"index":           i,
				"relevance_score": float64(len(req.Documents)-i) * 0.05,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer srv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: srv.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	s := Staged{
		Config:       LLMConfig{APIURL: llmSrv.URL, Model: "m"}, // present, must NOT be hit
		RerankClient: client,
	}
	out, err := s.Rerank(context.Background(), "q", mkBackendItems(20))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 20 {
		t.Fatalf("expected 20 items returned (re-ordered, not dropped), got %d", len(out))
	}
	if got := atomic.LoadInt32(&llmCalls); got != 0 {
		t.Fatalf("expected 0 LLM calls, got %d", got)
	}
	// Top item should be id-0 (highest score in our mock).
	if out[0].ID() != "id-0" {
		t.Errorf("expected id-0 first under descending CE scores, got %s", out[0].ID())
	}
}

// TestStaged_BackendCE_MissingClientFallsBackToLLM confirms requested=ce
// with no client wired falls back to LLM (NOT silent skip) — that's the
// whole "never silently disable" requirement of the spec.
func TestStaged_BackendCE_MissingClientFallsBackToLLM(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	t.Setenv("MEMDB_STAGED_BACKEND", "ce")

	stage2 := `{"ids":["id-0","id-1"]}`
	stage3 := `{"items":[
		{"id":"id-0","justification":"y","relevant":true},
		{"id":"id-1","justification":"n","relevant":false}
	]}`
	llmSrv := mockStagedServer(t, []string{stage2, stage3})
	defer llmSrv.Close()

	s := Staged{
		Config:       LLMConfig{APIURL: llmSrv.URL, Model: "m"},
		RerankClient: nil, // CE requested but client missing → must use LLM
	}
	out, err := s.Rerank(context.Background(), "q", mkBackendItems(20))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 20 {
		t.Fatalf("expected 20 items kept, got %d", len(out))
	}
	if out[0].ID() != "id-0" {
		t.Errorf("expected id-0 first (LLM said relevant), got %s", out[0].ID())
	}
}

// TestStaged_BackendUnavailable_LogsWarn covers the "no usable backend"
// path: CE requested, no client, no LLM URL → strategy must skip with
// WARN log (NOT silent). We can't easily intercept slog here without
// adding a handler so we just verify the strategy didn't crash and
// returned the input unchanged. The WARN behaviour is exercised via
// inspection — if you change the path, leave the log statement.
func TestStaged_BackendUnavailable_NotSilentSkip(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	t.Setenv("MEMDB_STAGED_BACKEND", "ce")

	s := Staged{
		Config:       LLMConfig{}, // empty
		RerankClient: nil,
	}
	out, err := s.Rerank(context.Background(), "q", mkBackendItems(20))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 20 {
		t.Fatalf("expected 20 items unchanged on skip, got %d", len(out))
	}
}

// TestStaged_DefaultPrefersCEWhenWired confirms unset MEMDB_STAGED_BACKEND
// resolves to CE when both backends are available — i.e. the M12.6
// migration defaults to the cheap path without explicit opt-in.
func TestStaged_DefaultPrefersCEWhenWired(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	// MEMDB_STAGED_BACKEND deliberately unset.

	llmCalls := int32(0)
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llmCalls, 1)
		t.Errorf("LLM must NOT be called when CE is wired AND env is unset (default policy)")
	}))
	defer llmSrv.Close()

	ceSrv, _ := newCEServerWithScores(t, []float64{0.9, 0.8, 0.7, 0.6, 0.5, 0.4, 0.3, 0.2, 0.1, 0.0,
		0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0,
	})
	defer ceSrv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: ceSrv.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	s := Staged{
		Config:       LLMConfig{APIURL: llmSrv.URL, Model: "m"},
		RerankClient: client,
	}
	_, err := s.Rerank(context.Background(), "q", mkBackendItems(20))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := atomic.LoadInt32(&llmCalls); got != 0 {
		t.Fatalf("expected 0 LLM calls under default policy with CE wired, got %d", got)
	}
}

// TestStaged_ExplicitBackendBypassesEnv verifies an explicit Backend
// field overrides MEMDB_STAGED_BACKEND. Used by tests that want to
// pin behaviour deterministically.
func TestStaged_ExplicitBackendBypassesEnv(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	t.Setenv("MEMDB_STAGED_BACKEND", "ce") // would normally pick CE

	stage2 := `{"ids":["id-0","id-1","id-2"]}`
	stage3 := `{"items":[
		{"id":"id-0","justification":"yes","relevant":true},
		{"id":"id-1","justification":"yes","relevant":true},
		{"id":"id-2","justification":"yes","relevant":true}
	]}`
	llmSrv := mockStagedServer(t, []string{stage2, stage3})
	defer llmSrv.Close()

	s := Staged{
		Config:  LLMConfig{APIURL: llmSrv.URL, Model: "m"},
		Backend: LLMBackend{Config: LLMConfig{APIURL: llmSrv.URL, Model: "m"}},
	}
	out, err := s.Rerank(context.Background(), "q", mkBackendItems(20))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 20 {
		t.Fatalf("expected 20 items, got %d", len(out))
	}
	// LLM-returned shortlist puts id-0 first.
	if out[0].ID() != "id-0" {
		t.Errorf("expected id-0 first, got %s", out[0].ID())
	}
}

// newCEServerByText returns a server that scores docs by mapping the
// document text → score via the supplied table. Stable across multiple
// requests with different document subsets — the contract that matters
// for the LLM↔CE parity test (stage 3 sees a different doc subset than
// stage 2, so a positional table breaks).
func newCEServerByText(t *testing.T, scoreByText map[string]float64) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, 0, len(req.Documents))
		for i, d := range req.Documents {
			results = append(results, map[string]any{
				"index":           i,
				"relevance_score": scoreByText[d],
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	return ts, &calls
}

// TestStagedBackend_LLMvsCE_SameRanking is the parity guard required by
// the spec. With BOTH backends agreeing on the relevance ordering for a
// fixed corpus of 6 items, the final reranked top-3 must be identical
// — this is the regression net for "swap backend, keep semantics".
//
// CE scoring uses a text→score table so stage 2 (full candidate set)
// and stage 3 (shortlist subset) produce CONSISTENT scores; otherwise
// the shortlist re-rank shifts winners and breaks parity.
func TestStagedBackend_LLMvsCE_SameRanking(t *testing.T) {
	// CE mock: text→score map. id-2 highest, then id-0, then id-4.
	scoreByText := map[string]float64{
		"memory text #0": 0.7,
		"memory text #1": 0.1,
		"memory text #2": 0.9,
		"memory text #3": 0.2,
		"memory text #4": 0.5,
		"memory text #5": 0.3,
	}

	// LLM mock: matches the CE ordering so both backends agree on top-3.
	stage2 := `{"ids":["id-2","id-0","id-4"]}`
	stage3 := `{"items":[
		{"id":"id-2","justification":"y","relevant":true},
		{"id":"id-0","justification":"y","relevant":true},
		{"id":"id-4","justification":"y","relevant":true}
	]}`

	t.Setenv("MEMDB_SEARCH_STAGED", "true")

	// Run LLM backend.
	t.Setenv("MEMDB_STAGED_BACKEND", "llm")
	llmSrv := mockStagedServer(t, []string{stage2, stage3})
	defer llmSrv.Close()
	llmS := Staged{Config: LLMConfig{APIURL: llmSrv.URL, Model: "m"}}
	llmOut, err := llmS.Rerank(context.Background(), "q", mkBackendItems(6))
	if err != nil {
		t.Fatalf("llm err: %v", err)
	}

	// Run CE backend on a fresh item slice (the LLM run mutates metadata
	// in place; we want a clean comparison without anchor flags etc).
	t.Setenv("MEMDB_STAGED_BACKEND", "ce")
	ceSrv, _ := newCEServerByText(t, scoreByText)
	defer ceSrv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: ceSrv.URL, Model: "test", Timeout: 2 * time.Second}, nil)
	ceS := Staged{RerankClient: client}
	ceOut, err := ceS.Rerank(context.Background(), "q", mkBackendItems(6))
	if err != nil {
		t.Fatalf("ce err: %v", err)
	}

	// The top-3 IDs (the relevant set) must match between the two runs.
	wantTop := []string{"id-2", "id-0", "id-4"}
	for i, want := range wantTop {
		if llmOut[i].ID() != want {
			t.Errorf("llm[%d]=%s want %s", i, llmOut[i].ID(), want)
		}
		if ceOut[i].ID() != want {
			t.Errorf("ce[%d]=%s want %s", i, ceOut[i].ID(), want)
		}
	}
}

// TestStaged_PerFlightOnly1CECall measures HTTP call count for the
// pure CE backend: stage 2 + stage 3 are TWO calls today (one to score
// the full candidate set, one to rescore the shortlist with threshold).
// If we ever fold these into one (the "single POST" optimisation noted
// in the spec), this test will need updating — guard against silent
// regressions where some refactor accidentally adds a third call.
func TestStaged_PerFlightCECallCount(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	t.Setenv("MEMDB_STAGED_BACKEND", "ce")

	srv, calls := newCEServerWithScores(t, []float64{0.9, 0.8, 0.7, 0.6, 0.5, 0.4, 0.3, 0.2, 0.1, 0.05,
		0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0,
	})
	defer srv.Close()
	client := gokitrerank.New(gokitrerank.Config{URL: srv.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	s := Staged{RerankClient: client}
	if _, err := s.Rerank(context.Background(), "q", mkBackendItems(20)); err != nil {
		t.Fatalf("err: %v", err)
	}
	got := atomic.LoadInt32(calls)
	if got != 2 {
		t.Errorf("expected 2 CE HTTP calls (stage2 + stage3), got %d", got)
	}
}

// TestStagedBackendName_EnvParsing pins the env validator: only the
// three known backends are accepted; everything else collapses to "".
func TestStagedBackendName_EnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", ""},
		{"ce", "ce"},
		{"llm", "llm"},
		{"hybrid", "hybrid"},
		{"CE", ""},     // case-sensitive
		{"junk", ""},   // unknown
		{" ce", ""},    // no trim
	}
	for _, c := range cases {
		t.Setenv("MEMDB_STAGED_BACKEND", c.env)
		if got := stagedBackendName(); got != c.want {
			t.Errorf("env=%q: want %q got %q", c.env, c.want, got)
		}
	}
}

// TestStagedCEThreshold_EnvParsing pins the threshold knob.
func TestStagedCEThreshold_EnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		want float32
	}{
		{"", 0.0},
		{"0.5", 0.5},
		{"-2", -2.0},
		{"junk", 0.0},
	}
	for _, c := range cases {
		t.Setenv("MEMDB_STAGED_CE_THRESHOLD", c.env)
		if got := stagedCEThreshold(); got != c.want {
			t.Errorf("env=%q: want %v got %v", c.env, c.want, got)
		}
	}
}
