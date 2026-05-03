package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// voyageRerankEndpoint is the canonical Voyage AI rerank endpoint.
const voyageRerankEndpoint = "https://api.voyageai.com/v1/rerank"

// voyageDefaultModel is the default Voyage rerank model used when the
// constructor is called with an empty model string.
const voyageDefaultModel = "rerank-2.5"

// voyageDefaultTimeout is the per-request HTTP timeout applied to outgoing
// Voyage rerank calls.
const voyageDefaultTimeout = 10 * time.Second

// voyageRetryPolicy is the retry policy applied to Voyage rerank calls.
// It extends the package-default RetryableStatus with 429 (Too Many Requests)
// since Voyage signals rate-limiting via 429.
var voyageRetryPolicy = RetryPolicy{
	MaxAttempts:     3,
	BaseBackoff:     200 * time.Millisecond,
	MaxBackoff:      2 * time.Second,
	Multiplier:      2.0,
	Jitter:          0.1,
	RetryableStatus: []int{429, 500, 502, 503, 504},
}

// VoyageRerankClient calls the Voyage AI rerank API.
//
// VoyageRerankClient implements the Reranker interface and is intended to be
// composed into a Cascade alongside other rerankers. Mirrors the conventions
// of embed.VoyageClient: typed errors, retry on 429/5xx with exponential
// backoff, no panics on missing config.
type VoyageRerankClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *slog.Logger
	timeout    time.Duration
}

// NewVoyageRerankClient constructs a VoyageRerankClient.
//
// model="" defaults to "rerank-2.5". logger=nil falls back to slog.Default().
// An empty apiKey is permitted — the resulting client returns Available()=false
// and RerankWithResult returns StatusSkipped without making any HTTP call.
func NewVoyageRerankClient(apiKey, model string, logger *slog.Logger) *VoyageRerankClient {
	if model == "" {
		model = voyageDefaultModel
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &VoyageRerankClient{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: voyageDefaultTimeout},
		logger:     logger,
		timeout:    voyageDefaultTimeout,
	}
}

// voyageRerankRequest mirrors the Voyage /v1/rerank request body.
type voyageRerankRequest struct {
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	Model           string   `json:"model"`
	TopK            *int     `json:"top_k,omitempty"`
	ReturnDocuments bool     `json:"return_documents"`
	Truncation      bool     `json:"truncation"`
}

// voyageRerankResult is a single scored doc in the Voyage rerank response.
type voyageRerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// voyageRerankResponse is the full Voyage rerank response body.
type voyageRerankResponse struct {
	Object string               `json:"object"`
	Data   []voyageRerankResult `json:"data"`
	Model  string               `json:"model"`
	Usage  struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Available reports whether the client is configured to make calls.
// Returns false when the API key is empty.
func (v *VoyageRerankClient) Available() bool {
	return v != nil && v.apiKey != ""
}

// Rerank returns docs sorted by Voyage relevance score (desc). Best-effort:
// any error returns input unchanged (preserving order, Score=0, OrigRank=i).
//
// Use RerankWithResult when callers need to distinguish degraded/skipped paths.
func (v *VoyageRerankClient) Rerank(ctx context.Context, query string, docs []Doc) []Scored {
	res, _ := v.RerankWithResult(ctx, query, docs)
	if res == nil {
		return passthroughScored(docs)
	}
	return res.Scored
}

// RerankWithResult calls the Voyage rerank API and returns a typed Result.
//
// Status semantics:
//   - StatusOk        — request succeeded, scores valid, sorted desc by score.
//   - StatusDegraded  — request failed; Scored holds input order with Score=0.
//   - StatusSkipped   — apiKey empty OR docs empty; no HTTP call.
//
// Per-call options honored:
//   - WithTopN(n)     — forwarded to request body as top_k (server-side cut).
//   - WithThreshold   — currently ignored (Voyage has no native threshold).
//   - WithDryRun      — skips HTTP entirely; returns StatusSkipped passthrough.
//
// Retries 429 and 5xx with exponential backoff (3 attempts, 200ms→400ms→800ms).
// 4xx other than 429 fail fast and produce StatusDegraded.
func (v *VoyageRerankClient) RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error) {
	callCfg := rerankCallCfg{}
	for _, o := range opts {
		o(&callCfg)
	}

	model := v.modelOrDefault()

	// Skipped: empty docs, missing API key, or DryRun.
	if len(docs) == 0 || v == nil || v.apiKey == "" || callCfg.DryRun {
		return &Result{
			Scored: passthroughScored(docs),
			Status: StatusSkipped,
			Model:  model,
		}, nil
	}

	// Build request body.
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = d.Text
	}
	reqBody := voyageRerankRequest{
		Query:           query,
		Documents:       texts,
		Model:           model,
		ReturnDocuments: false,
		Truncation:      true,
	}
	if callCfg.TopN > 0 {
		topK := callCfg.TopN
		reqBody.TopK = &topK
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return v.degraded(docs, model, fmt.Errorf("voyage: marshal request: %w", err))
	}

	start := time.Now()
	resp, err := do(ctx, voyageRetryPolicy, model, noopObserver{}, func() (*voyageRerankResponse, error) {
		return v.doRequest(ctx, bodyBytes)
	})
	dur := time.Since(start)
	recordDuration(model, dur)

	if err != nil {
		recordStatus(model, "error")
		if v.logger != nil {
			v.logger.Warn("voyage rerank failed",
				slog.String("model", model),
				slog.Int("docs", len(docs)),
				slog.Any("err", err),
			)
		}
		return v.degraded(docs, model, err)
	}
	recordStatus(model, "ok")

	// Map response back to input docs. Voyage returns sorted desc by
	// relevance_score; preserve that order in Scored.
	scored := make([]Scored, 0, len(resp.Data))
	for _, r := range resp.Data {
		if r.Index < 0 || r.Index >= len(docs) {
			continue // defensive
		}
		scored = append(scored, Scored{
			Doc:      docs[r.Index],
			Score:    float32(r.RelevanceScore),
			OrigRank: r.Index,
		})
	}

	respModel := resp.Model
	if respModel == "" {
		respModel = model
	}
	v.logger.Debug("voyage rerank complete",
		slog.Int("docs_in", len(docs)),
		slog.Int("docs_out", len(scored)),
		slog.Int("tokens", resp.Usage.TotalTokens),
	)
	return &Result{
		Scored: scored,
		Status: StatusOk,
		Model:  respModel,
	}, nil
}

// doRequest performs a single HTTP round-trip and parses the response.
// Retriable failures (429/5xx) return errHTTPStatus so the shared do[T] helper
// can apply the retry policy. The caller's ctx plus v.timeout bounds the call.
func (v *VoyageRerankClient) doRequest(ctx context.Context, bodyBytes []byte) (*voyageRerankResponse, error) {
	callCtx := ctx
	if v.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, v.timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, voyageRerankEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("voyage: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, respBodyLimit))
		return nil, errHTTPStatus{Code: resp.StatusCode}
	}

	rb, err := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("voyage: read response: %w", err)
	}

	var parsed voyageRerankResponse
	if err := json.Unmarshal(rb, &parsed); err != nil {
		return nil, fmt.Errorf("voyage: unmarshal response: %w", err)
	}
	return &parsed, nil
}

// degraded returns a non-nil StatusDegraded Result paired with the error.
// Mirrors *Client.RerankWithResult: callers receive (res, res.Err).
func (v *VoyageRerankClient) degraded(docs []Doc, model string, err error) (*Result, error) {
	return &Result{
		Scored: passthroughScored(docs),
		Status: StatusDegraded,
		Model:  model,
		Err:    err,
	}, err
}

// modelOrDefault returns v.model or the package default if v.model is empty.
// Defensive against zero-value clients constructed outside the constructor.
func (v *VoyageRerankClient) modelOrDefault() string {
	if v == nil || v.model == "" {
		return voyageDefaultModel
	}
	return v.model
}

// passthroughScored returns docs wrapped in Scored with Score=0 and
// OrigRank=i. Used for skipped/degraded paths so callers see a stable shape.
func passthroughScored(docs []Doc) []Scored {
	out := make([]Scored, len(docs))
	for i, d := range docs {
		out[i] = Scored{Doc: d, OrigRank: i}
	}
	return out
}

// Compile-time interface check.
var _ Reranker = (*VoyageRerankClient)(nil)
