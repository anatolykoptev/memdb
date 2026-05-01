package sparse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// httpSparseDefaultTimeout is the default HTTP client timeout applied when
// the caller does not pass WithHTTPTimeout. 30s comfortably covers
// embed-server's measured SPLADE p95 (~460ms single-text long-doc, ~2.3s
// batch=8 long) post INT8 quantisation + pool=2 — leaves headroom for
// transient pool contention without falsely tripping retries.
const httpSparseDefaultTimeout = 30 * time.Second

// httpSparseDefaultModel is the SPLADE model name assumed when the caller
// does not pass WithModel and the env factory does not override. Today
// embed-server loads exactly one SPLADE model under this name; multi-model
// dispatch becomes the caller's responsibility once a second SPLADE
// is added.
const httpSparseDefaultModel = "splade-v3-distilbert"

// httpSparseDefaultVocabSize is the BERT-base WordPiece vocabulary size,
// which matches splade-v3-distilbert. Used as the default VocabSize() when
// the caller does not pass WithVocabSize. Configured-not-validated: the
// HTTPSparseEmbedder does not check that returned indices fit in this
// range; the value is purely informational for downstream callers
// (pgvector dim, sparsevec literal formatting).
const httpSparseDefaultVocabSize = 30522

// httpSparseRespBodyLimit caps response body reads at 8 MiB. SPLADE
// outputs are bounded by top_k (default 256), so an 8 MiB cap is two
// orders of magnitude over the realistic worst-case batched response.
const httpSparseRespBodyLimit = 8 * 1024 * 1024

// HTTPSparseEmbedder calls the embed-server /embed_sparse endpoint.
//
// Endpoint: POST /embed_sparse (TEI-convention path, no /v1/ prefix). The
// path is appended to baseURL — pass only the host (e.g.
// "http://embed-server:8082").
//
// Concurrent-safe: no mutable state beyond the http.Client which is itself
// safe for concurrent use.
type HTTPSparseEmbedder struct {
	baseURL   string
	model     string
	vocabSize int
	topK      int     // 0 = omit field; server default applies
	minWeight float32 // 0 = omit field; server default applies
	client    *http.Client
	logger    *slog.Logger
	observer  Observer    // noopObserver{} when not set
	retry     RetryConfig // initialised to defaultRetry; overridable via WithHTTPRetry
}

// HTTPSparseOption is a functional option for NewHTTPSparseEmbedder.
type HTTPSparseOption func(*HTTPSparseEmbedder)

// WithHTTPTimeout overrides the default HTTP client timeout (30s). Pass d=0
// to leave the default unchanged.
func WithHTTPTimeout(d time.Duration) HTTPSparseOption {
	return func(h *HTTPSparseEmbedder) {
		if d > 0 {
			h.client.Timeout = d
		}
	}
}

// WithTopK overrides the per-instance top_k cap on sparse entries per
// output. Pass k<=0 to omit the field — the server default (256) applies.
// Per-call overrides are not exposed in v1 to keep the API surface
// minimal; mirrors embed/'s pattern (per-instance options only).
func WithTopK(k int) HTTPSparseOption {
	return func(h *HTTPSparseEmbedder) {
		if k > 0 {
			h.topK = k
		}
	}
}

// WithMinWeight overrides the per-instance weight cutoff. Entries with
// weight <= w are dropped server-side. Pass w<=0 to omit the field — the
// server default (0.0) applies.
func WithMinWeight(w float32) HTTPSparseOption {
	return func(h *HTTPSparseEmbedder) {
		if w > 0 {
			h.minWeight = w
		}
	}
}

// WithVocabSize overrides the default BERT-base vocab size (30522). Useful
// for SPLADE variants on a different tokenizer (e.g. RoBERTa-based future
// models). Configured-not-validated.
func WithVocabSize(v int) HTTPSparseOption {
	return func(h *HTTPSparseEmbedder) {
		if v > 0 {
			h.vocabSize = v
		}
	}
}

// WithHTTPObserver registers a lifecycle Observer on the raw
// HTTPSparseEmbedder. The observer's OnRetry hook fires for each retried
// failure inside withRetry. nil-ignored. v2 callers who use NewClient get
// the observer wired automatically from WithObserver via factory.go — this
// option is for callers that hold the HTTPSparseEmbedder directly.
func WithHTTPObserver(obs Observer) HTTPSparseOption {
	return func(h *HTTPSparseEmbedder) {
		if obs != nil {
			h.observer = obs
		}
	}
}

// WithHTTPRetry overrides the default retry policy (3 attempts, 200ms→400ms
// with 10% jitter on transient failures: timeouts, 429, 5xx). Pass NoRetry
// to disable retries.
//
// v2 callers who use NewClient get the policy wired automatically from
// WithRetry via factory.go — this option is for callers that hold the
// HTTPSparseEmbedder directly.
func WithHTTPRetry(cfg RetryConfig) HTTPSparseOption {
	return func(h *HTTPSparseEmbedder) {
		h.retry = cfg
	}
}

// NewHTTPSparseEmbedder creates an HTTPSparseEmbedder pointing at baseURL.
//
// baseURL should not include /embed_sparse — it will be appended
// automatically. model="" defaults to splade-v3-distilbert.
// logger=nil falls back to slog.Default().
func NewHTTPSparseEmbedder(baseURL, model string, logger *slog.Logger, opts ...HTTPSparseOption) *HTTPSparseEmbedder {
	if model == "" {
		model = httpSparseDefaultModel
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &HTTPSparseEmbedder{
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     model,
		vocabSize: httpSparseDefaultVocabSize,
		client:    &http.Client{Timeout: httpSparseDefaultTimeout},
		logger:    logger,
		observer:  noopObserver{},
		retry:     defaultRetry,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// httpSparseRequest is the wire shape of /embed_sparse — matches the Rust
// SparseEmbeddingsRequest. omitempty on TopK / MinWeight ensures the
// server-side defaults apply when the caller does not configure overrides.
type httpSparseRequest struct {
	Input     []string `json:"input"`
	Model     string   `json:"model"`
	TopK      int      `json:"top_k,omitempty"`
	MinWeight float32  `json:"min_weight,omitempty"`
}

type httpSparseItem struct {
	Index   int       `json:"index"`
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

type httpSparseResponse struct {
	Model string           `json:"model"`
	Data  []httpSparseItem `json:"data"`
}

// modelNotConfiguredMarkers are the substrings the Rust /embed_sparse
// handler emits when SPLADE model resolution fails (resolve_splade_name
// returns 400). Detecting them lets callers branch on configuration drift
// via errors.Is(err, ErrModelNotConfigured) without parsing the message.
var modelNotConfiguredMarkers = []string{
	"no splade models configured",
	"splade model", // matches "splade model 'X' not found"
	"is required when multiple splade",
}

// isModelNotConfiguredBody reports whether the response body contains any
// of the resolve_splade_name failure markers. Case-insensitive on the
// SPLADE keyword to tolerate handler-side wording drift.
func isModelNotConfiguredBody(body string) bool {
	low := strings.ToLower(body)
	for _, m := range modelNotConfiguredMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// EmbedSparse sends texts to the remote embed-server and returns one
// SparseVector per input, in input order.
//
// Empty input returns (nil, nil) without a network call — mirrors
// embed/'s HTTPEmbedder. The Rust handler rejects empty input with 400,
// so this guard avoids a guaranteed failure round-trip.
//
// Retries transient failures (timeout, 429, 5xx) with exponential backoff
// + jitter (200ms → 400ms with ±10% jitter, max 3 attempts). With
// MaxAttempts=3 only two sleeps fire — between attempts 1→2 and 2→3 —
// so the realised delay budget is roughly 200ms + 400ms before the final
// failure. Non-retriable errors (4xx validation, unmarshal) fail fast.
//
// 4xx responses with bodies matching the Rust handler's
// resolve_splade_name failure messages are wrapped with
// ErrModelNotConfigured so callers can errors.Is them.
func (h *HTTPSparseEmbedder) EmbedSparse(ctx context.Context, texts []string) ([]SparseVector, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	start := time.Now()
	outcome := outcomeSuccess
	var termCounts []int
	defer func() {
		recordRequest("http", outcome, len(texts), termCounts, time.Since(start))
	}()

	body, err := json.Marshal(httpSparseRequest{
		Input:     texts,
		Model:     h.model,
		TopK:      h.topK,
		MinWeight: h.minWeight,
	})
	if err != nil {
		outcome = outcomeError
		return nil, fmt.Errorf("http sparse: marshal: %w", err)
	}

	url := h.baseURL + "/embed_sparse"

	respBody, err := withRetry(ctx, h.retry, "http", h.observer, func() ([]byte, int, error) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, 0, fmt.Errorf("http sparse: create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := h.client.Do(req)
		if doErr != nil {
			return nil, 0, fmt.Errorf("http sparse: request: %w", doErr)
		}
		defer resp.Body.Close() //nolint:errcheck

		rb, readErr := io.ReadAll(io.LimitReader(resp.Body, httpSparseRespBodyLimit))
		if readErr != nil {
			return nil, resp.StatusCode, fmt.Errorf("http sparse: read response: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			httpErr := &errHTTPStatus{Code: resp.StatusCode, Body: string(rb)}
			// 4xx with a resolve_splade_name marker → wrap both errors so
			// errors.As(&errHTTPStatus) AND errors.Is(ErrModelNotConfigured)
			// both succeed for downstream callers.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && isModelNotConfiguredBody(string(rb)) {
				return nil, resp.StatusCode, fmt.Errorf("http sparse: %w: %w", httpErr, ErrModelNotConfigured)
			}
			return nil, resp.StatusCode, fmt.Errorf("http sparse: %w", httpErr)
		}
		return rb, resp.StatusCode, nil
	})
	if err != nil {
		outcome = outcomeError
		return nil, err
	}

	var parsed httpSparseResponse
	if uerr := json.Unmarshal(respBody, &parsed); uerr != nil {
		outcome = outcomeError
		return nil, fmt.Errorf("http sparse: unmarshal: %w", uerr)
	}

	if len(parsed.Data) != len(texts) {
		outcome = outcomeError
		return nil, fmt.Errorf("http sparse: response length mismatch: got %d want %d",
			len(parsed.Data), len(texts))
	}

	out := make([]SparseVector, len(texts))
	termCounts = make([]int, 0, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(out) {
			outcome = outcomeError
			return nil, fmt.Errorf("http sparse: bad index %d (out of [0,%d))", item.Index, len(out))
		}
		if len(item.Indices) != len(item.Values) {
			outcome = outcomeError
			return nil, fmt.Errorf("http sparse: malformed item index=%d: indices=%d values=%d",
				item.Index, len(item.Indices), len(item.Values))
		}
		out[item.Index] = SparseVector{Indices: item.Indices, Values: item.Values}
		termCounts = append(termCounts, len(item.Indices))
	}

	h.logger.Debug("http sparse complete",
		slog.Int("texts", len(texts)),
		slog.String("model", h.model),
	)
	return out, nil
}

// EmbedSparseQuery embeds a single query string by delegating to EmbedSparse.
func (h *HTTPSparseEmbedder) EmbedSparseQuery(ctx context.Context, text string) (SparseVector, error) {
	return EmbedSparseQueryViaEmbed(ctx, h, text)
}

// VocabSize returns the configured vocabulary size (default 30522).
func (h *HTTPSparseEmbedder) VocabSize() int { return h.vocabSize }

// Model returns the configured SPLADE model name. Satisfies the optional
// modelGetter interface used by modelFromEmbedder.
func (h *HTTPSparseEmbedder) Model() string { return h.model }

// Close is a no-op for the HTTP-based embedder.
func (h *HTTPSparseEmbedder) Close() error { return nil }

// Compile-time interface check.
var _ SparseEmbedder = (*HTTPSparseEmbedder)(nil)
