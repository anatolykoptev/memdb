package embed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// NewClient is the v2 entry point — returns a *Client configured via
// functional options. v1 callers continue to use New(cfg, logger) which
// calls the per-backend helpers directly.
//
// url is the backend URL when applicable. For Ollama/HTTP backends, pass the
// base URL. For Voyage, url is ignored (endpoint is hardcoded by the API).
// For ONNX, use the embed/onnx subpackage directly.
//
// At least one backend-specific Opt must be applied; otherwise NewClient
// returns an error from the underlying constructor.
//
// The returned *Client implements Embedder, so it is assignable to an Embedder
// variable for v1-style callers. Cast to *Client to access EmbedWithResult.
func NewClient(url string, opts ...Opt) (*Client, error) {
	cfg := defaultCfg()
	cfg.url = url
	for _, opt := range opts {
		opt(cfg)
	}
	return newClientFromInternal(cfg)
}

// newClientFromInternal builds a *Client from an already-resolved cfgInternal.
// Used by both NewClient (v2) and the v1 New() wrapper after option translation.
// E1: finalises the CircuitBreaker wiring (model + observer hook) now that all
// options have been applied.
func newClientFromInternal(cfg *cfgInternal) (*Client, error) {
	// Bearer token: env (EMBED_TOKEN) auto-resolution must happen BEFORE
	// backend construction so HTTPEmbedder picks it up via the chained
	// HTTPOption in factory.go newFromInternal. Empty env = no header.
	if cfg.httpBearerToken == "" {
		if tok := os.Getenv("EMBED_TOKEN"); tok != "" {
			cfg.httpBearerToken = tok
		}
	}

	inner, err := newFromInternal(cfg)
	if err != nil {
		return nil, err
	}
	model := modelFromEmbedder(inner)

	// E1: wire circuit breaker. If WithCircuit set a sentinel CB, rebuild it
	// with the final model name and observer so the transition hook works.
	var cb *CircuitBreaker
	if cfg.circuit != nil {
		cbCfg := cfg.circuit.cfg
		cb = NewCircuitBreaker(cbCfg, model, makeCircuitHook(model, cfg.observer))
	}

	// E5: resolve chunkSize. Priority: explicit opt > env > default.
	chunkSz := cfg.chunkSize
	if chunkSz <= 0 {
		// Try environment override.
		if raw := os.Getenv("GOKIT_EMBED_CHUNK_SIZE"); raw != "" {
			if n, parseErr := strconv.Atoi(raw); parseErr != nil || n <= 0 {
				slog.Default().Warn("GOKIT_EMBED_CHUNK_SIZE: invalid value, falling back to default",
					slog.String("value", raw),
					slog.Int("default", defaultChunkSize),
				)
			} else {
				chunkSz = n
			}
		}
	}
	if chunkSz <= 0 {
		chunkSz = defaultChunkSize
	}

	return &Client{
		inner:       inner,
		observer:    cfg.observer,
		logger:      cfg.logger,
		model:       model,
		expectedDim: cfg.dim,
		retry:       cfg.retry,
		circuit:     cb,
		fallback:    cfg.fallback,
		cache:       cfg.cache,
		docPrefix:   cfg.ollamaDocPrefix,
		queryPrefix: cfg.ollamaQueryPrefix,
		chunkSize:   chunkSz,
	}, nil
}

// EmbedOpt is a per-call option for EmbedWithResult.
type EmbedOpt func(*embedCallCfg)

type embedCallCfg struct {
	DryRun bool
	// Role is "query" / "passage" / "" — included in cacheKey to prevent
	// EmbedQuery/Embed collisions on backends that apply role-based prefixing
	// server-side (e.g. HTTP embed-server). Set internally by EmbedQuery via
	// withRole; not exposed publicly.
	Role string
}

// withRole sets the role on the call config. Internal — not exported.
// EmbedQuery uses "query"; Embed uses "passage" (default when unset is "").
func withRole(role string) EmbedOpt {
	return func(c *embedCallCfg) { c.Role = role }
}

// WithDryRun skips the backend call entirely and returns Status=Skipped vectors
// of zero length. For testing pipeline wiring without a live server.
func WithDryRun() EmbedOpt {
	return func(c *embedCallCfg) { c.DryRun = true }
}

// EmbedWithResult is the v2 Embed API — returns a typed Result with Status and
// fires Observer hooks around the backend call.
//
// Lifecycle:
//
//	OnBeforeEmbed → (fallback check) → callBackendResilient → OnAfterEmbed
//
// When chunking is active (len(texts) > chunkSize), `OnBeforeEmbed` /
// `OnAfterEmbed` fire ONCE PER DISPATCHED CHUNK, not once per user-facing
// call — observers tracking call count vs token count should reflect this.
// `embed_chunks_per_call` is recorded once per `EmbedWithResult` call
// (value=1 for non-chunked, value=N for chunked).
//
// Status semantics:
//   - StatusOk       — request succeeded, vectors are valid
//   - StatusDegraded — request failed, Err is set
//   - StatusFallback — primary degraded, secondary succeeded (E1)
//   - StatusSkipped  — nil inner, empty texts, or DryRun enabled
//
// E1 wires retry/circuit/fallback on top of this call.
// E2 wires auto-batching, E3 wires cache, E4 wires per-text Status reasoning.
// E5 wires client-side chunking when len(texts) > c.chunkSize.
func (c *Client) EmbedWithResult(ctx context.Context, texts []string, opts ...EmbedOpt) (*Result, error) {
	// E5: client-side chunking gate FIRST — before fallback routing — so
	// fallback-wired clients also chunk. Each chunk dispatches through
	// `dispatchChunk` which routes via fallback if configured.
	//
	// Why above fallback: fallback's primary call hits server-side cap at
	// HTTP 400 (4xx) which `embedWithFallback` classifies as caller error
	// → no secondary attempt → caller sees raw 400 with no chunk, no retry.
	// Chunking above fallback gives both paths the protection.
	//
	// Why sequential (not parallel): ox-embed-server's batcher already
	// coalesces concurrent calls; parallel client chunks cause batcher
	// contention. Sequential also keeps client memory bounded.
	if c != nil && c.inner != nil && c.chunkSize > 0 && len(texts) > c.chunkSize {
		return c.embedChunked(ctx, texts, opts...)
	}

	// Record chunks=1 for the non-chunked path so the histogram covers
	// 100% of EmbedWithResult calls (chunked + non-chunked). Without this
	// only chunked calls show up, undercounting backend-call multipliers.
	if c != nil {
		recordChunksPerCall(c.model, 1)
	}

	// E1: if fallback is configured, route through embedWithFallback.
	if c != nil && c.fallback != nil {
		callCfg := embedCallCfg{}
		for _, o := range opts {
			o(&callCfg)
		}
		// DryRun shortcut still fires before fallback routing.
		if callCfg.DryRun {
			return dryRunResult(c, len(texts)), nil
		}
		res := embedWithFallback(ctx, c, c.fallback, texts, opts...)
		if res.Status == StatusDegraded {
			return res, res.Err
		}
		return res, nil
	}

	res := c.embedWithResultUnchained(ctx, texts, opts...)
	if res.Status == StatusDegraded {
		return res, res.Err
	}
	return res, nil
}

// dispatchChunk dispatches a single chunk through the fallback chain if
// configured, else direct via `embedWithResultUnchained`. Used by
// `embedChunked` so chunked paths preserve fallback semantics.
func (c *Client) dispatchChunk(ctx context.Context, chunk []string, opts ...EmbedOpt) *Result {
	if c.fallback != nil {
		return embedWithFallback(ctx, c, c.fallback, chunk, opts...)
	}
	return c.embedWithResultUnchained(ctx, chunk, opts...)
}

// embedChunked splits texts into sequential sub-batches of c.chunkSize and
// merges results. Called only when len(texts) > c.chunkSize.
//
// Metrics:
//   - embed_chunks_per_call{model}: recorded once per call with the number of chunks.
//   - embed_chunk_size{model}: recorded once per dispatched sub-batch.
//   - All existing per-call counters/histograms (embed_batch_size, embed_duration_seconds)
//     record the ORIGINAL len(texts) inside embedWithResultUnchained per chunk —
//     intentional: they reflect backend call sizes, not user intent.
//     The embed_chunks_per_call metric captures user-facing intent separately.
//
// On any sub-batch error: returns that error AS-IS with no partial results
// (all-or-nothing contract — callers expect vectors[i] corresponds to texts[i]).
func (c *Client) embedChunked(ctx context.Context, texts []string, opts ...EmbedOpt) (*Result, error) {
	numChunks := (len(texts) + c.chunkSize - 1) / c.chunkSize
	// Record chunk-count metric once for the user-facing call.
	recordChunksPerCall(c.model, numChunks)

	allVectors := make([]*Vector, 0, len(texts))

	for i := 0; i < len(texts); i += c.chunkSize {
		end := i + c.chunkSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk := texts[i:end]

		// Record per-sub-batch size.
		recordChunkSize(c.model, len(chunk))

		// Each chunk goes through fallback chain if configured.
		res := c.dispatchChunk(ctx, chunk, opts...)
		if res.Status == StatusDegraded {
			// Translate ErrDimMismatch.Index to report the absolute position
			// in the ORIGINAL input slice. validateDim populates Index with
			// the per-vector position WITHIN this chunk; add the chunk start
			// offset `i` to map it to the user-facing input index.
			if res.Err != nil {
				var de *ErrDimMismatch
				if errors.As(res.Err, &de) {
					return nil, &ErrDimMismatch{
						Got:   de.Got,
						Want:  de.Want,
						Model: de.Model,
						Index: i + de.Index,
					}
				}
			}
			return nil, res.Err
		}
		allVectors = append(allVectors, res.Vectors...)
	}

	return &Result{
		Vectors: allVectors,
		Status:  StatusOk,
		Model:   c.model,
	}, nil
}

// embedWithResultUnchained executes the embed call for this client WITHOUT
// consulting the fallback chain. Used internally by embedWithFallback to avoid
// recursion. External callers always go through EmbedWithResult.
func (c *Client) embedWithResultUnchained(ctx context.Context, texts []string, opts ...EmbedOpt) *Result {
	callCfg := embedCallCfg{}
	for _, o := range opts {
		o(&callCfg)
	}

	if c == nil || c.inner == nil {
		return &Result{Status: StatusSkipped, Model: ""}
	}
	if len(texts) == 0 {
		return &Result{
			Status: StatusSkipped,
			Model:  c.model,
		}
	}
	if callCfg.DryRun {
		return dryRunResult(c, len(texts))
	}

	// E3: full-batch cache lookup BEFORE backend call.
	// Full hit → skip callBackendResilient entirely (no retry, no circuit, no backend).
	// Partial miss (any single text absent) → fall through to backend for the full batch.
	if c.cache != nil {
		dim := c.inner.Dimension()
		cached := tryCacheFullBatchGet(ctx, c.cache, c.model, dim, c.docPrefix, c.queryPrefix, texts, callCfg.Role)
		if cached != nil {
			safeCall(func() { c.observer.OnCacheHit(ctx, len(texts)) })
			recordCacheHit(c.model)
			out := make([]*Vector, len(cached))
			for i, v := range cached {
				out[i] = &Vector{
					Embedding: v,
					Dim:       len(v),
					Status:    StatusOk,
				}
			}
			return &Result{
				Vectors: out,
				Status:  StatusOk,
				Model:   c.model,
			}
		}
		recordCacheMiss(c.model)
	}

	// Fire OnBeforeEmbed hook (panic-safe).
	safeCall(func() { c.observer.OnBeforeEmbed(ctx, c.model, len(texts)) })

	start := time.Now()
	raw, err := c.callBackendResilient(ctx, texts)
	dur := time.Since(start)

	if err != nil {
		safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusDegraded, dur, len(texts)) })
		return &Result{
			Vectors: emptyVectors(len(texts)),
			Status:  StatusDegraded,
			Model:   c.model,
			Err:     err,
		}
	}

	if len(raw) != len(texts) {
		partialErr := fmt.Errorf("embed: backend returned %d vectors, expected %d", len(raw), len(texts))
		safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusDegraded, dur, len(texts)) })
		return &Result{
			Vectors: emptyVectors(len(texts)),
			Status:  StatusDegraded,
			Model:   c.model,
			Err:     partialErr,
		}
	}

	// E3: populate cache after successful backend call (raw vectors, pre-pipeline).
	if c.cache != nil {
		dim := c.inner.Dimension()
		for i, vec := range raw {
			c.cache.Set(ctx, cacheKey(c.model, dim, c.docPrefix, c.queryPrefix, texts[i], callCfg.Role), vec)
		}
		recordCacheSet(c.model, len(raw))
	}

	out := make([]*Vector, len(raw))
	for i, v := range raw {
		out[i] = &Vector{
			Embedding: v,
			Dim:       len(v),
			Status:    StatusOk,
		}
	}
	safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusOk, dur, len(out)) })
	return &Result{
		Vectors: out,
		Status:  StatusOk,
		Model:   c.model,
	}
}

// dryRunResult returns a StatusSkipped Result with zero-value Vector entries.
func dryRunResult(c *Client, n int) *Result {
	zeros := make([]*Vector, n)
	for i := range zeros {
		zeros[i] = &Vector{Status: StatusSkipped}
	}
	model := ""
	if c != nil {
		model = c.model
	}
	return &Result{
		Vectors: zeros,
		Status:  StatusSkipped,
		Model:   model,
	}
}

// EmbedWithResult is the package-level v2 API shim — kept for backward
// compatibility with callers using the old free-function signature.
//
// If e is a *Client, its EmbedWithResult method is called directly (observer
// hooks fire). For any other Embedder, a temporary *Client wrapper is created
// with no observer wired — hooks are silent. New code should use
// NewClient(...).EmbedWithResult(...) directly.
//
// Deprecated: use (*Client).EmbedWithResult for new code.
func EmbedWithResult(ctx context.Context, e Embedder, texts []string, opts ...EmbedOpt) (*Result, error) {
	if c, ok := e.(*Client); ok {
		return c.EmbedWithResult(ctx, texts, opts...)
	}
	// Fallback: wrap in a temporary Client (no observer).
	tmp := &Client{
		inner:    e,
		observer: noopObserver{},
		model:    modelFromEmbedder(e),
		retry:    defaultRetryPolicy(),
	}
	return tmp.EmbedWithResult(ctx, texts, opts...)
}

// modelFromEmbedder returns the backend model name when available.
// Resolution order:
//  1. Model() string interface — caller-supplied or custom Embedder that
//     exposes its model name (e.g. future embed/onnx extension).
//  2. Concrete type-switch for built-in backends (HTTPEmbedder, OllamaClient,
//     VoyageClient) — avoids requiring a public Model() method on each type.
//  3. Falls back to "" for unknown / opaque types.
func modelFromEmbedder(e Embedder) string {
	if e == nil {
		return ""
	}
	type modelGetter interface{ Model() string }
	if m, ok := e.(modelGetter); ok {
		return m.Model()
	}
	switch v := e.(type) {
	case *HTTPEmbedder:
		return v.model
	case *OllamaClient:
		return v.model
	case *VoyageClient:
		return v.model
	default:
		return ""
	}
}

// emptyVectors returns n placeholder Vector entries with Status=StatusSkipped.
func emptyVectors(n int) []*Vector {
	out := make([]*Vector, n)
	for i := range out {
		out[i] = &Vector{Status: StatusSkipped}
	}
	return out
}
