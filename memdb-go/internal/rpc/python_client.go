// Package rpc implements the reverse proxy client to the Python backend.
// In Phase 1 this is a simple HTTP proxy. In later phases, swap to ConnectRPC.
package rpc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// pythonErrStatusThreshold is the HTTP status code threshold above which the
// Python backend is considered unhealthy.
const pythonErrStatusThreshold = 500

// PythonClient proxies requests to the Python MemDB backend.
type PythonClient struct {
	baseURL    string
	httpClient *http.Client // for regular (non-streaming) requests
	sseClient  *http.Client // for SSE streams — no Timeout (streams are long-lived)
	logger     *slog.Logger
}

// sharedTransport is reused by both HTTP clients to share the connection pool.
var sharedTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 100,
	IdleConnTimeout:     90 * time.Second,
	DisableCompression:  false,
}

// NewPythonClient creates a new HTTP proxy client to Python backend.
func NewPythonClient(baseURL string, logger *slog.Logger) *PythonClient {
	return &PythonClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: sharedTransport,
		},
		// SSE client has NO Timeout — streams are terminated by ctx cancellation
		// (client disconnect or server shutdown), not by a fixed deadline.
		sseClient: &http.Client{
			Transport: sharedTransport,
		},
		logger: logger,
	}
}

// ProxyRequest forwards an HTTP request to the Python backend and streams the response back.
// This is the main method used in Phase 1 — all endpoints proxy through here.
func (c *PythonClient) ProxyRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	targetURL := c.baseURL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	c.logger.Debug("proxying request",
		slog.String("method", r.Method),
		slog.String("target", targetURL),
	)

	// Create the proxied request
	proxyReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, r.Body)
	if err != nil {
		c.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create proxy request: %v", err))
		return
	}

	// Copy headers from original request
	for key, values := range r.Header {
		for _, v := range values {
			proxyReq.Header.Add(key, v)
		}
	}

	// Choose the right HTTP client before sending.
	// SSE streams must use sseClient (no Timeout) — using httpClient would kill
	// long-running streams after 120s. We detect SSE intent from the request:
	//   1. Accept: text/event-stream — explicit client signal
	//   2. Known SSE path suffixes (/stream, /wait/stream)
	httpCli := c.httpClient
	if isSSERequest(r) {
		httpCli = c.sseClient
	}

	start := time.Now()
	resp, err := httpCli.Do(proxyReq)
	if err != nil {
		c.logger.Error("proxy request failed",
			slog.String("target", targetURL),
			slog.Any("error", err),
			slog.Duration("duration", time.Since(start)),
		)
		c.writeError(w, http.StatusBadGateway, fmt.Sprintf("backend unavailable: %v", err))
		return
	}
	defer resp.Body.Close()

	c.logger.Debug("proxy response received",
		slog.String("target", targetURL),
		slog.Int("status", resp.StatusCode),
		slog.Duration("duration", time.Since(start)),
	)

	// Copy response headers
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	// Check if this is an SSE stream and handle accordingly.
	// Use the context-aware scanner-based proxy for correct SSE framing.
	if isSSEContentType(resp.Header.Get("Content-Type")) {
		c.streamSSEProxy(ctx, w, resp)
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}


func (c *PythonClient) writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"code":%d,"message":"%s","data":null}`, code, message)
}

// HealthCheck pings the Python backend.
func (c *PythonClient) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("python backend unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= pythonErrStatusThreshold {
		return fmt.Errorf("python backend unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
