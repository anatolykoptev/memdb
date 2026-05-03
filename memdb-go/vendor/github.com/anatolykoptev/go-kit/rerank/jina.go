// Package rerank — Jina AI hosted reranker.
//
// JinaRerankClient calls the https://api.jina.ai/v1/rerank endpoint and
// adapts the response to the Reranker interface used by the rest of the
// package (Cascade, fallback, etc).
//
// The implementation mirrors the VoyageRerankClient pattern in voyage.go:
// stdlib-only HTTP, a small inline retry loop on 429/503, and immediate
// fail-fast on other 4xx responses.
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// Jina-specific endpoint and defaults.
const (
	jinaEndpoint           = "https://api.jina.ai/v1/rerank"
	jinaDefaultModel       = "jina-reranker-v2-base-multilingual"
	jinaDefaultHTTPTimeout = 10 * time.Second
	// jinaRespBodyLimit bounds the response body read to guard against a
	// misbehaving upstream. Rerank responses are small JSON.
	jinaRespBodyLimit = 1 << 20 // 1 MiB

	jinaMaxAttempts    = 3
	jinaBaseBackoff    = 200 * time.Millisecond
	jinaBackoffFactor  = 2.0
)

// JinaRerankClient is an HTTP client for the Jina AI rerank API.
// Safe for concurrent use.
type JinaRerankClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *slog.Logger
	timeout    time.Duration
}

// NewJinaRerankClient constructs a client. An empty model falls back to
// "jina-reranker-v2-base-multilingual"; logger=nil falls back to
// slog.Default(). An empty apiKey produces a client whose Available()
// returns false — calls return StatusSkipped without hitting the network.
func NewJinaRerankClient(apiKey, model string, logger *slog.Logger) *JinaRerankClient {
	if model == "" {
		model = jinaDefaultModel
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &JinaRerankClient{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: jinaDefaultHTTPTimeout,
		},
		logger:  logger,
		timeout: jinaDefaultHTTPTimeout,
	}
}

// jinaRequest is the JSON body sent to https://api.jina.ai/v1/rerank.
type jinaRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n,omitempty"`
	ReturnDocuments bool     `json:"return_documents"`
}

// jinaResponse is the relevant subset of the Jina rerank response body.
type jinaResponse struct {
	Model   string `json:"model"`
	Usage   struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Results []jinaResult `json:"results"`
}

// jinaResult is one entry in the Jina results array.
type jinaResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// Available reports whether the client is configured to make calls.
// Returns false when apiKey is empty.
func (j *JinaRerankClient) Available() bool {
	return j != nil && j.apiKey != ""
}

// Rerank returns docs sorted by Jina relevance score (descending).
// Best-effort: any failure returns input order with Score=0. Use
// RerankWithResult to distinguish StatusOk / StatusDegraded / StatusSkipped.
func (j *JinaRerankClient) Rerank(ctx context.Context, query string, docs []Doc) []Scored {
	res, _ := j.RerankWithResult(ctx, query, docs)
	if res == nil {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return out
	}
	return res.Scored
}

// RerankWithResult sends the (query, docs) pair to the Jina rerank API and
// adapts the response into a typed Result.
//
// Behaviour:
//
//   - Empty docs OR an empty apiKey → StatusSkipped, no API call.
//   - WithDryRun() → StatusSkipped, no API call.
//   - WithTopN(n) → forwarded as "top_n" in the request body.
//   - 429/503 → retried with exponential backoff (3 attempts, 200/400 ms).
//   - Other 4xx → fail fast, StatusDegraded.
//   - Persistent 5xx / network error → StatusDegraded after retries.
//
// The Result.Scored slice is sorted by descending Score. Result.Status is
// always populated; Result.Err is non-nil iff Status == StatusDegraded.
func (j *JinaRerankClient) RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error) {
	// Resolve per-call options (rerankCallCfg is package-private).
	var callCfg rerankCallCfg
	for _, o := range opts {
		o(&callCfg)
	}

	pass := func() []Scored {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return out
	}

	// DryRun, no apiKey, or no docs → StatusSkipped passthrough.
	if callCfg.DryRun || j == nil || j.apiKey == "" || len(docs) == 0 {
		return &Result{
			Scored: pass(),
			Status: StatusSkipped,
			Model:  j.modelName(),
		}, nil
	}

	start := time.Now()

	// Build request body.
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = d.Text
	}
	body := jinaRequest{
		Model:           j.model,
		Query:           query,
		Documents:       texts,
		TopN:            callCfg.TopN, // 0 → omitted by `omitempty`
		ReturnDocuments: false,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return &Result{
			Scored: pass(),
			Status: StatusDegraded,
			Model:  j.model,
			Err:    fmt.Errorf("jina: marshal request: %w", err),
		}, fmt.Errorf("jina: marshal request: %w", err)
	}

	resp, err := j.callWithRetry(ctx, bodyBytes)
	recordDuration(j.model, time.Since(start))
	if err != nil {
		recordStatus(j.model, "error")
		if j.logger != nil {
			j.logger.Warn("jina rerank failed",
				slog.String("model", j.model),
				slog.Int("docs", len(docs)),
				slog.Any("err", err),
			)
		}
		return &Result{
			Scored: pass(),
			Status: StatusDegraded,
			Model:  j.model,
			Err:    err,
		}, err
	}
	recordStatus(j.model, "ok")

	// Map response → Scored. Defensive: skip out-of-range indices.
	scored := make([]Scored, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Index < 0 || r.Index >= len(docs) {
			continue
		}
		scored = append(scored, Scored{
			Doc:      docs[r.Index],
			Score:    float32(r.RelevanceScore),
			OrigRank: r.Index,
		})
	}

	// Sort by Score descending if Jina returned them out of order.
	if !sort.SliceIsSorted(scored, func(i, k int) bool {
		return scored[i].Score > scored[k].Score
	}) {
		sort.SliceStable(scored, func(i, k int) bool {
			return scored[i].Score > scored[k].Score
		})
	}

	model := j.model
	if resp.Model != "" {
		model = resp.Model
	}

	return &Result{
		Scored: scored,
		Status: StatusOk,
		Model:  model,
	}, nil
}

// modelName is a nil-safe accessor for j.model.
func (j *JinaRerankClient) modelName() string {
	if j == nil {
		return ""
	}
	return j.model
}

// jinaHTTPError is a transient/non-transient HTTP error wrapper. Its
// IsRetryable bit drives the retry loop in callWithRetry.
type jinaHTTPError struct {
	StatusCode  int
	Body        string
	IsRetryable bool
}

// Error renders the upstream status and body for log lines.
func (e *jinaHTTPError) Error() string {
	return fmt.Sprintf("jina: http %d: %s", e.StatusCode, e.Body)
}

// callWithRetry performs the POST with up to jinaMaxAttempts attempts. It
// retries only on 429/503 and network errors; non-retryable HTTP errors
// (other 4xx) return immediately.
func (j *JinaRerankClient) callWithRetry(ctx context.Context, bodyBytes []byte) (*jinaResponse, error) {
	var lastErr error
	for attempt := 0; attempt < jinaMaxAttempts; attempt++ {
		resp, err := j.doRequest(ctx, bodyBytes)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		// Non-retryable HTTP error → fail fast.
		var httpErr *jinaHTTPError
		if errors.As(err, &httpErr) && !httpErr.IsRetryable {
			return nil, err
		}

		// Last attempt: give up without sleeping.
		if attempt == jinaMaxAttempts-1 {
			break
		}

		// Exponential backoff: 200ms, 400ms.
		backoff := time.Duration(float64(jinaBaseBackoff) * pow(jinaBackoffFactor, attempt))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, lastErr
}

// pow is a tiny float64^int helper to avoid importing math for one line.
func pow(base float64, exp int) float64 {
	if exp <= 0 {
		return 1
	}
	out := base
	for i := 1; i < exp; i++ {
		out *= base
	}
	return out
}

// doRequest performs a single HTTP round-trip and parses the response.
func (j *JinaRerankClient) doRequest(ctx context.Context, bodyBytes []byte) (*jinaResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, jinaEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("jina: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+j.apiKey)

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jina: http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, jinaRespBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("jina: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable
		return nil, &jinaHTTPError{
			StatusCode:  resp.StatusCode,
			Body:        string(respBody),
			IsRetryable: retryable,
		}
	}

	var out jinaResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("jina: unmarshal response: %w", err)
	}
	return &out, nil
}

// Compile-time interface check.
var _ Reranker = (*JinaRerankClient)(nil)
