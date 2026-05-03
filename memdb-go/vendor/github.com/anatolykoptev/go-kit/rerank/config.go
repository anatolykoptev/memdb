package rerank

import (
	"net/http"
	"time"
)

// cfgInternal holds the resolved configuration for a Client.
// Built from functional options (v2) or translated from Config (v1 wrapper).
type cfgInternal struct {
	url            string
	model          string
	apiKey         string
	timeout        time.Duration
	maxDocs        int
	maxCharsPerDoc int
	observer       Observer
	hc             *http.Client
	// G1 fields
	retry    RetryPolicy
	circuit  *CircuitBreaker
	fallback     Reranker // *Client OR any Reranker (Voyage/Jina/...)
	fallbackName string   // metric label for non-*Client secondaries
	// G2-client fields
	normalizeMode    NormalizeMode      // local client-side normalize (MinMax/ZScore); default None
	serverNormalize  string             // "" | "sigmoid" — sent in cohereRequest.Normalize
	queryInstruction string             // prefix prepended to query before POST
	docInstruction   string             // prefix prepended to each document before POST
	maxTokensPerDoc  int                // 0 = disabled; token-aware truncation (applied before chars)
	sourceWeights    map[string]float32 // per-source score multiplier; nil = disabled
	// G4 fields
	cache Cache // nil = disabled; caller wires Redis/LRU/sync.Map per runtime
}

// Opt is a functional option for NewClient.
type Opt func(*cfgInternal)

// defaultCfg returns a cfgInternal with sensible defaults.
// G1: retry-on-5xx is ACTIVE by default (defaultRetryPolicy). v1 callers using
// New(cfg, logger) inherit this; opt out via WithRetry(rerank.NoRetry).
func defaultCfg() *cfgInternal {
	return &cfgInternal{
		maxDocs:  defaultMaxDocs,
		observer: noopObserver{},
		hc:       &http.Client{},
		retry:    defaultRetryPolicy(),
		circuit:  nil, // off by default; opt-in via WithCircuit
		fallback:     nil, // off by default; opt-in via WithFallback
		fallbackName: "",
	}
}

// WithModel sets the model name sent in the request body.
func WithModel(model string) Opt {
	return func(c *cfgInternal) { c.model = model }
}

// WithAPIKey sets the Bearer token for hosted reranker providers (e.g. Cohere).
func WithAPIKey(key string) Opt {
	return func(c *cfgInternal) { c.apiKey = key }
}

// WithTimeout sets the per-request HTTP timeout applied via context.WithTimeout.
func WithTimeout(d time.Duration) Opt {
	return func(c *cfgInternal) { c.timeout = d }
}

// WithMaxDocs caps the number of docs sent to the server per call.
// Docs beyond the cap are preserved in original order after the reranked head.
// 0 keeps the default (50).
func WithMaxDocs(n int) Opt {
	return func(c *cfgInternal) {
		if n > 0 {
			c.maxDocs = n
		}
	}
}

// WithMaxCharsPerDoc enables rune-aware truncation of each document text.
// 0 disables truncation. Prefer WithMaxTokensPerDoc for models with explicit
// token budgets; WithMaxCharsPerDoc is kept for v1 compatibility.
func WithMaxCharsPerDoc(n int) Opt {
	return func(c *cfgInternal) { c.maxCharsPerDoc = n }
}

// WithObserver registers an Observer that receives lifecycle callbacks.
// A nil observer is ignored (noopObserver stays active).
func WithObserver(obs Observer) Opt {
	return func(c *cfgInternal) {
		if obs != nil {
			c.observer = obs
		}
	}
}

// WithHTTPClient replaces the default *http.Client with the provided one.
// Useful for injecting custom transports, TLS config, or test round-trippers.
func WithHTTPClient(hc *http.Client) Opt {
	return func(c *cfgInternal) {
		if hc != nil {
			c.hc = hc
		}
	}
}

// WithRetry configures the retry policy for transient errors (5xx HTTP status).
// The default policy retries 3 times with exponential backoff.
// Opt-out: WithRetry(rerank.NoRetry).
func WithRetry(p RetryPolicy) Opt {
	return func(c *cfgInternal) { c.retry = p }
}

// WithCircuit enables the circuit breaker with the given configuration.
// By default the circuit breaker is OFF (nil). Wiring the observer for
// OnCircuitTransition callbacks is done automatically from the client's observer.
func WithCircuit(cfg CircuitConfig) Opt {
	return func(c *cfgInternal) {
		// The transition hook needs to know the model and observer, but those
		// may not be set yet (options apply in order). The circuit is created
		// lazily in newFromInternal after all options are applied; here we store
		// a sentinel CircuitBreaker with the config so newFromInternal can wire it.
		c.circuit = &CircuitBreaker{cfg: cfg}
	}
}

// WithFallback configures a secondary Client to try if the primary fails with
// a non-4xx error. StatusFallback is returned on secondary success.
// A nil secondary is a no-op.
//
// Backward compatible: *Client implements Reranker, so existing callers
// passing *Client compile unchanged. For non-*Client secondaries (e.g.
// *VoyageRerankClient, *JinaRerankClient), the metric label defaults to
// "fallback" — override via WithFallbackName.
func WithFallback(secondary Reranker) Opt {
	return func(c *cfgInternal) {
		c.fallback = secondary
		// Auto-derive metric label for *Client secondaries; leave others at
		// "fallback" unless explicitly overridden via WithFallbackName.
		if cl, ok := secondary.(*Client); ok && cl != nil {
			c.fallbackName = cl.cfg.model
		} else if c.fallbackName == "" {
			c.fallbackName = "fallback"
		}
	}
}

// WithFallbackName overrides the secondary's metric label. Use when the
// fallback is not a *Client and the auto-derived "fallback" label would
// collide with another configuration on the same dashboard. Has no effect
// when WithFallback was given a *Client (its configured model wins).
func WithFallbackName(name string) Opt {
	return func(c *cfgInternal) {
		// Set first so a later WithFallback(*Client) call still wins for
		// *Client (auto-derive overrides this), but other Reranker impls
		// keep the explicit name.
		c.fallbackName = name
	}
}

// ── G2-client options ─────────────────────────────────────────────────────────

// WithNormalize sets the client-side score normalization mode applied after
// server scoring and before SourceWeights. Default NormalizeNone (identity).
// See NormalizeMode for available modes. Sigmoid is intentionally absent —
// use WithServerNormalize(ServerNormalizeSigmoid) for sigmoid.
func WithNormalize(mode NormalizeMode) Opt {
	return func(c *cfgInternal) { c.normalizeMode = mode }
}

// WithInstruction, WithMaxTokensPerDoc, WithSourceWeights, and
// WithServerNormalize are declared in their respective files:
// instruction.go, tokens.go, source_weights.go, server_normalize.go.

// ── G4 options ────────────────────────────────────────────────────────────────

// WithCache is declared in cache.go.
