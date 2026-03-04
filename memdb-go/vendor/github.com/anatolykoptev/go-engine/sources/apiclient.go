package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

// Default HTTP client timeout for APIClient requests.
const defaultClientTimeout = 30 * time.Second

// maxErrorBodyBytes is the maximum number of bytes read from a non-2xx response body
// when constructing an error message.
const maxErrorBodyBytes = 512

// APIClient is a thin HTTP client for JSON APIs used by source integrations.
//
// It applies authentication, optional rate limiting, and JSON encoding/decoding
// for every request. Construct with [NewAPIClient] and configure via [APIOption].
//
// APIClient is safe for concurrent use.
type APIClient struct {
	base    string
	http    *http.Client
	auth    AuthMethod
	limiter *rate.Limiter
}

// APIOption configures an [APIClient].
type APIOption func(*APIClient)

// WithHTTPClient replaces the default http.Client.
func WithHTTPClient(c *http.Client) APIOption {
	return func(a *APIClient) { a.http = c }
}

// WithAuth sets the authentication method applied to every request.
func WithAuth(m AuthMethod) APIOption {
	return func(a *APIClient) { a.auth = m }
}

// WithRateLimit sets a token-bucket rate limiter. Requests block until a token
// is available. Pass nil to disable rate limiting.
func WithRateLimit(l *rate.Limiter) APIOption {
	return func(a *APIClient) { a.limiter = l }
}

// NewAPIClient creates an APIClient targeting base (e.g. "https://api.example.com").
// By default requests use no authentication and no rate limiting.
func NewAPIClient(base string, opts ...APIOption) *APIClient {
	a := &APIClient{
		base: base,
		http: &http.Client{Timeout: defaultClientTimeout},
		auth: NoAuthMethod(),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Get sends a GET request to path with optional query parameters and
// JSON-decodes the response body into dest.
func (a *APIClient) Get(ctx context.Context, path string, params url.Values, dest any) error {
	target := a.base + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("sources: build request: %w", err)
	}
	return a.do(req, dest)
}

// Post sends a POST request to path with body JSON-encoded and
// decodes the JSON response into dest.
func (a *APIClient) Post(ctx context.Context, path string, body any, dest any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sources: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sources: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(req, dest)
}

// do applies auth, waits for rate-limit token, executes the request,
// checks the response status, and decodes the JSON body into dest.
func (a *APIClient) do(req *http.Request, dest any) error {
	a.auth.Apply(req)

	if a.limiter != nil {
		if err := a.limiter.Wait(req.Context()); err != nil {
			return fmt.Errorf("sources: rate limit: %w", err)
		}
	}

	resp, err := a.http.Do(req) //nolint:bodyclose,gosec // closed below; base URL is caller-controlled config
	if err != nil {
		return fmt.Errorf("sources: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("sources: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("sources: decode response: %w", err)
	}
	return nil
}
