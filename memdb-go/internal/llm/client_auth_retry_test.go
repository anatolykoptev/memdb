package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestClient_AuthRetry_RetriesOn401ThenSucceeds asserts that a 401 on the first
// attempt is retried (not immediately stopped) and succeeds when the second
// attempt returns 200. This is the key-rotation recovery path (PF-7 / #325).
//
// RED: before the fix, classifyAttemptError returned retryStop on IsAuth(),
// so the second attempt never happened and the test would time out / fail.
func TestClient_AuthRetry_RetriesOn401ThenSucceeds(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// First call: simulate key rotation → 401.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "Invalid API key",
					"type":    "invalid_request_error",
				},
			})
			return
		}
		// Second call: key rotated, success.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	// Use a very short auth retry delay so the test is fast.
	t.Setenv("MEMDB_LLM_AUTH_RETRY_DELAY_S", "0")

	c := NewClient(srv.URL, "test-key", "test-model", nil, slog.New(slog.NewTextHandler(&devNull{}, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Chat(ctx, []map[string]string{{"role": "user", "content": "hi"}}, 100)
	if err != nil {
		t.Fatalf("Chat should succeed after auth retry, got err: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %q", resp)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 calls (1 auth fail + 1 success), got %d", got)
	}
}

// TestClient_AuthRetry_ExhaustsRetries asserts that repeated 401s eventually
// stop retrying and return an error (not infinite loop).
func TestClient_AuthRetry_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "Invalid API key",
				"type":    "invalid_request_error",
			},
		})
	}))
	defer srv.Close()

	t.Setenv("MEMDB_LLM_AUTH_RETRY_DELAY_S", "0")

	c := NewClient(srv.URL, "test-key", "test-model", nil, slog.New(slog.NewTextHandler(&devNull{}, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := c.Chat(ctx, []map[string]string{{"role": "user", "content": "hi"}}, 100)
	if err == nil {
		t.Fatal("expected error after exhausting auth retries, got nil")
	}
	// maxRetries=3, so 3 calls total (all 401).
	if got := calls.Load(); got != int32(maxRetries) {
		t.Fatalf("expected %d calls (all 401), got %d", maxRetries, got)
	}
}

// TestAuthRetryDelay_Default verifies the default delay when env is unset.
func TestAuthRetryDelay_Default(t *testing.T) {
	t.Setenv("MEMDB_LLM_AUTH_RETRY_DELAY_S", "")
	got := authRetryDelay()
	if got != authRetryDelayDefault {
		t.Fatalf("default delay: expected %v, got %v", authRetryDelayDefault, got)
	}
}

// TestAuthRetryDelay_EnvOverride verifies env override is respected.
func TestAuthRetryDelay_EnvOverride(t *testing.T) {
	t.Setenv("MEMDB_LLM_AUTH_RETRY_DELAY_S", "5")
	got := authRetryDelay()
	if got != 5*time.Second {
		t.Fatalf("env override: expected 5s, got %v", got)
	}
}

// TestAuthRetryDelay_InvalidEnvFallsBack verifies invalid env falls back to default.
func TestAuthRetryDelay_InvalidEnvFallsBack(t *testing.T) {
	t.Setenv("MEMDB_LLM_AUTH_RETRY_DELAY_S", "not-a-number")
	got := authRetryDelay()
	if got != authRetryDelayDefault {
		t.Fatalf("invalid env: expected default %v, got %v", authRetryDelayDefault, got)
	}
}

type devNull struct{}

func (d *devNull) Write(p []byte) (int, error) { return len(p), nil }

// TestClient_ReasoningContentFallback verifies that when a reasoning model
// (e.g. deepseek-v4-flash) returns an empty content field but populates
// reasoning_content, the client falls back to reasoning_content so the
// extraction pipeline receives the actual answer.
func TestClient_ReasoningContentFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{
					"role":              "assistant",
					"content":           "",
					"reasoning_content": "extracted fact here",
				}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key", "test-model", nil, slog.New(slog.NewTextHandler(&devNull{}, &slog.HandlerOptions{Level: slog.LevelError})))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Chat(ctx, []map[string]string{{"role": "user", "content": "hi"}}, 100)
	if err != nil {
		t.Fatalf("Chat should succeed, got err: %v", err)
	}
	if resp != "extracted fact here" {
		t.Fatalf("expected reasoning_content fallback 'extracted fact here', got %q", resp)
	}
}

// TestClient_ContentPreferredOverReasoning verifies that when both content
// and reasoning_content are present, content wins (reasoning_content is
// only a fallback for empty content).
func TestClient_ContentPreferredOverReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{
					"role":              "assistant",
					"content":           "primary",
					"reasoning_content": "fallback-only",
				}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key", "test-model", nil, slog.New(slog.NewTextHandler(&devNull{}, &slog.HandlerOptions{Level: slog.LevelError})))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Chat(ctx, []map[string]string{{"role": "user", "content": "hi"}}, 100)
	if err != nil {
		t.Fatalf("Chat should succeed, got err: %v", err)
	}
	if resp != "primary" {
		t.Fatalf("expected 'primary' (content wins), got %q", resp)
	}
}
