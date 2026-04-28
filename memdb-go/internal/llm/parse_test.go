package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// chatStructuredFixture wraps a test server that replies with a programmed
// sequence of (status, content) tuples. The Nth POST gets entry N-1.
type chatStructuredFixture struct {
	t        *testing.T
	calls    atomic.Int64
	requests []recordedRequest
	server   *httptest.Server
}

type recordedRequest struct {
	bodyMessages []Message
	model        string
}

type chatTurn struct {
	status  int
	content string // assistant.content (only used when status==200)
}

func newChatStructuredFixture(t *testing.T, turns []chatTurn) *chatStructuredFixture {
	t.Helper()
	f := &chatStructuredFixture{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(f.calls.Add(1) - 1)
		// Capture request payload for assertions.
		var payload struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		f.requests = append(f.requests, recordedRequest{
			bodyMessages: payload.Messages,
			model:        payload.Model,
		})
		if idx >= len(turns) {
			t.Errorf("unexpected LLM call #%d (only %d programmed)", idx+1, len(turns))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		turn := turns[idx]
		if turn.status != http.StatusOK {
			w.WriteHeader(turn.status)
			return
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": turn.content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return f
}

func (f *chatStructuredFixture) close() { f.server.Close() }

type sampleStructured struct {
	Answer string `json:"answer"`
	Score  int    `json:"score"`
}

func TestChatStructured_HappyPath(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: `{"answer":"hello","score":42}`},
	})
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_happy", []Message{
		{Role: "system", Content: "be helpful"},
		{Role: "user", Content: "q"},
	}, &got, WithMaxTokens(64))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Answer != "hello" || got.Score != 42 {
		t.Fatalf("unexpected parsed value: %+v", got)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("expected exactly 1 HTTP call, got %d", f.calls.Load())
	}
}

func TestChatStructured_FenceStripping(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"json fence", "```json\n{\"answer\":\"x\",\"score\":1}\n```"},
		{"bare fence", "```\n{\"answer\":\"x\",\"score\":1}\n```"},
		{"trailing whitespace", "```json\n{\"answer\":\"x\",\"score\":1}\n```\n  "},
		{"no fence", `{"answer":"x","score":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newChatStructuredFixture(t, []chatTurn{
				{status: http.StatusOK, content: tc.content},
			})
			defer f.close()
			c := NewSimpleClient(f.server.URL, "", "m")
			var got sampleStructured
			if err := ChatStructured(context.Background(), c, "test_fence",
				[]Message{{Role: "user", Content: "q"}}, &got); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Answer != "x" || got.Score != 1 {
				t.Fatalf("parsed wrong: %+v", got)
			}
		})
	}
}

func TestChatStructured_RetryOnceOnBadJSON(t *testing.T) {
	// First reply: not JSON. Second: valid JSON. Default maxRetries=1 → success.
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: "I am sorry, I can't comply."},
		{status: http.StatusOK, content: `{"answer":"ok","score":7}`},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_retry",
		[]Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "original question"},
		}, &got)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if got.Answer != "ok" || got.Score != 7 {
		t.Fatalf("wrong parse after retry: %+v", got)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("expected 2 calls (initial + retry), got %d", f.calls.Load())
	}
	// Verify reminder was appended to the user message on the second call.
	if len(f.requests) < 2 {
		t.Fatalf("expected at least 2 recorded requests")
	}
	second := f.requests[1]
	if len(second.bodyMessages) == 0 {
		t.Fatalf("retry call had no messages")
	}
	last := second.bodyMessages[len(second.bodyMessages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "strict JSON") {
		t.Fatalf("expected reminder in last user message, got %+v", last)
	}
}

func TestChatStructured_RetryExhaustedOnBadJSON(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: "not json"},
		{status: http.StatusOK, content: "still not json"},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_retry_fail",
		[]Message{{Role: "user", Content: "q"}}, &got)
	if err == nil {
		t.Fatalf("expected parse error after exhausting retries")
	}
	if f.calls.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", f.calls.Load())
	}
}

func TestChatStructured_RetryOn429(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusTooManyRequests},
		{status: http.StatusOK, content: `{"answer":"after_429","score":1}`},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_429",
		[]Message{{Role: "user", Content: "q"}}, &got)
	if err != nil {
		t.Fatalf("expected success on 429-then-200, got %v", err)
	}
	if got.Answer != "after_429" {
		t.Fatalf("wrong content: %+v", got)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("expected 2 calls (429 + retry), got %d", f.calls.Load())
	}
}

func TestChatStructured_429RetryStillFails(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusTooManyRequests},
		{status: http.StatusTooManyRequests},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_429_fail",
		[]Message{{Role: "user", Content: "q"}}, &got)
	if err == nil {
		t.Fatalf("expected error after both 429s")
	}
}

func TestChatStructured_NoRetryOn500(t *testing.T) {
	// Existing callsites graceful-degrade on 5xx after a single round trip.
	// ChatStructured must preserve that — exactly one HTTP call on 500.
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusInternalServerError},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_500",
		[]Message{{Role: "user", Content: "q"}}, &got)
	if err == nil {
		t.Fatalf("expected error on 500")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("expected exactly 1 call on 500 (no retry), got %d", f.calls.Load())
	}
}

func TestChatStructured_ResponseFormatJSONOpt(t *testing.T) {
	// Verify WithJSONResponseMode injects response_format into payload.
	var capturedPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"answer":"y","score":2}`}},
			},
		})
	}))
	defer srv.Close()
	c := NewSimpleClient(srv.URL, "", "m")
	var got sampleStructured
	if err := ChatStructured(context.Background(), c, "test_json_mode",
		[]Message{{Role: "user", Content: "q"}}, &got, WithJSONResponseMode()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rf, ok := capturedPayload["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Fatalf("expected response_format={type:json_object}, got %v", capturedPayload["response_format"])
	}
}

func TestChatStructured_NilTarget(t *testing.T) {
	c := NewSimpleClient("http://nope", "", "m")
	err := ChatStructured[sampleStructured](context.Background(), c, "test_nil",
		[]Message{{Role: "user", Content: "q"}}, nil)
	if err == nil {
		t.Fatalf("expected error on nil target")
	}
}

func TestChatStructured_NilClient(t *testing.T) {
	var got sampleStructured
	err := ChatStructured(context.Background(), nil, "test_nil_client",
		[]Message{{Role: "user", Content: "q"}}, &got)
	if err == nil {
		t.Fatalf("expected error on nil client")
	}
}

func TestChatText_HappyPath(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: "  hello world  "},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	got, err := ChatText(context.Background(), c, "test_text",
		[]Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ChatText returns content verbatim — no fence stripping, no trim.
	if got != "  hello world  " {
		t.Fatalf("got %q, want %q", got, "  hello world  ")
	}
}

func TestChatText_429Retry(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusTooManyRequests},
		{status: http.StatusOK, content: "second-try-content"},
	})
	defer f.close()
	c := NewSimpleClient(f.server.URL, "", "m")
	got, err := ChatText(context.Background(), c, "test_text_429",
		[]Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "second-try-content" {
		t.Fatalf("got %q", got)
	}
}

func TestChatStructured_WithTimeout(t *testing.T) {
	// Server sleeps longer than the per-call timeout: expect timeout error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"answer":"x","score":1}`}},
			},
		})
	}))
	defer srv.Close()
	c := NewSimpleClient(srv.URL, "", "m")
	var got sampleStructured
	err := ChatStructured(context.Background(), c, "test_timeout",
		[]Message{{Role: "user", Content: "q"}}, &got,
		WithTimeout(20*time.Millisecond))
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}

func TestNewSimpleClient_TrimsTrailingSlash(t *testing.T) {
	c := NewSimpleClient("http://x.example/proxy/", "key", "m")
	if c.baseURL != "http://x.example/proxy" {
		t.Fatalf("expected trimmed baseURL, got %q", c.baseURL)
	}
	if c.apiKey != "key" || c.model != "m" {
		t.Fatalf("unexpected fields: %+v", c)
	}
}
