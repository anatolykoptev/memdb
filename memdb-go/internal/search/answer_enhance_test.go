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
	// env off → applyAnswerEnhancement returns items unchanged and does NOT
	// call the LLM even if cfg.APIURL is set.
	calls := 0
	ts := newAnswerEnhanceServer(t, `{"answer":"x","source_ids":[],"confidence":0.5}`, &calls)
	defer ts.Close()

	items := []map[string]any{itemWithRel("u1", "mem A", 0.9)}
	cfg := AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"}

	out := applyAnswerEnhancement(context.Background(), nil, "q", items, cfg)
	if len(out) != 1 || out[0]["id"] != "u1" {
		t.Fatalf("expected items unchanged when env off, got %v", out)
	}
	if calls != 0 {
		t.Fatalf("expected 0 LLM calls when env off, got %d", calls)
	}
}

func TestEnhance_EmptyItems(t *testing.T) {
	ans, src, conf, err := EnhanceRetrievalAnswer(
		context.Background(),
		"anything",
		nil,
		AnswerEnhanceConfig{APIURL: "http://x", Model: "m"},
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
	ans, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "q", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
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

	ans, srcs, conf, err := EnhanceRetrievalAnswer(
		context.Background(), "what is Caroline's job?", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
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
	ans, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "job?", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
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
	ans, _, _, err := EnhanceRetrievalAnswer(
		context.Background(), "q", items,
		AnswerEnhanceConfig{APIURL: ts.URL, Model: "m"},
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
	if meta["relativity"] != 1.0 {
		t.Errorf("expected synth relativity=1.0, got %v", meta["relativity"])
	}
	if meta["enhanced"] != true {
		t.Errorf("expected enhanced=true, got %v", meta["enhanced"])
	}
	if conf, _ := meta["confidence"].(float64); conf != 0.88 {
		t.Errorf("expected confidence=0.88, got %v", conf)
	}

	// Original items are at positions 1..3 in the same order.
	for i, wantID := range []string{"a", "b", "c"} {
		if out[i+1]["id"] != wantID {
			t.Errorf("at position %d: expected id=%s, got %v", i+1, wantID, out[i+1]["id"])
		}
		// Each original item's metadata should have enhanced_answer injected.
		m, _ := out[i+1]["metadata"].(map[string]any)
		if m["enhanced_answer"] != "social worker" {
			t.Errorf("item %s: expected enhanced_answer to be set, got %v", wantID, m["enhanced_answer"])
		}
		if ec, _ := m["enhanced_confidence"].(float64); ec != 0.88 {
			t.Errorf("item %s: expected enhanced_confidence=0.88, got %v", wantID, ec)
		}
	}

	// Synthetic id is stable for identical queries.
	out2 := prependEnhancedAnswer(nil, "x", nil, 0, "the query")
	if out2[0]["id"] != id {
		t.Errorf("expected deterministic synth id, got %v vs %v", out2[0]["id"], id)
	}
}
