package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func mkStubItems(n int) []Item {
	out := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		s := newStub("id-"+strconv.Itoa(i), 0.5)
		s.text = "memory text #" + strconv.Itoa(i)
		out = append(out, s)
	}
	return out
}

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
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestStaged_DisabledByEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called when env-disabled")
	}))
	defer srv.Close()

	s := Staged{Config: LLMConfig{APIURL: srv.URL, Model: "m"}}
	items := mkStubItems(20)
	out, err := s.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 20 {
		t.Errorf("expected unchanged length, got %d", len(out))
	}
}

func TestStaged_BelowMinSize(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should NOT be called below min input")
	}))
	defer srv.Close()

	s := Staged{Config: LLMConfig{APIURL: srv.URL, Model: "m"}}
	out, _ := s.Rerank(context.Background(), "q", mkStubItems(stagedMinInputSize-1))
	if len(out) != stagedMinInputSize-1 {
		t.Errorf("expected unchanged length, got %d", len(out))
	}
}

func TestStaged_TwoStageReorder(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	stage2 := `{"ids":["id-5","id-2","id-9"]}`
	stage3 := `{"items":[
		{"id":"id-5","justification":"direct","relevant":true},
		{"id":"id-2","justification":"weak","relevant":false},
		{"id":"id-9","justification":"direct","relevant":true}
	]}`
	srv := mockStagedServer(t, []string{stage2, stage3})
	defer srv.Close()

	s := Staged{Config: LLMConfig{APIURL: srv.URL, Model: "m"}}
	out, _ := s.Rerank(context.Background(), "q", mkStubItems(20))
	if len(out) != 20 {
		t.Fatalf("expected 20 items kept, got %d", len(out))
	}
	// Stage 3 dropped id-2 (irrelevant) → relevantIDs = [id-5, id-9]
	if out[0].ID() != "id-5" {
		t.Errorf("expected id-5 first, got %s", out[0].ID())
	}
	if out[1].ID() != "id-9" {
		t.Errorf("expected id-9 second, got %s", out[1].ID())
	}
}

func TestStaged_HookFires(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_STAGED", "true")
	stage2 := `{"ids":["id-0","id-1"]}`
	stage3 := `{"items":[
		{"id":"id-0","justification":"yes","relevant":true},
		{"id":"id-1","justification":"no","relevant":false}
	]}`
	srv := mockStagedServer(t, []string{stage2, stage3})
	defer srv.Close()

	var stageCalls, justifiedCalls int32
	s := Staged{
		Config: LLMConfig{APIURL: srv.URL, Model: "m"},
		OnStage: func(_ context.Context, _ string, _ string) {
			atomic.AddInt32(&stageCalls, 1)
		},
		OnJustified: func(_ context.Context, _ string) {
			atomic.AddInt32(&justifiedCalls, 1)
		},
	}
	_, _ = s.Rerank(context.Background(), "q", mkStubItems(20))
	if atomic.LoadInt32(&stageCalls) < 2 {
		t.Errorf("expected 2 stage hook firings, got %d", atomic.LoadInt32(&stageCalls))
	}
	if atomic.LoadInt32(&justifiedCalls) != 2 {
		t.Errorf("expected 2 justified firings, got %d", atomic.LoadInt32(&justifiedCalls))
	}
}

func TestStaged_NameStable(t *testing.T) {
	s := Staged{}
	if s.Name() != "staged" {
		t.Errorf("Staged.Name() must be 'staged'")
	}
}
