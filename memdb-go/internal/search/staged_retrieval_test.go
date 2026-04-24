package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// mockStagedServer returns an httptest server that serves a sequence of
// chat-completion contents. The Nth POST gets contents[N-1]. If the
// request goes past len(contents), the handler reports a test failure.
// Empty content entries write status 500 (for error simulation).
func mockStagedServer(t *testing.T, contents []string) *httptest.Server {
	t.Helper()
	var n atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(n.Add(1) - 1)
		if idx >= len(contents) {
			t.Errorf("unexpected staged LLM call #%d (only %d programmed)", idx+1, len(contents))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		c := contents[idx]
		if c == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": c}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func mkItems(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{
			"id":     "id-" + strconv.Itoa(i),
			"memory": "memory text #" + strconv.Itoa(i),
		})
	}
	return out
}

func ids(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		id, _ := it["id"].(string)
		out = append(out, id)
	}
	return out
}

func TestStagedRetrieval_Disabled_ReturnsOriginal(t *testing.T) {
	// env NOT set → disabled path. Should not hit the server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called when disabled")
	}))
	defer srv.Close()

	items := mkItems(20)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != len(items) {
		t.Fatalf("expected unchanged length, got %d", len(got))
	}
	if got[0]["id"] != items[0]["id"] {
		t.Fatalf("expected unchanged order when disabled")
	}
}

func TestStagedRetrieval_EmptyInput_Unchanged(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called for empty input")
	}))
	defer srv.Close()

	got := RunStagedRetrieval(context.Background(), nil, "q", nil,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if got != nil {
		t.Fatalf("expected nil returned for nil input, got %v", got)
	}
}

func TestStagedRetrieval_BelowMinSize_Unchanged(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called below min input size")
	}))
	defer srv.Close()

	items := mkItems(stagedMinInputSize - 1) // 4
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != len(items) {
		t.Fatalf("expected unchanged length below min, got %d", len(got))
	}
}

func TestStagedRetrieval_CapsAtMaxInputSize(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	// Stage2 returns ids from the first 50 (no id-50, id-55, etc.).
	stage2Content := `{"ids":["id-0","id-1","id-2"]}`
	stage3Content := `{"items":[
		{"id":"id-0","justification":"direct","relevant":true},
		{"id":"id-1","justification":"direct","relevant":true},
		{"id":"id-2","justification":"direct","relevant":true}
	]}`
	srv := mockStagedServer(t, []string{stage2Content, stage3Content})
	defer srv.Close()

	items := mkItems(100) // > max
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	// All 100 must still be returned (demoted, not dropped).
	if len(got) != 100 {
		t.Fatalf("expected 100 items (demotion, not drop), got %d", len(got))
	}
	if got[0]["id"] != "id-0" || got[1]["id"] != "id-1" || got[2]["id"] != "id-2" {
		t.Fatalf("expected shortlisted items first, got order %v", ids(got)[:3])
	}
}

func TestStagedRetrieval_ValidShortlist_Reorders(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	// Stage 2 says id-7 is most relevant, then id-3, id-0.
	stage2 := `{"ids":["id-7","id-3","id-0"]}`
	stage3 := `{"items":[
		{"id":"id-7","justification":"perfect match","relevant":true},
		{"id":"id-3","justification":"partial match","relevant":true},
		{"id":"id-0","justification":"weaker match","relevant":true}
	]}`
	srv := mockStagedServer(t, []string{stage2, stage3})
	defer srv.Close()

	items := mkItems(20)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 20 {
		t.Fatalf("expected 20 returned, got %d", len(got))
	}
	want := []string{"id-7", "id-3", "id-0"}
	for i, w := range want {
		if got[i]["id"] != w {
			t.Fatalf("position %d: want %q, got %q", i, w, got[i]["id"])
		}
	}
	// Rest (non-shortlisted) should preserve relative order: id-1, id-2, id-4, id-5...
	// We check id-1 immediately after the shortlist of 3.
	if got[3]["id"] != "id-1" {
		t.Fatalf("expected id-1 at position 3 (first non-shortlisted), got %q", got[3]["id"])
	}
}

func TestStagedRetrieval_InventedIDs_Filtered(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	// Stage 2 returns some IDs not in input. Pipeline must tolerate
	// (reorderByIDs preserves input integrity; invented ids simply
	// don't match any row). End result: original unchanged — or just
	// reorders the matching ones.
	stage2 := `{"ids":["id-99-invented","id-2","id-ghost"]}`
	stage3 := `{"items":[
		{"id":"id-2","justification":"relevant","relevant":true}
	]}`
	srv := mockStagedServer(t, []string{stage2, stage3})
	defer srv.Close()

	items := mkItems(20)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 20 {
		t.Fatalf("expected 20 returned, got %d", len(got))
	}
	// id-2 should now be in front.
	if got[0]["id"] != "id-2" {
		t.Fatalf("expected id-2 first after stage3 filter, got %q", got[0]["id"])
	}
}

func TestStagedRetrieval_AllIrrelevant_ReturnsOriginal(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	stage2 := `{"ids":["id-0","id-1","id-2"]}`
	stage3 := `{"items":[
		{"id":"id-0","justification":"IRRELEVANT","relevant":false},
		{"id":"id-1","justification":"IRRELEVANT","relevant":false},
		{"id":"id-2","justification":"IRRELEVANT","relevant":false}
	]}`
	srv := mockStagedServer(t, []string{stage2, stage3})
	defer srv.Close()

	items := mkItems(20)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 20 {
		t.Fatalf("expected 20 returned, got %d", len(got))
	}
	// Should be original unchanged order since nothing survived stage3.
	if got[0]["id"] != "id-0" || got[1]["id"] != "id-1" {
		t.Fatalf("expected original order when all IRRELEVANT, got first=%q,%q",
			got[0]["id"], got[1]["id"])
	}
}

func TestStagedRetrieval_Stage3Error_FallsBackToStage2(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	stage2 := `{"ids":["id-5","id-9"]}`
	// Stage 3 returns 500 → falls back to stage2 shortlist.
	srv := mockStagedServer(t, []string{stage2, ""})
	defer srv.Close()

	items := mkItems(20)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 20 {
		t.Fatalf("expected 20 returned, got %d", len(got))
	}
	// Stage2 shortlist was id-5, id-9 — they should lead.
	if got[0]["id"] != "id-5" || got[1]["id"] != "id-9" {
		t.Fatalf("expected stage2 ordering on stage3 fallback, got %q,%q",
			got[0]["id"], got[1]["id"])
	}
}

func TestStagedRetrieval_Stage2Error_ReturnsOriginal(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	srv := mockStagedServer(t, []string{""}) // stage2 500
	defer srv.Close()

	items := mkItems(20)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if len(got) != 20 {
		t.Fatalf("expected 20 returned unchanged, got %d", len(got))
	}
	if got[0]["id"] != "id-0" {
		t.Fatalf("expected original order on stage2 error, got first=%q", got[0]["id"])
	}
}

func TestStagedRetrieval_MarkdownFence_Parsed(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	stage2 := "```json\n{\"ids\":[\"id-4\"]}\n```"
	stage3 := "```\n{\"items\":[{\"id\":\"id-4\",\"justification\":\"relevant\",\"relevant\":true}]}\n```"
	srv := mockStagedServer(t, []string{stage2, stage3})
	defer srv.Close()

	items := mkItems(10)
	got := RunStagedRetrieval(context.Background(), nil, "q", items,
		StagedRetrievalConfig{APIURL: srv.URL, Model: "m"})
	if got[0]["id"] != "id-4" {
		t.Fatalf("expected id-4 first (fence stripped), got %q", got[0]["id"])
	}
}

func TestReorderByIDs_PreservesRestOrder(t *testing.T) {
	items := mkItems(6)
	// Promote id-4, id-1.
	got := reorderByIDs(items, []string{"id-4", "id-1"})
	want := []string{"id-4", "id-1", "id-0", "id-2", "id-3", "id-5"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: want %d got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i]["id"] != w {
			t.Fatalf("position %d: want %q, got %q (full=%v)", i, w, got[i]["id"], ids(got))
		}
	}
}
