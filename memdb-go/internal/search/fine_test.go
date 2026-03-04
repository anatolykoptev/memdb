package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeChatResponse builds an OpenAI-compatible chat completion JSON response.
func makeChatResponse(content string) string {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestFineFilter_KeepsRelevant(t *testing.T) {
	decisions := []filterDecision{
		{ID: "1", Keep: true},
		{ID: "2", Keep: false},
	}
	body, _ := json.Marshal(decisions)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(makeChatResponse(string(body))))
	}))
	defer srv.Close()

	memories := []map[string]any{
		{"id": "1", "memory": "relevant fact"},
		{"id": "2", "memory": "irrelevant noise"},
	}
	cfg := FineConfig{APIURL: srv.URL, Model: "test"}

	result := LLMFilter(t.Context(), "test query", memories, cfg)

	if len(result) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(result))
	}
	if extractID(result[0]) != "1" {
		t.Errorf("expected id '1', got %q", extractID(result[0]))
	}
}

func TestFineFilter_ReturnsAllOnLLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	memories := []map[string]any{
		{"id": "1", "memory": "fact one"},
		{"id": "2", "memory": "fact two"},
	}
	cfg := FineConfig{APIURL: srv.URL, Model: "test"}

	result := LLMFilter(t.Context(), "test query", memories, cfg)

	if len(result) != len(memories) {
		t.Fatalf("expected %d memories on error, got %d", len(memories), len(result))
	}
}

func TestFineFilter_ReturnsAllWhenEmpty(t *testing.T) {
	cfg := FineConfig{APIURL: "http://unused", Model: "test"}

	result := LLMFilter(t.Context(), "query", nil, cfg)
	if len(result) != 0 {
		t.Fatalf("expected 0 memories for nil input, got %d", len(result))
	}

	result = LLMFilter(t.Context(), "query", []map[string]any{}, cfg)
	if len(result) != 0 {
		t.Fatalf("expected 0 memories for empty input, got %d", len(result))
	}
}

func TestRecallHint_ReturnsQuery(t *testing.T) {
	hint := map[string]string{"query": "more about cats"}
	body, _ := json.Marshal(hint)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(makeChatResponse(string(body))))
	}))
	defer srv.Close()

	memories := []map[string]any{
		{"id": "1", "memory": "dogs are nice"},
	}
	cfg := FineConfig{APIURL: srv.URL, Model: "test"}

	result := LLMRecallHint(t.Context(), "tell me about pets", memories, cfg)
	if result != "more about cats" {
		t.Errorf("expected 'more about cats', got %q", result)
	}
}

func TestRecallHint_EmptyOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	memories := []map[string]any{
		{"id": "1", "memory": "some fact"},
	}
	cfg := FineConfig{APIURL: srv.URL, Model: "test"}

	result := LLMRecallHint(t.Context(), "query", memories, cfg)
	if result != "" {
		t.Errorf("expected empty string on error, got %q", result)
	}
}
