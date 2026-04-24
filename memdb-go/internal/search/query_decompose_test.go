package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// mockCoTServer returns an httptest server that replies with a chat
// completion whose "content" field is `content` verbatim. Status defaults
// to 200 unless a non-zero override is passed.
func mockCoTServer(t *testing.T, content string, status int) *httptest.Server {
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

func TestDecomposeQuery_Disabled_ReturnsOriginal(t *testing.T) {
	// Env var NOT set → disabled path.
	q := "what did Alice say about the project and what did Bob think"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called when disabled")
	}))
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 1 || got[0] != q {
		t.Fatalf("expected single-element [q] when disabled, got %#v", got)
	}
}

func TestDecomposeQuery_ShortQuery_Skipped(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "Alice and Bob" // 3 words < 8
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called for short query")
	}))
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 1 || got[0] != q {
		t.Fatalf("expected single-element [q] for short query, got %#v", got)
	}
}

func TestDecomposeQuery_AtomicLLMResponse_ReturnsOriginal(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "what did Alice tell us about the project plan"
	// LLM returns the original as single element → atomic.
	content := `{"questions": ["what did Alice tell us about the project plan"]}`
	srv := mockCoTServer(t, content, 0)
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 1 || got[0] != q {
		t.Fatalf("expected [q] for atomic LLM response, got %#v", got)
	}
}

func TestDecomposeQuery_MultiPart_ReturnsList(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "what did Alice say about the project and what did Bob think about Carol"
	content := `{"questions": ["what did Alice say about the project", "what did Bob think about Carol"]}`
	srv := mockCoTServer(t, content, 0)
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 2 {
		t.Fatalf("expected 2 subqueries, got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "Alice") || !strings.Contains(got[1], "Bob") {
		t.Fatalf("unexpected decomposition contents: %#v", got)
	}
}

func TestDecomposeQuery_MoreThanMax_CappedAt3(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "alpha bravo charlie delta echo foxtrot golf hotel india"
	content := `{"questions": ["q1 alpha", "q2 bravo", "q3 charlie", "q4 delta", "q5 echo"]}`
	srv := mockCoTServer(t, content, 0)
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != cotMaxSubqueries {
		t.Fatalf("expected %d subqueries (capped), got %d: %#v", cotMaxSubqueries, len(got), got)
	}
	if got[0] != "q1 alpha" || got[2] != "q3 charlie" {
		t.Fatalf("cap should keep the first 3 in order, got %#v", got)
	}
}

func TestDecomposeQuery_MarkdownFence_Parsed(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "Alice told Bob about Carol and then Dave left the meeting"
	content := "```json\n{\"questions\": [\"what did Alice tell Bob about Carol\", \"why did Dave leave the meeting\"]}\n```"
	srv := mockCoTServer(t, content, 0)
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 2 {
		t.Fatalf("expected 2 subqueries after fence strip, got %d: %#v", len(got), got)
	}
}

func TestDecomposeQuery_MalformedJSON_FallsBack(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "what did Alice say about the project and Bob too"
	content := `this is not JSON at all { broken`
	srv := mockCoTServer(t, content, 0)
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 1 || got[0] != q {
		t.Fatalf("expected fallback to [q] on malformed JSON, got %#v", got)
	}
}

func TestDecomposeQuery_HTTPError_FallsBack(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "what did Alice say about the project and Bob too"
	srv := mockCoTServer(t, "", http.StatusInternalServerError)
	defer srv.Close()

	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 1 || got[0] != q {
		t.Fatalf("expected fallback to [q] on HTTP 500, got %#v", got)
	}
}

func TestDecomposeQuery_EmptyAPIURL_ReturnsOriginal(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_COT", "true")
	q := "what did Alice say about the project and what did Bob say"
	got := DecomposeQuery(context.Background(), nil, q, CoTConfig{APIURL: "", Model: "m"})
	if len(got) != 1 || got[0] != q {
		t.Fatalf("expected [q] when API URL is empty, got %#v", got)
	}
}

func TestUnionVectorResults_EmptyExtra_ReturnsPrimary(t *testing.T) {
	primary := []db.VectorSearchResult{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.5},
	}
	got := unionVectorResults(primary, nil)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("expected primary returned verbatim, got %#v", got)
	}
}

func TestUnionVectorResults_DisjointAppend(t *testing.T) {
	primary := []db.VectorSearchResult{{ID: "a", Score: 0.9}}
	extra := []db.VectorSearchResult{{ID: "b", Score: 0.5}}
	got := unionVectorResults(primary, extra)
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
	ids := map[string]float64{}
	for _, r := range got {
		ids[r.ID] = r.Score
	}
	if ids["a"] != 0.9 || ids["b"] != 0.5 {
		t.Fatalf("unexpected score map: %#v", ids)
	}
}

func TestUnionVectorResults_OverlapKeepsMaxScore(t *testing.T) {
	primary := []db.VectorSearchResult{{ID: "a", Score: 0.4}, {ID: "b", Score: 0.7}}
	extra := []db.VectorSearchResult{{ID: "a", Score: 0.9}, {ID: "b", Score: 0.1}, {ID: "c", Score: 0.5}}
	got := unionVectorResults(primary, extra)
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d: %#v", len(got), got)
	}
	scoreByID := map[string]float64{}
	for _, r := range got {
		scoreByID[r.ID] = r.Score
	}
	// a: extra(0.9) > primary(0.4) → 0.9
	// b: primary(0.7) > extra(0.1) → 0.7
	// c: only in extra → 0.5
	if scoreByID["a"] != 0.9 {
		t.Fatalf("expected a=0.9 (max), got %v", scoreByID["a"])
	}
	if scoreByID["b"] != 0.7 {
		t.Fatalf("expected b=0.7 (max), got %v", scoreByID["b"])
	}
	if scoreByID["c"] != 0.5 {
		t.Fatalf("expected c=0.5 (new), got %v", scoreByID["c"])
	}
}
