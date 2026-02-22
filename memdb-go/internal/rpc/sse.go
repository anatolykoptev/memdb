package rpc

// sse.go — SSE (Server-Sent Events) writer and helpers.
//
// SSE wire format (RFC 8895 / WHATWG EventSource):
//
//	field: value\n
//	\n                ← blank line terminates an event
//
// Fields: id, event, data, retry.
// Multi-line data: repeat "data: ...\n" for each line.
//
// Design choices (2026 best practices):
//   - bufio.Scanner for line-oriented reading from upstream — never splits SSE fields mid-line
//   - Separate http.Client without Timeout for SSE connections (timeout kills long streams)
//   - X-Accel-Buffering: no — disables nginx proxy buffering
//   - Context-aware: stops cleanly when client disconnects (ctx.Done)
//   - SSEWriter: typed builder for emitting well-formed events from native Go handlers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// SSEHeaders sets the required response headers for an SSE stream.
// Must be called before WriteHeader.
func SSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable nginx/caddy proxy buffering so events reach the client immediately.
	w.Header().Set("X-Accel-Buffering", "no")
	// Allow cross-origin EventSource connections.
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// SSEWriter writes well-formed SSE events to an http.ResponseWriter.
// It flushes after every event so clients receive data immediately.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	logger  *slog.Logger
}

// NewSSEWriter creates an SSEWriter. Returns nil if the ResponseWriter
// does not support flushing (i.e., streaming is not possible).
func NewSSEWriter(w http.ResponseWriter, logger *slog.Logger) *SSEWriter {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	return &SSEWriter{w: w, flusher: flusher, logger: logger}
}

// SSEEvent represents a single SSE event.
type SSEEvent struct {
	ID    string // optional: event ID for Last-Event-ID reconnect
	Event string // optional: event type (default "message")
	Data  string // required: payload (may be multi-line)
	Retry int    // optional: reconnect delay in ms (0 = omit)
}

// Write emits a single SSE event and flushes immediately.
func (s *SSEWriter) Write(ev SSEEvent) error {
	var sb strings.Builder

	if ev.ID != "" {
		fmt.Fprintf(&sb, "id: %s\n", ev.ID)
	}
	if ev.Event != "" {
		fmt.Fprintf(&sb, "event: %s\n", ev.Event)
	}
	if ev.Retry > 0 {
		fmt.Fprintf(&sb, "retry: %d\n", ev.Retry)
	}

	// Multi-line data: each line gets its own "data:" prefix.
	lines := strings.Split(ev.Data, "\n")
	for _, line := range lines {
		fmt.Fprintf(&sb, "data: %s\n", line)
	}

	// Blank line terminates the event.
	sb.WriteString("\n")

	if _, err := io.WriteString(s.w, sb.String()); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteData is a convenience method for simple data-only events.
func (s *SSEWriter) WriteData(data string) error {
	return s.Write(SSEEvent{Data: data})
}

// WriteError emits an error event and flushes.
func (s *SSEWriter) WriteError(msg string) error {
	return s.Write(SSEEvent{Event: "error", Data: msg})
}

// WriteDone emits the terminal [DONE] event (OpenAI streaming convention).
func (s *SSEWriter) WriteDone() error {
	return s.Write(SSEEvent{Data: "[DONE]"})
}

// isSSEContentType returns true if the Content-Type indicates an SSE stream.
func isSSEContentType(ct string) bool {
	return strings.HasPrefix(ct, "text/event-stream")
}

// isSSERequest returns true if the incoming request signals SSE intent.
// Detection heuristics (in order of reliability):
//  1. Accept: text/event-stream — explicit W3C EventSource signal
//  2. Path ends with /stream — MemOS/MemDB convention for SSE endpoints
func isSSERequest(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	p := r.URL.Path
	return strings.HasSuffix(p, "/stream") || strings.HasSuffix(p, "/stream/")
}

const (
	// sseProxyScannerInitBuf is the initial scanner buffer size for SSE proxying (64 KB).
	sseProxyScannerInitBuf = 64 * 1024

	// sseProxyScannerMaxBuf is the max scanner buffer size for SSE proxying (512 KB).
	// SSE data fields can be large (e.g. streaming LLM tokens).
	sseProxyScannerMaxBuf = 512 * 1024
)

// streamSSEProxy proxies an SSE response from an upstream HTTP response to the client.
//
// Uses bufio.Scanner for line-oriented reading — guarantees SSE field boundaries
// are never split across reads (unlike raw buf.Read which can split "data: ..." mid-line).
//
// Stops cleanly when:
//   - upstream closes the connection (io.EOF)
//   - client disconnects (ctx.Done)
//   - scanner error
func (c *PythonClient) streamSSEProxy(ctx context.Context, w http.ResponseWriter, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.writeError(w, http.StatusInternalServerError, "streaming not supported by server")
		return
	}

	SSEHeaders(w)
	w.WriteHeader(resp.StatusCode)

	scanner := bufio.NewScanner(resp.Body)
	// SSE lines can be long (large JSON data fields). Set a generous buffer.
	scanner.Buffer(make([]byte, sseProxyScannerInitBuf), sseProxyScannerMaxBuf)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				c.logger.Debug("sse proxy: scanner error", slog.Any("error", err))
			}
			return
		}

		line := scanner.Text()
		// Write the line + newline (scanner strips the newline).
		fmt.Fprintf(w, "%s\n", line)

		// Flush after blank lines (event boundary) and after data lines.
		// Flushing on every line is safe and ensures minimal latency.
		flusher.Flush()
	}
}
