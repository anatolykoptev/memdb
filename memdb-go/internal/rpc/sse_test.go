package rpc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- isSSEContentType -------------------------------------------------------

func TestIsSSEContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"application/json", false},
		{"", false},
		{"text/plain", false},
	}
	for _, tc := range cases {
		got := isSSEContentType(tc.ct)
		if got != tc.want {
			t.Errorf("isSSEContentType(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}

// ---- SSEHeaders -------------------------------------------------------------

func TestSSEHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	SSEHeaders(w)

	check := func(key, want string) {
		t.Helper()
		if got := w.Header().Get(key); got != want {
			t.Errorf("header %q = %q, want %q", key, got, want)
		}
	}
	check("Content-Type", "text/event-stream")
	check("Cache-Control", "no-cache")
	check("Connection", "keep-alive")
	check("X-Accel-Buffering", "no")
}

// ---- SSEWriter --------------------------------------------------------------

func TestSSEWriter_WriteData(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)
	if sw == nil {
		t.Fatal("NewSSEWriter returned nil (httptest.ResponseRecorder should implement Flusher)")
	}

	if err := sw.WriteData("hello world"); err != nil {
		t.Fatalf("WriteData: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "data: hello world\n") {
		t.Errorf("body missing 'data: hello world\\n': %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("body must end with blank line (event boundary): %q", body)
	}
}

func TestSSEWriter_WriteEvent_AllFields(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	ev := SSEEvent{
		ID:    "42",
		Event: "memory_update",
		Data:  "payload",
		Retry: 3000,
	}
	if err := sw.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	body := w.Body.String()
	for _, want := range []string{
		"id: 42\n",
		"event: memory_update\n",
		"retry: 3000\n",
		"data: payload\n",
		"\n", // event boundary
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestSSEWriter_MultiLineData(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	if err := sw.WriteData("line1\nline2\nline3"); err != nil {
		t.Fatalf("WriteData: %v", err)
	}

	body := w.Body.String()
	for _, want := range []string{"data: line1\n", "data: line2\n", "data: line3\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestSSEWriter_WriteDone(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	if err := sw.WriteDone(); err != nil {
		t.Fatalf("WriteDone: %v", err)
	}

	if !strings.Contains(w.Body.String(), "data: [DONE]\n") {
		t.Errorf("body missing [DONE]: %q", w.Body.String())
	}
}

func TestSSEWriter_WriteError(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	if err := sw.WriteError("something went wrong"); err != nil {
		t.Fatalf("WriteError: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: error\n") {
		t.Errorf("body missing 'event: error': %q", body)
	}
	if !strings.Contains(body, "data: something went wrong\n") {
		t.Errorf("body missing error data: %q", body)
	}
}

func TestSSEWriter_NoRetryField_WhenZero(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	sw.Write(SSEEvent{Data: "test", Retry: 0})
	if strings.Contains(w.Body.String(), "retry:") {
		t.Error("retry field must be omitted when Retry=0")
	}
}

func TestSSEWriter_NoIDField_WhenEmpty(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	sw.Write(SSEEvent{Data: "test", ID: ""})
	if strings.Contains(w.Body.String(), "id:") {
		t.Error("id field must be omitted when ID is empty")
	}
}

func TestSSEWriter_NoEventField_WhenEmpty(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w, nil)

	sw.Write(SSEEvent{Data: "test", Event: ""})
	if strings.Contains(w.Body.String(), "event:") {
		t.Error("event field must be omitted when Event is empty")
	}
}

// ---- streamSSEProxy ---------------------------------------------------------

// mockFlusher wraps httptest.ResponseRecorder to track Flush calls.
type mockFlusher struct {
	*httptest.ResponseRecorder
	flushCount int
}

func (m *mockFlusher) Flush() {
	m.flushCount++
	m.ResponseRecorder.Flush()
}

func TestStreamSSEProxy_ForwardsLines(t *testing.T) {
	// Upstream SSE response body.
	sseBody := "data: event1\n\ndata: event2\n\ndata: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseBody)
	}))
	defer upstream.Close()

	// Fetch from upstream.
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("upstream get: %v", err)
	}
	defer resp.Body.Close()

	rec := &mockFlusher{ResponseRecorder: httptest.NewRecorder()}
	client := NewPythonClient(upstream.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client.streamSSEProxy(ctx, rec, resp)

	body := rec.Body.String()
	for _, want := range []string{"data: event1\n", "data: event2\n", "data: [DONE]\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("proxy body missing %q:\n%s", want, body)
		}
	}

	if rec.flushCount == 0 {
		t.Error("Flush must be called at least once during SSE proxy")
	}
}

func TestStreamSSEProxy_StopsOnContextCancel(t *testing.T) {
	// Upstream that streams slowly.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 100; i++ {
			fmt.Fprintf(w, "data: event%d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("upstream get: %v", err)
	}
	defer resp.Body.Close()

	rec := &mockFlusher{ResponseRecorder: httptest.NewRecorder()}
	client := NewPythonClient(upstream.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	client.streamSSEProxy(ctx, rec, resp)
	elapsed := time.Since(start)

	// Must stop well before the full 100*10ms = 1s stream.
	if elapsed > 500*time.Millisecond {
		t.Errorf("streamSSEProxy did not stop on ctx cancel: elapsed %v", elapsed)
	}
}

func TestStreamSSEProxy_LargeDataLines(t *testing.T) {
	// Test that large data lines (> default scanner buffer) are handled.
	largeData := strings.Repeat("x", 100*1024) // 100KB
	sseBody := fmt.Sprintf("data: %s\n\n", largeData)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseBody)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("upstream get: %v", err)
	}
	defer resp.Body.Close()

	rec := &mockFlusher{ResponseRecorder: httptest.NewRecorder()}
	client := NewPythonClient(upstream.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client.streamSSEProxy(ctx, rec, resp)

	if !strings.Contains(rec.Body.String(), largeData) {
		t.Error("large data line was truncated or lost")
	}
}

// ---- NewSSEWriter nil check -------------------------------------------------

func TestNewSSEWriter_NilWhenNoFlusher(t *testing.T) {
	// A plain ResponseWriter without Flusher.
	type noFlush struct{ http.ResponseWriter }
	w := noFlush{httptest.NewRecorder()}
	sw := NewSSEWriter(w, nil)
	if sw != nil {
		t.Error("NewSSEWriter must return nil when ResponseWriter does not implement Flusher")
	}
}
