// Package rpc implements the reverse proxy client to the Python backend.
// In Phase 1 this is a simple HTTP proxy. In later phases, swap to ConnectRPC.
package rpc

import (
	"context"
	"fmt"
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
