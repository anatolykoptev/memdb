//go:build livepg

// Package handlers — chat_prompt_factual_livepg_test.go: end-to-end test for
// the M7 Stream A answer_style="factual" path against a live Postgres.
//
// What this exercises (top-to-bottom):
//   1. POST /product/chat/complete with answer_style="factual" passes validation.
//   2. The chat handler considers itself "native" (search + LLM both wired).
//   3. chatSearchMemories → SearchService.Search runs against a real (empty)
//      Postgres without panicking; an empty result set is the expected path.
//   4. buildSystemPrompt routes through the factualQAPromptEN branch and the
//      assembled system message is passed to the LLM client.
//   5. The captured request body contains "SHORTEST factual phrase" — the
//      load-bearing rule-1 string from the QA prompt.
//
// Gating:
//   - build tag `livepg` keeps this file out of `go test ./...` in CI.
//   - MEMDB_LIVE_PG_DSN must be set or the test t.Skip's. Format:
//     postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_ChatComplete_FactualPrompt -count=1 -v
//
// Why a stub embedder:
//   The factual-prompt assertion does not depend on retrieval recall — empty
//   memories still produce the full template. Spinning up the real ONNX
//   embedder would add ~2GB RAM + 10s init for zero test signal.

package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// (Reuses the package-level stubEmbedder defined in add_test.go: 1024-dim zero
// vectors. Against an empty DB the search returns no memories, which is the
// expected path for this prompt-template test — we only assert the prompt
// branch routing, not retrieval recall.)

// capturedLLMRequest holds the system message captured from the stub LLM server.
type capturedLLMRequest struct {
	mu            sync.Mutex
	systemMessage string
	calls         int
}

func (c *capturedLLMRequest) record(systemMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.systemMessage = systemMsg
	c.calls++
}

func (c *capturedLLMRequest) snapshot() (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.systemMessage, c.calls
}

// newStubLLMServer returns an httptest.Server that records the system message
// from incoming chat-completion requests and replies with a fixed answer.
func newStubLLMServer(t *testing.T, captured *capturedLLMRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("stub LLM: read body: %v", err)
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
			t.Errorf("stub LLM: unmarshal body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var sysMsg string
		for _, m := range payload.Messages {
			if m.Role == "system" {
				sysMsg = m.Content
				break
			}
		}
		captured.record(sysMsg)

		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "stub answer"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// openLivePGForChat is the handlers-package twin of scheduler.openLivePG.
// Kept local to avoid cross-package _test.go imports.
func openLivePGForChat(ctx context.Context, t *testing.T, logger *slog.Logger) *db.Postgres {
	t.Helper()
	dsn := os.Getenv("MEMDB_LIVE_PG_DSN")
	if dsn == "" {
		t.Skip("MEMDB_LIVE_PG_DSN not set; skipping live-Postgres factual-prompt test")
	}
	pg, err := db.NewPostgres(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	return pg
}

func TestLivePG_ChatComplete_FactualPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForChat(ctx, t, logger)
	defer pg.Close()

	emb := &stubEmbedder{}
	svc := search.NewSearchService(pg, nil, emb, logger)
	if !svc.CanSearch() {
		t.Fatalf("search service: CanSearch() = false; embedder=%v postgres=%v", emb, pg)
	}

	captured := &capturedLLMRequest{}
	llmServer := newStubLLMServer(t, captured)
	defer llmServer.Close()

	llmClient := llm.NewClient(llmServer.URL, "", "stub-model", nil, logger)

	h := &Handler{logger: logger, postgres: pg}
	h.SetSearchService(svc)
	h.SetChatLLM(llmClient)

	body := `{"user_id":"livepg-factual-test","query":"What language does the user like?","answer_style":"factual"}`
	req := httptest.NewRequest(http.MethodPost, "/product/chat/complete", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeChatComplete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	sysMsg, calls := captured.snapshot()
	if calls == 0 {
		t.Fatal("stub LLM was never called — chat handler must have proxied; check chatCanNative wiring")
	}
	if !strings.Contains(sysMsg, "SHORTEST factual phrase") {
		t.Errorf("captured system message missing factual rule-1 string.\nsystem message:\n%s", sysMsg)
	}
	if strings.Contains(sysMsg, "Four-Step Verdict") {
		t.Errorf("captured system message contains default cloud-chat boilerplate — factual routing failed.\nsystem message:\n%s", sysMsg)
	}
}
