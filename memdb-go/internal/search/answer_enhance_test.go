package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newAnswerEnhanceServer returns a test server whose /v1/chat/completions
// endpoint replies with the given content wrapped in an OpenAI-compatible
// chat response. Request count is returned via the int pointer.
func newAnswerEnhanceServer(t *testing.T, content string, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			*calls++
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func itemWithRel(id, memory string, rel float64) map[string]any {
	return map[string]any{
		"id":     id,
		"memory": memory,
		"metadata": map[string]any{
			"id":         id,
			"relativity": rel,
		},
	}
}

func TestEnhance_Disabled(t *testing.T) {
	// env off → computeAnswerEnhancement returns ok=false and does NOT
	// call the LLM even if cfg.APIURL is set. Caller leaves the post-trim
	// list untouched.
	calls := 0
	ts := newAnswerEnhanceServer(t, `{"answer":"x","source_ids":[],"confidence":0.5}`, &calls)
	defer ts.Close()

	items := []map[string]any{itemWithRel("u1", "mem A", 0.9)}
	cfg := AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"}

	res := computeAnswerEnhancement(context.Background(), nil, "q", items, cfg, nil)
	if res.ok {
		t.Fatalf("expected ok=false when env off, got %+v", res)
	}
	if calls != 0 {
		t.Fatalf("expected 0 LLM calls when env off, got %d", calls)
	}

	// applyAnswerEnhancementAfterTrim is the public surface — verify it
	// short-circuits to postTrim unchanged.
	out := applyAnswerEnhancementAfterTrim(context.Background(), nil, "q", items, items, cfg, nil)
	if len(out) != 1 || out[0]["id"] != "u1" {
		t.Fatalf("expected post-trim list returned unchanged, got %v", out)
	}
}

func TestEnhance_LowConfidenceWithholdsPrepend(t *testing.T) {
	// LLM returns answered with confidence 0.3 (below floor 0.5) — synth
	// MUST NOT be prepended; the post-trim list returns unchanged.
	t.Setenv("MEMDB_SEARCH_ENHANCE", "true")
	body := `{"answer":"social worker","source_ids":["u1"],"confidence":0.3}`
	ts := newAnswerEnhanceServer(t, body, nil)
	defer ts.Close()

	items := []map[string]any{itemWithRel("u1", "Caroline works as a social worker", 0.82)}
	cfg := AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"}

	out := applyAnswerEnhancementAfterTrim(context.Background(), nil, "what is Caroline's job?", items, items, cfg, nil)
	if len(out) != 1 || out[0]["id"] != "u1" {
		t.Fatalf("expected list unchanged on low-confidence answer, got %v", out)
	}
}

func TestEnhance_NoSourcesWithholdsPrepend(t *testing.T) {
	// LLM returns answered with high confidence but empty source_ids —
	// synth MUST NOT be prepended (unsourced answer is not trustworthy
	// enough to displace real top-K candidates).
	t.Setenv("MEMDB_SEARCH_ENHANCE", "true")
	body := `{"answer":"social worker","source_ids":[],"confidence":0.9}`
	ts := newAnswerEnhanceServer(t, body, nil)
	defer ts.Close()

	items := []map[string]any{itemWithRel("u1", "Caroline works as a social worker", 0.82)}
	cfg := AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"}

	out := applyAnswerEnhancementAfterTrim(context.Background(), nil, "job?", items, items, cfg, nil)
	if len(out) != 1 || out[0]["id"] != "u1" {
		t.Fatalf("expected list unchanged on no-source answer, got %v", out)
	}
}

func TestEnhance_EmptyItems(t *testing.T) {
	ans, src, conf, hinted, _, err := EnhanceRetrievalAnswer(
		context.Background(),
		"anything",
		nil,
		AnswerEnhanceConfig{APIURL: "http://x", Model: "m"},
		nil,
	)
	if err != nil {
		t.Fatalf("expected no error for empty items, got %v", err)
	}
	if ans != answerEnhanceUnknownAnswer {
		t.Errorf("expected UNKNOWN for empty items, got %q", ans)
	}
	if src != nil || conf != 0 {
		t.Errorf("expected zero sources/confidence, got %v / %v", src, conf)
	}
	if hinted {
		t.Errorf("expected hinted=false for empty items, got true")
	}
}

func TestEnhance_BelowThreshold(t *testing.T) {
	calls := 0
	ts := newAnswerEnhanceServer(t, `{"answer":"x","source_ids":[],"confidence":0.5}`, &calls)
	defer ts.Close()

	// All items below 0.4 → should short-circuit to UNKNOWN without LLM call.
	items := []map[string]any{
		itemWithRel("a", "mem A", 0.2),
		itemWithRel("b", "mem B", 0.35),
	}
	ans, _, _, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "q", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != answerEnhanceUnknownAnswer {
		t.Errorf("expected UNKNOWN below threshold, got %q", ans)
	}
	if calls != 0 {
		t.Errorf("expected 0 LLM calls below threshold, got %d", calls)
	}
}

func TestEnhance_ParsesLLMResponse(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_ENHANCE", "true")

	body, _ := json.Marshal(AnswerEnhanceResponse{
		Answer:     "social worker",
		SourceIDs:  []string{"uuid1"},
		Confidence: 0.9,
	})
	ts := newAnswerEnhanceServer(t, string(body), nil)
	defer ts.Close()

	items := []map[string]any{
		itemWithRel("uuid1",
			"Caroline is advocating against sexual assault through her work as a social worker",
			0.82),
	}

	ans, srcs, conf, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "what is Caroline's job?", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != "social worker" {
		t.Errorf("expected 'social worker', got %q", ans)
	}
	if len(srcs) != 1 || srcs[0] != "uuid1" {
		t.Errorf("expected [uuid1], got %v", srcs)
	}
	if conf != 0.9 {
		t.Errorf("expected confidence 0.9, got %v", conf)
	}
}

func TestEnhance_HandlesMarkdownFencedResponse(t *testing.T) {
	// Simulate a model that wraps JSON in ```json ... ``` fences.
	fenced := "```json\n" +
		`{"answer":"social worker","source_ids":["uuid1"],"confidence":0.8}` +
		"\n```"
	ts := newAnswerEnhanceServer(t, fenced, nil)
	defer ts.Close()

	items := []map[string]any{itemWithRel("uuid1", "a social worker", 0.9)}
	ans, _, _, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "job?", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error on fenced response: %v", err)
	}
	if ans != "social worker" {
		t.Errorf("expected 'social worker' from fenced JSON, got %q", ans)
	}
}

func TestEnhance_UnknownOnNoMemories(t *testing.T) {
	// Model says UNKNOWN explicitly → propagate as-is, no error.
	ts := newAnswerEnhanceServer(t,
		`{"answer":"UNKNOWN","source_ids":[],"confidence":0.0}`, nil)
	defer ts.Close()

	items := []map[string]any{itemWithRel("x", "irrelevant memory", 0.9)}
	ans, _, _, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "q", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != answerEnhanceUnknownAnswer {
		t.Errorf("expected UNKNOWN, got %q", ans)
	}
}

func TestPrependEnhancedAnswer(t *testing.T) {
	items := []map[string]any{
		itemWithRel("a", "first", 0.9),
		itemWithRel("b", "second", 0.8),
		itemWithRel("c", "third", 0.7),
	}
	out := prependEnhancedAnswer(items, "social worker", []string{"a"}, 0.88, "the query")

	if len(out) != 4 {
		t.Fatalf("expected 4 items (1 synth + 3 orig), got %d", len(out))
	}
	synth := out[0]
	id, _ := synth["id"].(string)
	if !strings.HasPrefix(id, "enhanced-") || len(id) != len("enhanced-")+answerEnhanceSynthIDHexLen {
		t.Errorf("expected id 'enhanced-<12hex>', got %q", id)
	}
	if synth["memory"] != "social worker" {
		t.Errorf("expected synth memory='social worker', got %v", synth["memory"])
	}
	meta, _ := synth["metadata"].(map[string]any)
	if meta["memory_type"] != "EnhancedAnswer" {
		t.Errorf("expected memory_type=EnhancedAnswer, got %v", meta["memory_type"])
	}
	// Forensic 2026-05-02 fix #3: synth relativity is anchored at
	// top1+0.001, not a hardcoded 1.0. Top-1 here is 0.9 → expect ~0.901.
	if rel, _ := meta["relativity"].(float64); rel < 0.9 || rel > 0.91 {
		t.Errorf("expected synth relativity ≈ 0.901 (top1+epsilon), got %v", rel)
	}
	if meta["enhanced"] != true {
		t.Errorf("expected enhanced=true, got %v", meta["enhanced"])
	}
	if conf, _ := meta["confidence"].(float64); conf != 0.88 {
		t.Errorf("expected confidence=0.88, got %v", conf)
	}

	// Original items are at positions 1..3 in the same order. The legacy
	// global mutation that stamped enhanced_answer / enhanced_confidence
	// onto every other item has been REMOVED — those keys must NOT be
	// present on real items (forensic 2026-05-02 fix #3).
	for i, wantID := range []string{"a", "b", "c"} {
		if out[i+1]["id"] != wantID {
			t.Errorf("at position %d: expected id=%s, got %v", i+1, wantID, out[i+1]["id"])
		}
		m, _ := out[i+1]["metadata"].(map[string]any)
		if _, has := m["enhanced_answer"]; has {
			t.Errorf("item %s: enhanced_answer mutation must be gone, got %v", wantID, m["enhanced_answer"])
		}
		if _, has := m["enhanced_confidence"]; has {
			t.Errorf("item %s: enhanced_confidence mutation must be gone, got %v", wantID, m["enhanced_confidence"])
		}
	}

	// Synthetic id is stable for identical queries.
	out2 := prependEnhancedAnswer(nil, "x", nil, 0, "the query")
	if out2[0]["id"] != id {
		t.Errorf("expected deterministic synth id, got %v vs %v", out2[0]["id"], id)
	}
}
