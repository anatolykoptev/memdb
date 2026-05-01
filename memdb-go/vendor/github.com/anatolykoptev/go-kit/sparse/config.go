package sparse

import (
	"log/slog"
	"time"
)

// Config holds all sparse-embedder configuration in one typed struct.
// Populated from environment variables by callers.
//
// Type selects the backend:
//
//   - "http" — embed-server /embed_sparse endpoint (HTTPBaseURL).
//
// Fields not relevant to the chosen Type are ignored. Only "http" is
// supported in v1; ONNX-local sparse inference is parked behind a future
// sparse/onnx subpackage.
type Config struct {
	Type        string  // "http" (only supported value in v1)
	Model       string  // SPLADE model name (default splade-v3-distilbert)
	HTTPBaseURL string  // embed-server URL for type="http"
	VocabSize   int     // 0 = default 30522 (BERT-base)
	TopK        int     // 0 = use server default (256)
	MinWeight   float32 // 0 = use server default (0.0)
}

// cfgInternal holds resolved configuration after Opt application. Built by
// NewClient via functional options OR translated from Config by v1 New().
type cfgInternal struct {
	backend string

	url       string
	model     string
	vocabSize int
	timeout   time.Duration
	topK      int
	minWeight float32

	customEmbedder SparseEmbedder

	observer Observer
	logger   *slog.Logger

	retry    RetryConfig
	circuit  *CircuitBreaker
	fallback *Client

	cache SparseCache
}

// Opt is a functional option for NewClient.
type Opt func(*cfgInternal)

// defaultCfg returns a cfgInternal with sensible defaults.
func defaultCfg() *cfgInternal {
	return &cfgInternal{
		observer: noopObserver{},
		timeout:  30 * time.Second,
		retry:    defaultRetry,
		circuit:  nil,
		fallback: nil,
	}
}

// --- Common options ---

// WithModel sets the SPLADE model name (e.g. "splade-v3-distilbert").
func WithModel(model string) Opt {
	return func(c *cfgInternal) { c.model = model }
}

// WithTimeout sets the per-request HTTP timeout. Zero leaves the default
// (30s) untouched.
func WithTimeout(d time.Duration) Opt {
	return func(c *cfgInternal) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithObserver registers a lifecycle Observer. nil-ignored.
func WithObserver(obs Observer) Opt {
	return func(c *cfgInternal) {
		if obs != nil {
			c.observer = obs
		}
	}
}

// WithLogger sets the slog.Logger. nil-ignored.
func WithLogger(l *slog.Logger) Opt {
	return func(c *cfgInternal) {
		if l != nil {
			c.logger = l
		}
	}
}

// --- Backend selectors ---

// WithBackend sets the backend type explicitly. Valid: "http".
// Mutually exclusive with WithEmbedder — if both are set, WithEmbedder
// wins.
func WithBackend(name string) Opt {
	return func(c *cfgInternal) { c.backend = name }
}

// WithEmbedder accepts a pre-built SparseEmbedder. NewClient skips backend
// factory dispatch and wires this Embedder as the inner backend. Useful
// for custom HTTP variants or in tests.
//
// Cache-key caveat: the Client's cache key is derived from the
// Client-level (top_k, min_weight, vocab_size) — NOT from the inner
// embedder's own values. If the inner embedder was constructed with
// HTTPSparseOption settings (WithTopK, WithMinWeight, WithVocabSize) AND
// you also enable WithCache, you MUST mirror those into the Client via
// WithClientTopK / WithClientMinWeight / WithClientVocabSize, otherwise
// two clients with different inner top_k values will hash to the same
// cache key and serve stale or wrong vectors. Example:
//
//	inner := NewHTTPSparseEmbedder(url, model, log, WithTopK(64))
//	client, _ := NewClient("",
//	    WithEmbedder(inner),
//	    WithCache(myCache),
//	    WithClientTopK(64), // MUST match inner's TopK
//	)
func WithEmbedder(e SparseEmbedder) Opt {
	return func(c *cfgInternal) {
		if e != nil {
			c.customEmbedder = e
		}
	}
}

// --- Backend-specific options ---

// WithClientTopK sets the per-instance top_k passed to the backend. k<=0
// is ignored (server default applies).
//
// Named WithClientTopK rather than WithTopK to disambiguate from
// HTTPSparseOption's WithTopK — they target different config layers
// (Client vs raw HTTPSparseEmbedder).
func WithClientTopK(k int) Opt {
	return func(c *cfgInternal) {
		if k > 0 {
			c.topK = k
		}
	}
}

// WithClientMinWeight sets the per-instance min_weight cutoff. w<=0 is
// ignored (server default applies).
func WithClientMinWeight(w float32) Opt {
	return func(c *cfgInternal) {
		if w > 0 {
			c.minWeight = w
		}
	}
}

// WithClientVocabSize sets the per-instance vocab size override. Used by
// VocabSize() and (future) shape validation.
func WithClientVocabSize(v int) Opt {
	return func(c *cfgInternal) {
		if v > 0 {
			c.vocabSize = v
		}
	}
}

// --- Resiliency options ---

// WithRetry overrides the retry policy for transient failures (timeouts,
// 429, 5xx). The policy flows to the HTTP backend via the v2 client
// factory (newFromInternal → WithHTTPRetry).
//
// Use NoRetry to disable retries entirely. To customise jitter, attempts,
// or delays, pass a RetryConfig:
//
//	c, _ := sparse.NewClient(
//	    "http://embed-server:8082",
//	    sparse.WithRetry(sparse.RetryConfig{
//	        MaxAttempts: 5,
//	        BaseDelay:   100 * time.Millisecond,
//	        MaxDelay:    5 * time.Second,
//	        Jitter:      0.2,
//	    }),
//	)
func WithRetry(cfg RetryConfig) Opt {
	return func(c *cfgInternal) { c.retry = cfg }
}

// NoRetry is an explicit opt-out from retries. MaxAttempts=1 means the
// initial call runs once and any failure is returned without sleeping.
var NoRetry = RetryConfig{MaxAttempts: 1}

// WithCircuit enables the circuit breaker with the given configuration.
// By default the circuit breaker is OFF.
func WithCircuit(cfg CircuitConfig) Opt {
	return func(c *cfgInternal) {
		c.circuit = &CircuitBreaker{cfg: cfg}
	}
}

// WithFallback sets a secondary *Client to try when the primary returns
// StatusDegraded with a non-4xx error. Fallback depth is capped at 1.
func WithFallback(secondary *Client) Opt {
	return func(c *cfgInternal) { c.fallback = secondary }
}
