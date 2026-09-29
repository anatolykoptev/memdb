package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive Client.Chat end to end against an httptest server that
// answers per model, reproducing the prod failure modes behind #410:
//   - a 200 whose content is empty (reasoning model spent max_tokens thinking)
//   - a model that hangs until the caller's deadline
//   - a primary model that is also listed in the fallbacks

type modelServer struct {
	mu    sync.Mutex
	calls map[string]int
	reply map[string]func(w http.ResponseWriter, r *http.Request)
}

func newModelServer(t *testing.T, reply map[string]func(w http.ResponseWriter, r *http.Request)) (*modelServer, *httptest.Server) {
	t.Helper()
	ms := &modelServer{calls: map[string]int{}, reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ms.mu.Lock()
		ms.calls[body.Model]++
		ms.mu.Unlock()
		ms.reply[body.Model](w, r)
	}))
	t.Cleanup(srv.Close)
	return ms, srv
}

func (ms *modelServer) count(model string) int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.calls[model]
}

func replyContent(content string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"},
			},
		})
	}
}

func replyStatus(code int) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": http.StatusText(code)}})
	}
}

func replyHang(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
}

var chatMsgs = []map[string]string{{"role": "user", "content": "hi"}}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&devNull{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// Empty content on a 200 must fall through to the next model instead of being
// handed to the caller as a successful "" (prod: "parse llm json (): unexpected
// end of JSON input").
func TestChat_EmptyContentFallsBackToNextModel(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyContent("  \n"),
		"b": replyContent(`{"ok":true}`),
	})
	c := NewClient(srv.URL, "k", "a", []string{"b"}, quietLogger())

	got, err := c.Chat(context.Background(), chatMsgs, 100)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("want model b's content, got %q", got)
	}
	if n := ms.count("a"); n != 1 {
		t.Fatalf("empty reply is not transient: want 1 call to a, got %d", n)
	}
}

func TestChat_AllModelsEmpty_ReturnsErrEmptyContent(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyContent(""),
		"b": replyContent(""),
	})
	c := NewClient(srv.URL, "k", "a", []string{"b"}, quietLogger())

	got, err := c.Chat(context.Background(), chatMsgs, 100)
	if !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("want ErrEmptyContent, got content=%q err=%v", got, err)
	}
}

// A hanging primary must not consume the caller's whole deadline: each attempt
// gets at most half the remaining budget, and a timed-out attempt moves on to
// the next model (prod: reorganizer 45s budget vs 90s http timeout).
func TestChat_HangingModelTimesOutAndFallsBack(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyHang,
		"b": replyContent("ok"),
	})
	c := NewClient(srv.URL, "k", "a", []string{"b"}, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := c.Chat(ctx, chatMsgs, 100)
	if err != nil {
		t.Fatalf("Chat: %v (model b was never reached)", err)
	}
	if got != "ok" {
		t.Fatalf("want ok, got %q", got)
	}
	if n := ms.count("a"); n != 1 {
		t.Fatalf("a hung once; retrying the same hanging model wastes the budget: want 1 call, got %d", n)
	}
}

func replyAfter(d time.Duration, content string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(d):
		}
		replyContent(content)(w, r)
	}
}

// A slow but healthy model must keep the caller's whole budget when it is the
// last (or only) model — slicing it would turn a success into a timeout.
func TestChat_SlowHealthyOnlyModelSucceeds(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyAfter(1200*time.Millisecond, "ok"),
	})
	c := NewClient(srv.URL, "k", "a", nil, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got, err := c.Chat(ctx, chatMsgs, 100); err != nil || got != "ok" {
		t.Fatalf("slow model inside the budget must succeed: got %q err=%v", got, err)
	}
}

// With a fallback, a hung primary gets an equal share of the budget and the
// fallback gets the rest — not a geometrically shrinking remainder.
func TestChat_FallbackGetsRemainingBudget(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyHang,
		"b": replyAfter(1200*time.Millisecond, "ok"),
	})
	c := NewClient(srv.URL, "k", "a", []string{"b"}, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got, err := c.Chat(ctx, chatMsgs, 100); err != nil || got != "ok" {
		t.Fatalf("fallback must get the remaining budget: got %q err=%v", got, err)
	}
}

// Once the caller's deadline has passed, no further attempts or "retrying"
// warnings may be produced (prod: 10 retry WARNs logged in the same millisecond
// after every consolidation timeout).
func TestChat_StopsAtParentDeadline(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyStatus(http.StatusInternalServerError),
		"b": replyStatus(http.StatusInternalServerError),
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := NewClient(srv.URL, "k", "a", []string{"b"}, logger)

	// Shorter than the first backoff (2s): the deadline fires during the sleep;
	// long enough that a slow -race runner does not trip the attempt slice.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Chat(ctx, chatMsgs, 100); err == nil {
		t.Fatal("want an error when every model fails")
	}
	if n := strings.Count(buf.String(), "llm transient error, retrying"); n != 1 {
		t.Fatalf("want exactly 1 retry warning before the deadline, got %d:\n%s", n, buf.String())
	}
}

// A ctx that is already done must yield an error, never ("", nil): callers
// such as the chat handler would otherwise answer the user with nothing.
func TestChat_ExpiredContextReturnsError(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyContent("ok"),
	})
	c := NewClient(srv.URL, "k", "a", nil, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := c.Chat(ctx, chatMsgs, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got content=%q err=%v", got, err)
	}
	if n := ms.count("a"); n != 0 {
		t.Fatalf("no request may start after cancellation, got %d", n)
	}
}

// A primary model repeated in the fallback list is tried once, not twice
// (prod: MEMDB_REORG_LLM_MODEL=nv-glm-5.3 is also in MEMDB_LLM_FALLBACK_MODELS).
func TestChat_PrimaryInFallbacksIsTriedOnce(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyStatus(http.StatusTooManyRequests),
		"b": replyContent("ok"),
	})
	c := NewClient(srv.URL, "k", "a", []string{"a", "b"}, quietLogger())

	if _, err := c.Chat(context.Background(), chatMsgs, 100); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if n := ms.count("a"); n != 1 {
		t.Fatalf("want 1 call to a, got %d", n)
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// When every model answers empty, the event and profile extractors keep their
// pre-#414 behaviour: the format-reminder retry is still sent and the result is
// "nothing extracted", not an LLM error (their outcome metric stays "empty").
func TestExtractors_AllModelsEmpty_KeepEmptyOutcome(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyContent(""),
	})
	c := NewClient(srv.URL, "k", "a", nil, quietLogger())

	events, err := NewEventExtractor(c).Extract(context.Background(), longConversation, time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("event: want (empty, nil), got %v, %v", events, err)
	}
	if n := ms.count("a"); n != 2 {
		t.Fatalf("event: want first call + format-reminder retry = 2, got %d", n)
	}

	profiles, err := NewProfileExtractor(c).ExtractProfile(context.Background(), longConversation, "u", "cube")
	if err != nil || len(profiles) != 0 {
		t.Fatalf("profile: want (empty, nil), got %v, %v", profiles, err)
	}
	if n := ms.count("a"); n != 4 {
		t.Fatalf("profile: want 2 more calls (first + retry), got total %d", n)
	}
}

// longConversation clears the extractors' minimum-input guards.
var longConversation = strings.Repeat("user: I moved to San Francisco last month and started a new job.\nassistant: Congratulations on the move!\n", 8)

// When the model failed and the deadline then ends the chain (here: during its
// retry backoff), its error must survive next to ctx.Err() — callers that
// classify errors would otherwise see only a timeout.
func TestChat_DeadlineKeepsModelError(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyStatus(http.StatusInternalServerError),
	})
	c := NewClient(srv.URL, "k", "a", nil, quietLogger())

	// Shorter than the first backoff (2s): the deadline fires during the sleep.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.Chat(ctx, chatMsgs, 100)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want model a's 500 in the error chain, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded too, got %v", err)
	}
}

// Devin review #414 (1): a single hanging model under a caller deadline must
// surface context.DeadlineExceeded, not only a synthetic transport 500.
func TestChat_LastModelHang_ReportsDeadline(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyHang,
	})
	c := NewClient(srv.URL, "k", "a", nil, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := c.Chat(ctx, chatMsgs, 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// Devin review #414 (2): no configured model must be an error, never ("", nil).
func TestChat_NoModelConfigured_ReturnsError(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	c := NewClient(srv.URL, "k", "", []string{""}, quietLogger())

	got, err := c.Chat(context.Background(), chatMsgs, 100)
	if err == nil {
		t.Fatalf("want an error for an empty model list, got content=%q", got)
	}
	if n := ms.count(""); n != 0 {
		t.Fatalf("no request should be sent, got %d", n)
	}
}

// Devin review #414 (3): when the deadline ends a chain whose last reply was
// empty, the error must match the ctx error but NOT ErrEmptyContent — otherwise
// the extractors map it to "nothing found" and swallow the cancellation.
func TestDeadlineErr_EmptyReplyDoesNotMaskCancellation(t *testing.T) {
	empty := &APIError{StatusCode: http.StatusOK, Message: ErrEmptyContent.Error(), kind: kindEmptyContent}
	err := deadlineErr(empty, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if errors.Is(err, ErrEmptyContent) {
		t.Fatalf("a cancelled chain must not read as an empty reply: %v", err)
	}

	other := &APIError{StatusCode: http.StatusInternalServerError, Message: "boom"}
	err = deadlineErr(other, context.DeadlineExceeded)
	var apiErr *APIError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("non-empty model error must be kept next to the deadline, got %v", err)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Pins the Chat call site of deadlineErr (not just the helper): model a answers
// empty and the caller gives up at that moment; model b's loop sees the dead
// ctx, so Chat ends on an empty lastErr. The error must read as cancelled, not
// as an empty reply the extractors would turn into "nothing found".
func TestChat_EmptyThenCancel_ReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewClient("http://llm.invalid", "k", "a", []string{"b"}, quietLogger())
	c.httpClient = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: r,
			Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":""}}]}`))}, nil
	})}

	_, err := c.Chat(ctx, chatMsgs, 10)
	if errors.Is(err, ErrEmptyContent) || !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled without ErrEmptyContent, got %v", err)
	}
}

// A transient failure that lands after the caller's deadline must not log a
// "retrying" warning — nothing will be retried (prod noise behind #410).
func TestChat_NoRetryWarningAfterDeadline(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyHang,
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := NewClient(srv.URL, "k", "a", nil, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = c.Chat(ctx, chatMsgs, 100)
	if n := strings.Count(buf.String(), "llm transient error, retrying"); n != 0 {
		t.Fatalf("want no retry warning after the deadline, got %d:\n%s", n, buf.String())
	}
}

// Prod after the fleet llm.env switch: a 10-model chain split the caller's 45s
// equally, so the primary got ~4.5s and timed out while healthy (78
// attempt_timeout vs 24 success in 80 min). A healthy primary replying at 30%
// of the budget must succeed however long the fallback chain is.
func TestChat_LongChain_PrimaryKeepsHalfTheBudget(t *testing.T) {
	reply := map[string]func(http.ResponseWriter, *http.Request){
		"a": replyAfter(900*time.Millisecond, "ok"),
	}
	fallbacks := []string{}
	for _, m := range []string{"b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		reply[m] = replyContent("fallback-" + m)
		fallbacks = append(fallbacks, m)
	}
	ms, srv := newModelServer(t, reply)
	c := NewClient(srv.URL, "k", "a", fallbacks, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := c.Chat(ctx, chatMsgs, 100)
	if err != nil || got != "ok" {
		t.Fatalf("healthy primary must answer: got %q err=%v (fallback b calls=%d)", got, err, ms.count("b"))
	}
}

// Prod after the fleet llm.env switch: 310 "transient error, retrying" in 80 min
// from fallbacks answering 5xx — each retry + 2s/4s backoff burned budget the
// next model could have used. With a next model available, a transient 5xx
// switches at once; only the last model runs the retry ladder.
func TestChat_TransientSwitchesWhenFallbackExists(t *testing.T) {
	ms, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyStatus(http.StatusBadGateway),
		"b": replyContent("ok"),
	})
	c := NewClient(srv.URL, "k", "a", []string{"b"}, quietLogger())

	start := time.Now()
	got, err := c.Chat(context.Background(), chatMsgs, 100)
	if err != nil || got != "ok" {
		t.Fatalf("want b's answer, got %q err=%v", got, err)
	}
	if n := ms.count("a"); n != 1 {
		t.Fatalf("a transient 5xx with a fallback available must not be retried: a calls=%d", n)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("switch must not wait for backoff, took %v", d)
	}
}
