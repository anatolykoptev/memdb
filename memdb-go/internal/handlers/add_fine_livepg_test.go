//go:build livepg

// Package handlers — add_fine_livepg_test.go: end-to-end tests for the M9
// Stream 4 date-aware extract prompt against a live Postgres.
//
// Positive case: ingest a conversation that references "May 12, 2024", stub
// LLM returns a fact with `[mention 2024-05-12]`, verify the tag reached
// Postgres AND the hint reached the LLM wire.
// Negative case: non-temporal conversation, stub LLM returns a fact without
// the tag, verify our pipeline does not inject one.
//
// Build tag `livepg` keeps this out of `go test ./...`.
// MEMDB_LIVE_PG_DSN must be set or the tests t.Skip.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_FineAdd_DateAware -count=1 -v

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// stubExtractorRequest captures one /chat/completions hit from the LLM stub.
type stubExtractorRequest struct {
	mu          sync.Mutex
	userMessage string
	calls       int
}

func (s *stubExtractorRequest) record(userMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userMessage = userMsg
	s.calls++
}

func (s *stubExtractorRequest) snapshot() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userMessage, s.calls
}

// newStubExtractorServer returns an httptest.Server that records the LAST
// user message, then replies with a JSON array containing exactly one
// ExtractedFact whose `resolved_text` is the supplied factText. Confidence
// is forced to 0.95 so the parser does not drop it.
func newStubExtractorServer(t *testing.T, captured *stubExtractorRequest, factText string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("stub extractor: read body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("stub extractor: unmarshal: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var userMsg string
		for _, m := range payload.Messages {
			if m.Role == "user" {
				userMsg = m.Content
				break
			}
		}
		captured.record(userMsg)

		// Single fact response. Use raw JSON to keep test independent of any
		// future ExtractedFact field additions.
		factsJSON := fmt.Sprintf(`[{
			"reasoning": "test fact",
			"raw_text": "stub raw",
			"resolved_text": %q,
			"memory": %q,
			"type": "LongTermMemory",
			"action": "add",
			"confidence": 0.95,
			"tags": ["test","temporal"]
		}]`, factText, factText)

		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": factsJSON}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// runDateAwareLivepgScenario sets up the live-PG handler + stub LLM, ingests
// `messages`, and returns the captured user-message + the stored memory text
// for the first response item. Centralised so the two test cases stay short.
func runDateAwareLivepgScenario(t *testing.T, cubePrefix, factText string, messages []chatMessage) (userMsg, storedText string) {
	t.Helper()
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "true")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	cube := fmt.Sprintf("%s-%d", cubePrefix, time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, cube)

	captured := &stubExtractorRequest{}
	stub := newStubExtractorServer(t, captured, factText)
	defer stub.Close()

	h := &Handler{
		logger:       logger,
		postgres:     pg,
		embedder:     &stubEmbedder{},
		llmExtractor: llm.NewLLMExtractor(stub.URL, "test-key", "stub-model", nil, logger),
	}

	cubeStr := cube
	items, err := h.nativeFineAddForCube(ctx, &fullAddRequest{
		UserID:          &cubeStr,
		Mode:            strPtr(modeFine),
		WritableCubeIDs: []string{cube},
		Messages:        messages,
	}, cube)
	if err != nil {
		t.Fatalf("nativeFineAddForCube failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected ≥1 add response item, got 0")
	}

	uMsg, calls := captured.snapshot()
	if calls < 1 {
		t.Fatalf("expected ≥1 LLM call, got %d", calls)
	}
	nodes, err := h.postgres.GetMemoryByPropertyIDs(ctx, []string{items[0].MemoryID}, cube)
	if err != nil {
		t.Fatalf("GetMemoryByPropertyIDs: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatalf("no memory rows found for id=%s cube=%s", items[0].MemoryID, cube)
	}
	return uMsg, nodes[0].Text
}

func TestLivePG_FineAdd_DateAware_PropagatesMentionTag(t *testing.T) {
	userMsg, stored := runDateAwareLivepgScenario(t,
		"livepg-fine-dateaware",
		"The user met Caroline on [mention 2024-05-12] at the conference.",
		[]chatMessage{
			{Role: "user", Content: "I met Caroline on May 12, 2024 at the conference.", ChatTime: "2024-05-13T09:00:00"},
			{Role: "assistant", Content: "How did the meeting go?", ChatTime: "2024-05-13T09:00:05"},
			{Role: "user", Content: "Great — we discussed the new project together.", ChatTime: "2024-05-13T09:00:15"},
			{Role: "assistant", Content: "Sounds productive!", ChatTime: "2024-05-13T09:00:20"},
		},
	)

	// Assertion 1: the date-aware hint reached the LLM stub.
	for _, want := range []string{"[mention YYYY-MM-DD]", "<content_hints>", "Never write relative dates"} {
		if !strings.Contains(userMsg, want) {
			t.Errorf("user message missing %q\nuser_msg: %s", want, userMsg)
		}
	}
	// Assertion 2: the `[mention 2024-05-12]` tag survived storage.
	if !strings.Contains(stored, "[mention 2024-05-12]") {
		t.Errorf("stored memory text missing `[mention 2024-05-12]` tag\ngot: %q", stored)
	}
}

func TestLivePG_FineAdd_DateAware_NonTemporalNoMention(t *testing.T) {
	_, stored := runDateAwareLivepgScenario(t,
		"livepg-fine-dateaware-neg",
		"The user prefers Python over Java.",
		[]chatMessage{
			{Role: "user", Content: "I really like programming in Python.", ChatTime: "2026-04-25T10:00:00"},
			{Role: "assistant", Content: "Python is a great choice for many tasks.", ChatTime: "2026-04-25T10:00:05"},
			{Role: "user", Content: "Yes, much nicer than Java.", ChatTime: "2026-04-25T10:00:10"},
			{Role: "assistant", Content: "Each language has its own strengths.", ChatTime: "2026-04-25T10:00:15"},
		},
	)

	// The hint guides the LLM but does not POST-process — non-temporal facts
	// must store cleanly (no spurious `[mention …]` injected by our pipeline).
	if strings.Contains(stored, "[mention") {
		t.Errorf("non-temporal stored memory should not contain `[mention …]` tag\ngot: %q", stored)
	}
}
