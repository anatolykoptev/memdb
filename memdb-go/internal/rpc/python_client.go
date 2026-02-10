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

// PythonClient proxies requests to the Python MemDB backend.
type PythonClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewPythonClient creates a new HTTP proxy client to Python backend.
func NewPythonClient(baseURL string, logger *slog.Logger) *PythonClient {
	return &PythonClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
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

	start := time.Now()
	resp, err := c.httpClient.Do(proxyReq)
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

	// Check if this is an SSE stream and handle accordingly
	contentType := resp.Header.Get("Content-Type")
	if contentType == "text/event-stream" {
		c.streamSSE(w, resp)
		return
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// streamSSE handles Server-Sent Events streaming from the Python backend.
func (c *PythonClient) streamSSE(w http.ResponseWriter, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)

	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			if err != io.EOF {
				c.logger.Error("SSE stream error", slog.Any("error", err))
			}
			return
		}
	}
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
	if resp.StatusCode >= 500 {
		return fmt.Errorf("python backend unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
