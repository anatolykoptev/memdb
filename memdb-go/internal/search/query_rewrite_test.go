package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockRewriteServer returns an httptest server that replies with a chat
// completion whose "content" field is `content` verbatim. Status defaults
// to 200 unless a non-zero override is passed.
func mockRewriteServer(t *testing.T, content string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestQueryRewrite_Disabled_ReturnsOriginal(t *testing.T) {
	// Env var NOT set → disabled path.
	q := "what did I tell Alice about the project last week"
	res := RewriteQueryForRetrieval(context.Background(), q, time.Now().UTC().Format(time.RFC3339),
		QueryRewriteConfig{APIURL: "http://unused", Model: "m"})
	if res.Used {
		t.Fatalf("expected Used=false when env disabled, got true")
	}
	if res.Rewritten != q {
		t.Fatalf("expected rewritten==original when disabled, got %q", res.Rewritten)
	}
}

func TestQueryRewrite_ShortQuery_Skipped(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	q := "what about Alice" // 3 tokens < 5
	// Use a mock server that would fail the test if it got hit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called for short query")
	}))
	defer srv.Close()
	res := RewriteQueryForRetrieval(context.Background(), q, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if res.Used {
		t.Fatalf("expected Used=false for short query")
	}
	if res.Rewritten != q {
		t.Fatalf("expected original returned for short query, got %q", res.Rewritten)
	}
}

func TestQueryRewrite_TooLong_Skipped(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	q := strings.Repeat("word ", 200) // ~1000 chars
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called for long query")
	}))
	defer srv.Close()
	res := RewriteQueryForRetrieval(context.Background(), q, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if res.Used {
		t.Fatalf("expected Used=false for long query")
	}
	if res.Rewritten != q {
		t.Fatalf("expected original returned for long query")
	}
}

func TestQueryRewrite_ParsesValidResponse(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	orig := "what did I tell Alice about the project last week"
	rewritten := `User's message to Alice about the project during the week before 2026-04-23`
	content := `{"rewritten": "` + rewritten + `", "confidence": 0.9}`
	srv := mockRewriteServer(t, content, 0)
	defer srv.Close()

	res := RewriteQueryForRetrieval(context.Background(), orig, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if !res.Used {
		t.Fatalf("expected Used=true, got false; err=%v", res.Err)
	}
	if res.Rewritten != rewritten {
		t.Fatalf("rewritten mismatch: want %q, got %q", rewritten, res.Rewritten)
	}
	if res.Confidence != 0.9 {
		t.Fatalf("confidence mismatch: want 0.9, got %v", res.Confidence)
	}
	if res.Original != orig {
		t.Fatalf("original mismatch: want %q, got %q", orig, res.Original)
	}
}

func TestQueryRewrite_LowConfidence_Discarded(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	orig := "what did I tell Alice about the project last week"
	content := `{"rewritten": "some rewrite that does not improve recall", "confidence": 0.3}`
	srv := mockRewriteServer(t, content, 0)
	defer srv.Close()

	res := RewriteQueryForRetrieval(context.Background(), orig, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if res.Used {
		t.Fatalf("expected Used=false for low confidence")
	}
	if res.Rewritten != orig {
		t.Fatalf("expected original returned for low confidence, got %q", res.Rewritten)
	}
}

func TestQueryRewrite_HandlesMarkdownFence(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	orig := "what did I tell Alice about the project last week"
	rewritten := "User's message to Alice about project topic"
	// LLM wraps JSON in a markdown fence — llm.StripJSONFence should handle it.
	content := "```json\n{\"rewritten\": \"" + rewritten + "\", \"confidence\": 0.85}\n```"
	srv := mockRewriteServer(t, content, 0)
	defer srv.Close()

	res := RewriteQueryForRetrieval(context.Background(), orig, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if !res.Used {
		t.Fatalf("expected Used=true for fenced valid JSON, err=%v", res.Err)
	}
	if res.Rewritten != rewritten {
		t.Fatalf("rewritten mismatch after fence strip: want %q, got %q", rewritten, res.Rewritten)
	}
}

func TestQueryRewrite_MalformedJSON_FallsBackToOriginal(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	orig := "what did I tell Alice about the project last week"
	content := `this is not JSON at all { broken`
	srv := mockRewriteServer(t, content, 0)
	defer srv.Close()

	res := RewriteQueryForRetrieval(context.Background(), orig, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if res.Used {
		t.Fatalf("expected Used=false on malformed JSON")
	}
	if res.Rewritten != orig {
		t.Fatalf("expected original returned on parse failure, got %q", res.Rewritten)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil Err on malformed JSON")
	}
}

func TestQueryRewrite_HTTPError_FallsBack(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	orig := "what did I tell Alice about the project last week"
	srv := mockRewriteServer(t, "", http.StatusInternalServerError)
	defer srv.Close()

	res := RewriteQueryForRetrieval(context.Background(), orig, "2026-04-23T00:00:00Z",
		QueryRewriteConfig{APIURL: srv.URL, Model: "m"})
	if res.Used {
		t.Fatalf("expected Used=false on HTTP 500")
	}
	if res.Rewritten != orig {
		t.Fatalf("expected original returned on HTTP error, got %q", res.Rewritten)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil Err on HTTP error")
	}
}
