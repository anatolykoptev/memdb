package rerank

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// NewClient is the v2 constructor. Use functional options to configure.
//
// Example:
//
//	c := rerank.NewClient("http://embed:8082",
//	    rerank.WithModel("bge-reranker-v2-m3"),
//	    rerank.WithTimeout(2*time.Second))
func NewClient(url string, opts ...Opt) *Client {
	cfg := defaultCfg()
	cfg.url = url
	for _, opt := range opts {
		opt(cfg)
	}
	// Bearer token: explicit WithAPIKey > env (EMBED_TOKEN). For self-hosted
	// embed-server. Hosted providers (Cohere, Voyage) should pass WithAPIKey
	// explicitly; this env fallback is a convenience for the cross-host
	// bearer flow where the env is set per-process.
	if cfg.apiKey == "" {
		if tok := os.Getenv("EMBED_TOKEN"); tok != "" {
			cfg.apiKey = tok
		}
	}
	// Log at construction time when no token is configured and auth is not
	// required, so misconfiguration is observable. With WithRequireAuth the
	// check (and ErrNoToken) is deferred to the first call because NewClient
	// returns *Client, not (*Client, error).
	if strings.TrimSpace(cfg.apiKey) == "" && !cfg.requireAuth {
		slog.Info("rerank: no auth token configured — assuming self-hosted backend without auth")
	}
	return newFromInternal(cfg)
}

// newFromInternal builds a *Client from an already-resolved cfgInternal.
// Used by both NewClient (v2) and the v1 New() wrapper after option translation.
// G1: finalises the CircuitBreaker wiring (model + observer hook) now that all
// options have been applied.
func newFromInternal(cfg *cfgInternal) *Client {
	// Wire circuit breaker: if WithCircuit set a sentinel CB, rebuild it with
	// the final model name and observer so the transition hook works.
	if cfg.circuit != nil {
		cbCfg := cfg.circuit.cfg
		cb := NewCircuitBreaker(cbCfg, makeCircuitHook(cfg.model, cfg.observer))
		cb.model = cfg.model
		cfg.circuit = cb
	}
	return &Client{cfg: cfg}
}

// rerankCallCfg holds per-call options passed to RerankWithResult (G2-client).
// Zero value disables all per-call overrides.
type rerankCallCfg struct {
	TopN      int     // 0 = no cap
	Threshold float32 // 0 = no filter
	DryRun    bool    // skip HTTP entirely, return StatusSkipped passthrough
}

// RerankOpt is a per-call option for RerankWithResult.
type RerankOpt func(*rerankCallCfg)

// WithTopN limits the number of returned docs to the n highest-scoring after
// the full pipeline (normalize → weight → sort). 0 disables the cap.
func WithTopN(n int) RerankOpt {
	return func(c *rerankCallCfg) { c.TopN = n }
}

// WithThreshold drops docs whose post-pipeline score is strictly below t.
// Applied after sort, so the retained docs remain in descending score order.
// 0 disables filtering.
func WithThreshold(t float32) RerankOpt {
	return func(c *rerankCallCfg) { c.Threshold = t }
}

// WithDryRun skips the HTTP call entirely. The result has StatusSkipped and
// Scored contains the input docs in their original order with Score=0.
// Useful for testing pipeline wiring without a live server.
func WithDryRun() RerankOpt {
	return func(c *rerankCallCfg) { c.DryRun = true }
}

// RerankWithResult is the v2 Rerank API. Returns a typed Result with Status
// so callers can distinguish failure modes:
//   - StatusOk       — request succeeded, scores valid
//   - StatusDegraded — request failed, Scored contains input order Score=0
//   - StatusFallback — primary failed, secondary succeeded (G1+)
//   - StatusSkipped  — no URL configured or docs slice is empty
//
// G1: if a fallback client is configured, it is tried on StatusDegraded with
// a non-4xx error.
// G2-client: per-call opts (WithTopN, WithThreshold, WithDryRun) are forwarded
// through the pipeline and to fallback clients.
func (c *Client) RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error) {
	var res *Result
	if c.cfg.fallback != nil {
		res = rerankWithFallback(ctx, c, c.cfg.fallback, c.cfg.fallbackName, query, docs, opts...)
	} else {
		res = c.rerankInternal(ctx, query, docs, opts...)
	}

	if res.Status == StatusDegraded {
		return res, res.Err
	}
	return res, nil
}
