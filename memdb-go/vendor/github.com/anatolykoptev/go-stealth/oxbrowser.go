package stealth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// OxBrowserClient calls the ox-browser REST API.
type OxBrowserClient struct {
	baseURL string
	client  *http.Client
}

// NewOxBrowserClient creates a client for ox-browser at the given base URL.
// Does NOT route through proxy. Use NewOxBrowserClientWithProxy for stealth scenarios.
func NewOxBrowserClient(baseURL string) *OxBrowserClient {
	return &OxBrowserClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// NewOxBrowserClientWithProxy creates a client for ox-browser that routes
// all requests (to the ox-browser API itself) through the given proxy function.
// proxyFn is compatible with http.Transport.Proxy — pass proxyPool.TransportProxy()
// to ensure each call to ox-browser uses a fresh rotated residential IP.
func NewOxBrowserClientWithProxy(baseURL string, proxyFn func(*http.Request) (*url.URL, error)) *OxBrowserClient {
	transport := &http.Transport{Proxy: proxyFn}
	return &OxBrowserClient{
		baseURL: baseURL,
		client:  &http.Client{Transport: transport, Timeout: 60 * time.Second},
	}
}

// SolveResponse is the response from /solve.
type SolveResponse struct {
	Status  string            `json:"status"`
	Cookies map[string]string `json:"cookies"`
	Error   string            `json:"error,omitempty"`
}

// FetchSmartResponse is the response from /fetch-smart.
type FetchSmartResponse struct {
	Status     int    `json:"status"`
	Body       string `json:"body"`
	Method     string `json:"method"`
	CfDetected bool   `json:"cf_detected"`
	ElapsedMs  int64  `json:"elapsed_ms"`
	Error      string `json:"error,omitempty"`
}

// AnalyzeTech is a single detected technology.
type AnalyzeTech struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Confidence int      `json:"confidence"`
	Version    *string  `json:"version,omitempty"`
}

// AnalyzeResponse is the response from /analyze.
type AnalyzeResponse struct {
	URL          string        `json:"url"`
	Status       int           `json:"status"`
	Technologies []AnalyzeTech `json:"technologies"`
	Error        string        `json:"error,omitempty"`
}

// Solve calls ox-browser /solve to get CF clearance cookies.
func (c *OxBrowserClient) Solve(ctx context.Context, url, challengeType string) (map[string]string, error) {
	body, err := json.Marshal(map[string]string{"url": url, "challenge_type": challengeType})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	var result SolveResponse
	if err := c.post(ctx, "/solve", body, &result); err != nil {
		return nil, err
	}
	if result.Status != "ok" {
		return nil, fmt.Errorf("ox-browser solve: %s", result.Error)
	}
	return result.Cookies, nil
}

// FetchSmart calls ox-browser /fetch-smart (auto CF bypass).
func (c *OxBrowserClient) FetchSmart(ctx context.Context, url string) (*FetchSmartResponse, error) {
	body, err := json.Marshal(map[string]string{"url": url})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	var result FetchSmartResponse
	if err := c.post(ctx, "/fetch-smart", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Analyze calls ox-browser /analyze for tech detection.
func (c *OxBrowserClient) Analyze(ctx context.Context, url string) (*AnalyzeResponse, error) {
	body, err := json.Marshal(map[string]string{"url": url})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	var result AnalyzeResponse
	if err := c.post(ctx, "/analyze", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *OxBrowserClient) post(ctx context.Context, path string, body []byte, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ox-browser: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("ox-browser %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // deferred close, error unrecoverable

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ox-browser: read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("ox-browser %s: HTTP %d: %s", path, resp.StatusCode, string(respBody))
	}
	return json.Unmarshal(respBody, out)
}
