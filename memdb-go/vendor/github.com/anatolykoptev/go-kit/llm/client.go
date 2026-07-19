// Package llm provides an OpenAI-compatible LLM client with retry and fallback keys.
// Supports text and multimodal (vision) requests. Zero external dependencies
// beyond net/http. Designed to replace duplicated LLM clients across go-* services.
package llm

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// Default client constants.
const (
	defaultMaxTokens  = 8192
	defaultMaxRetries = 3
	defaultTimeout    = 90 * time.Second
	retryDelay        = 500 * time.Millisecond
	maxRetryDelay     = 5 * time.Second
)

// Client is an OpenAI-compatible LLM client with retry and fallback key support.
type Client struct {
	baseURL               string
	apiKey                string
	model                 string
	maxTokens             int
	temperature           *float64 // nil = omit from request (some models reject it)
	httpClient            *http.Client
	fallbackKeys          []string
	maxRetries            int
	endpoints             []Endpoint
	middleware            []Middleware
	endpointObserver      EndpointAttemptObserver
	perAttemptTimeout     time.Duration     // 0 = disabled; per-attempt wrapping skipped, behavior byte-identical to pre-feature
	cooldown              *modelCooldown    // nil = disabled; no per-model cooldown, behavior byte-identical to pre-feature
	selectionStrategy     SelectionStrategy // default SelectionPriority
	rander                *rand.Rand        // nil = global source; injectable for deterministic tests
	modelWeights          map[string]int    // nil = all models default weight 1 (SelectionWeighted)
	reasoningEffortModels []string          // nil = pass-through; non-empty = per-endpoint allowlist gating in attemptEndpoint
}

// Option configures the Client.
type Option func(*Client)

// WithFallbackKeys sets fallback API keys tried when the primary gets 429/5xx.
func WithFallbackKeys(keys []string) Option {
	return func(c *Client) { c.fallbackKeys = keys }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithMaxTokens sets the max tokens for completions.
func WithMaxTokens(n int) Option {
	return func(c *Client) { c.maxTokens = n }
}

// WithTemperature sets the sampling temperature. Without this option the
// request omits the field entirely — some providers (Anthropic claude-opus-4-7,
// future variants) reject any temperature parameter with 400 invalid_request.
func WithTemperature(t float64) Option {
	return func(c *Client) { c.temperature = &t }
}

// WithMaxRetries sets how many times to retry on retryable errors.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// Endpoint defines a complete API endpoint for fallback chains.
// Each endpoint can have its own URL, API key, and model.
type Endpoint struct {
	URL   string
	Key   string
	Model string
}

// WithEndpoints sets fallback endpoint chains. Each endpoint is tried
// in order on retryable errors. Overrides the base URL/key/model when set.
func WithEndpoints(endpoints []Endpoint) Option {
	return func(c *Client) { c.endpoints = endpoints }
}

// EndpointAttemptObserver — callback вызывается per-endpoint attempt:
// один раз для успешного, один раз для каждого failed endpoint в chain.
// nil err = success. Endpoint несёт Model — caller bumps per-model metric.
//
// Не вызывается для single-endpoint (без WithEndpoints) path — там нет
// chain-level events наблюдать (key rotation остаётся internal к kit).
//
// Observer не должен блокировать (logged async или просто incr counter) —
// fires внутри executeInner request path. Не должен panic'нуть —
// если recovery нужен, оборачивай в defer recover() сам.
type EndpointAttemptObserver func(ep Endpoint, err error)

// WithPerAttemptTimeout bounds EACH endpoint attempt in a WithEndpoints
// chain by its own deadline d, derived from the caller's ctx. The outer
// ctx remains an absolute ceiling: a per-attempt deadline is effectively
// min(d, time left on the outer ctx). When d <= 0 (default) no per-attempt
// wrapping is applied and the call path is byte-identical to before this
// option existed.
//
// Semantics: the per-attempt deadline bounds the WHOLE per-endpoint attempt —
// including that endpoint's internal doWithRetry backoff — matching what
// hand-rolled caller loops did with context.WithTimeout(ctx, timeout) around a
// single high-level call. One slow model can no longer starve the rest of the chain.
//
// No effect on the single-endpoint (no WithEndpoints) path — there is no
// chain to bound. Does NOT change the WithEndpoints XOR WithFallbackKeys constraint.
func WithPerAttemptTimeout(d time.Duration) Option {
	return func(c *Client) { c.perAttemptTimeout = d }
}

// WithEndpointAttemptObserver регистрирует observer на per-endpoint
// chain attempts. Use case: per-model fail counter, per-model latency
// observation, structured logging of which model in chain succeeded.
//
//	obs := func(ep llm.Endpoint, err error) {
//	    label := ep.Model
//	    if err != nil {
//	        promChainFails.WithLabelValues(label).Inc()
//	    } else {
//	        promChainSuccess.WithLabelValues(label).Inc()
//	    }
//	}
//	c := llm.NewClient(url, key, model,
//	    llm.WithEndpoints(eps),
//	    llm.WithEndpointAttemptObserver(obs),
//	)
func WithEndpointAttemptObserver(obs EndpointAttemptObserver) Option {
	return func(c *Client) { c.endpointObserver = obs }
}

// WithModelCooldown enables per-model quota-aware cooldown for the WithEndpoints
// fallback chain. After cfg.FailThreshold (default 2) observed quota-class
// failures on a model (429, or a 503 marking quota/auth-unavailable), that model
// is put in cooldown for the upstream's Retry-After (clamped to cfg.Max, default
// 10m) or cfg.Default (60s) and subsequent calls SKIP it — going straight to the
// next healthy model. The cooldown lifts when its window expires; a 200 from the
// model clears it early (quota recovered). A cooled model receives no traffic
// until then, so the "clear early" path only applies to a model still in the
// rotation, not the one being skipped.
//
// Scope: this applies to the NON-STREAMING completion chain (Complete /
// CompleteRaw / chat completions through executeInner). Stream is NOT
// cooldown-aware yet — it neither consults nor records cooldown state; a chain
// used for streaming sees no skipping (tracked as a P2 follow-up).
//
// Cooldown is MODEL-KEYED. BuildModelChainEndpoints dedups by model id, so a
// helper-built chain has model-distinct endpoints. If you hand-build a []Endpoint
// with the SAME Model on multiple entries (e.g. same model id behind different
// keys/URLs), they SHARE one cooldown entry — cooling one cools all of them.
// Give such entries distinct Model strings if you want independent cooldown.
//
// Opt-in: without this option there is zero cooldown state and behaviour is
// byte-identical to before it existed. No effect on the single-endpoint (no
// WithEndpoints) path.
//
// Never fail-closed: if EVERY model in the chain is cooled, the loop still
// attempts the primary (degraded > dead) and returns the real upstream error.
//
// Composes with WithCircuitBreaker: the breaker is the outermost middleware
// keyed on the construction model (whole-client granularity); cooldown is a
// per-endpoint-model skip INSIDE the chain loop. They are orthogonal.
func WithModelCooldown(cfg CooldownConfig) Option {
	return func(c *Client) {
		if c.cooldown == nil {
			c.cooldown = newModelCooldown(cfg)
		} else {
			c.cooldown.cfg = cfg.withDefaults()
		}
	}
}

// WithModelCooldownObserver registers an optional, non-blocking callback fired
// once on cooldown ENTRY (cooling=true, d = the cooldown duration) and once on
// RECOVERY (cooling=false, d = 0) per model. This is the de-duped degraded-chain
// signal: a consumer wanting a "primary quota exhausted" log line wires a 3-line
// observer that logs on cooling==true && model==primary. The callback must not
// block or panic (it fires inside the request path on a state transition).
//
// Implies WithModelCooldown with default config if the cooldown is not already
// enabled, so the observer has something to observe.
func WithModelCooldownObserver(fn func(model string, cooling bool, d time.Duration)) Option {
	return func(c *Client) {
		if c.cooldown == nil {
			c.cooldown = newModelCooldown(CooldownConfig{})
		}
		c.cooldown.onChange = fn
	}
}

// WithSelectionStrategy sets the endpoint selection strategy for WithEndpoints chains.
// SelectionPriority (default) tries endpoints in configured order (primary first).
// SelectionRandom shuffles eligible (non-cooled) endpoints on each request,
// distributing load across the pool so no single provider is always tried first.
//
// NewClient reads LLM_SELECTION_STRATEGY from the environment automatically; this
// option lets callers override or test-inject the strategy programmatically.
func WithSelectionStrategy(s SelectionStrategy) Option {
	return func(c *Client) { c.selectionStrategy = s }
}

// WithRander sets the random source used for SelectionRandom strategy.
// The injected *rand.Rand is intended for deterministic testing only and
// MUST NOT be shared across concurrent requests: *rand.Rand is not safe
// for concurrent use. Leave nil in production to use the locked global
// rand.Shuffle.
func WithRander(r *rand.Rand) Option {
	return func(c *Client) { c.rander = r }
}

// WithModelWeights sets per-model weights for SelectionWeighted strategy.
// Models with weight 0 are structurally excluded from the try-order.
// Models absent from the map default to weight 1. Negative weights are
// skipped (same contract as the env parser) with a warning — a negative
// weight inverts the Efraimidis-Spirakis key, promoting rather than
// suppressing a model, which is never the caller's intent. The map is
// copied defensively. Has no effect unless SelectionWeighted is used.
//
// If EVERY eligible model is weight 0 (after filtering), the last-resort
// guard in weightedShuffleEndpoints attempts the priority-primary endpoint
// rather than failing closed — so "weight-0 never attempted" holds only
// while ≥1 positive-weight eligible model exists.
func WithModelWeights(weights map[string]int) Option {
	return func(c *Client) {
		m := make(map[string]int, len(weights))
		for k, v := range weights {
			if v < 0 {
				slog.Warn("llm: WithModelWeights: negative weight treated as 0 (excluded); use 0 explicitly to suppress a model", "model", k, "weight", v)
				m[k] = 0
				continue
			}
			m[k] = v
		}
		c.modelWeights = m
	}
}

// WithReasoningEffortModels sets the allowlist of exact model IDs that receive
// reasoning_effort in a WithEndpoints chain. When the allowlist is non-empty,
// attemptEndpoint strips reasoning_effort from any endpoint whose model is NOT
// in the list — preventing HTTP 400 from providers that reject the parameter.
//
// Empty (default) = pass-through for all endpoints (existing behavior preserved
// for callers not using this option).
//
// NewClient also reads LLM_REASONING_EFFORT_MODELS env (comma-separated model
// IDs) on construction; an explicit WithReasoningEffortModels option applied
// after NewClient wins over the env var.
func WithReasoningEffortModels(models []string) Option {
	return func(c *Client) { c.reasoningEffortModels = models }
}

// Middleware wraps chat completion calls. Use for logging, metrics, caching.
// The next function sends the request to the API (or the next middleware).
// First added middleware is the outermost wrapper.
type Middleware func(ctx context.Context, req *ChatRequest, next func(context.Context, *ChatRequest) (*ChatResponse, error)) (*ChatResponse, error)

// WithMiddleware adds a middleware to the execution pipeline.
func WithMiddleware(m Middleware) Option {
	return func(c *Client) { c.middleware = append(c.middleware, m) }
}

// WithCircuitBreaker wires a CircuitBreaker as the OUTERMOST middleware.
// Tripped state short-circuits the call with ErrCircuitOpen so callers can
// fail-fast instead of paying the per-call timeout when the backend is
// degraded. Mirrors embed/rerank circuit-breaker patterns.
//
// Failure attribution: only errors that look like backend failure (5xx,
// 429, network/timeout) trip the breaker — context cancellation by the
// caller does NOT (it's a client decision, not a backend health signal).
//
// Pass a zero-value CircuitConfig{} to take defaults (5 fails, 30s open,
// 1 half-open probe). To disable cleanly later, recreate the Client without
// this option.
func WithCircuitBreaker(cfg CircuitConfig) Option {
	return func(c *Client) {
		cb := NewCircuitBreaker(cfg, c.model, nil)
		circuit := func(ctx context.Context, req *ChatRequest, next func(context.Context, *ChatRequest) (*ChatResponse, error)) (*ChatResponse, error) {
			if !cb.Allow() {
				return nil, ErrCircuitOpen
			}
			resp, err := next(ctx, req)
			if err != nil && isCircuitTrippingError(err) {
				cb.MarkFailure()
			} else if err == nil {
				cb.MarkSuccess()
			}
			// Caller-cancelled errors fall through without affecting state.
			return resp, err
		}
		// Prepend so the breaker wraps everything else (cache, fallback,
		// user middlewares all execute INSIDE the breaker boundary).
		c.middleware = append([]Middleware{circuit}, c.middleware...)
	}
}

// isCircuitTrippingError classifies an error as a backend-health failure.
// Returns false for caller-controlled errors (context cancellation) so the
// breaker doesn't trip when a client gives up early.
func isCircuitTrippingError(err error) bool {
	if err == nil {
		return false
	}
	// Context cancellation by the caller is not a backend failure.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Only treat DeadlineExceeded as a failure when it comes from OUR
		// httpClient timeout (backend too slow), not from the caller's ctx.
		// Approximation: deadlineExceeded with no parent ctx deadline → ours.
		// Conservative path: treat as failure — false-positive wallpapering
		// (one extra failure count) is preferable to silently hiding chronic
		// backend slowness from the breaker.
		if errors.Is(err, context.DeadlineExceeded) {
			return true
		}
		return false
	}
	// 4xx (other than 429) are caller errors, not backend health issues.
	// We don't have typed status here without parsing the error string —
	// downstream RetryableError types in errors.go cover the canonical set.
	// Treat anything not explicitly safe as a failure (conservative).
	return true
}

// NewClient creates a new LLM client.
// For callers that want to gracefully disable LLM when apiKey is empty, see NewOptional.
//
// LLM_SELECTION_STRATEGY env var is read on construction: "random" → SelectionRandom;
// empty/missing → SelectionPriority (default, no log); any other non-empty value →
// SelectionPriority + slog.Warn. An explicit WithSelectionStrategy option always wins
// over the env var (options are applied after the env default is set).
func NewClient(baseURL, apiKey, model string, opts ...Option) *Client {
	// Temperature is intentionally nil — see ChatRequest.Temperature comment.
	// Callers who want non-default sampling pass WithTemperature(t).
	c := &Client{
		baseURL:           strings.TrimRight(baseURL, "/"),
		apiKey:            apiKey,
		model:             model,
		maxTokens:         defaultMaxTokens,
		maxRetries:        defaultMaxRetries,
		httpClient:        &http.Client{Timeout: defaultTimeout},
		selectionStrategy: parseSelectionStrategy(os.Getenv("LLM_SELECTION_STRATEGY")),
	}
	if raw := os.Getenv("LLM_MODEL_WEIGHTS"); raw != "" {
		c.modelWeights = parseModelWeights(raw)
	}
	if raw := os.Getenv("LLM_REASONING_EFFORT_MODELS"); raw != "" {
		c.reasoningEffortModels = parseCSV(raw)
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Complete sends a text completion request with optional system prompt.
// When using WithEndpoints, a model response with empty content and no tool calls
// is treated as a non-retryable failure on that endpoint; the chain advances to
// the next model. If all models return empty content, the call returns a terminal
// empty_completion APIError.
// If system is empty, only the user message is sent.
// Optional ChatOptions (e.g. WithChatTemperature, WithChatMaxTokens) override client defaults for this call.
func (c *Client) Complete(ctx context.Context, system, user string, opts ...ChatOption) (string, error) {
	var msgs []Message
	if system != "" {
		msgs = append(msgs, Message{Role: "system", Content: system})
	}
	msgs = append(msgs, Message{Role: "user", Content: user})
	return c.CompleteRaw(ctx, msgs, opts...)
}

// CompleteMultimodal sends a vision request with text + images.
// Optional ChatOptions (e.g. WithChatTemperature, WithChatMaxTokens) override client defaults for this call.
func (c *Client) CompleteMultimodal(ctx context.Context, prompt string, images []ImagePart, opts ...ChatOption) (string, error) {
	parts := []ContentPart{{Type: "text", Text: prompt}}
	for _, img := range images {
		if img.URL == "" && img.Base64 == "" {
			continue
		}
		url := img.URL
		if img.Base64 != "" {
			mt := img.MIMEType
			if mt == "" {
				mt = "image/png"
			}
			url = "data:" + mt + ";base64," + img.Base64
		}
		parts = append(parts, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: url, Detail: img.Detail},
		})
	}
	msgs := []Message{{Role: "user", Content: parts}}
	return c.CompleteRaw(ctx, msgs, opts...)
}

// CompleteRaw sends a chat completion with explicit messages.
// When using WithEndpoints, a model response with empty content and no tool calls
// is treated as a non-retryable failure on that endpoint; the chain advances to
// the next model. If all models return empty content, the call returns a terminal
// empty_completion APIError.
// Retries on 429/5xx, cycles through fallback keys.
// Optional ChatOptions (e.g. WithChatTemperature, WithChatMaxTokens) override client defaults for this call.
func (c *Client) CompleteRaw(ctx context.Context, messages []Message, opts ...ChatOption) (string, error) {
	req := c.newRequest(messages)
	if len(opts) > 0 {
		var cfg chatConfig
		for _, opt := range opts {
			opt(&cfg)
		}
		cfg.apply(req)
	}
	resp, err := c.execute(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *Client) newRequest(messages []Message) *ChatRequest {
	return &ChatRequest{
		Model: c.model,
		// Clone: per-request options (e.g. WithMessageTimestamps) mutate
		// req.Messages in place; never mutate the caller's slice/structs.
		Messages:    slices.Clone(messages),
		Temperature: c.temperature,
		MaxTokens:   c.maxTokens,
	}
}

func (c *Client) execute(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if len(c.middleware) == 0 {
		return c.executeInner(ctx, req)
	}
	return c.buildChain(0)(ctx, req)
}

func (c *Client) buildChain(i int) func(context.Context, *ChatRequest) (*ChatResponse, error) {
	if i >= len(c.middleware) {
		return c.executeInner
	}
	return func(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
		return c.middleware[i](ctx, req, c.buildChain(i+1))
	}
}

// ExtractJSON extracts a JSON value from LLM output that may be wrapped
// in markdown code fences or surrounded by text.
// Handles both JSON objects ({...}) and arrays ([...]).
func ExtractJSON(s string) string {
	// Try markdown ```json ... ``` first.
	start := strings.Index(s, "```json")
	if start >= 0 {
		s = s[start+7:]
		end := strings.Index(s, "```")
		if end >= 0 {
			return strings.TrimSpace(s[:end])
		}
	}
	// Try plain ``` ... ``` fences.
	start = strings.Index(s, "```")
	if start >= 0 {
		inner := s[start+3:]
		end := strings.Index(inner, "```")
		if end >= 0 {
			return strings.TrimSpace(inner[:end])
		}
	}
	// Fall back to finding first/last matching delimiters.
	// Try object first, then array, pick whichever starts earlier.
	firstObj := strings.IndexByte(s, '{')
	lastObj := strings.LastIndexByte(s, '}')
	firstArr := strings.IndexByte(s, '[')
	lastArr := strings.LastIndexByte(s, ']')

	objOK := firstObj >= 0 && lastObj > firstObj
	arrOK := firstArr >= 0 && lastArr > firstArr

	switch {
	case objOK && arrOK:
		// Earliest opener wins ("object before array" returns the outer object;
		// "array before object" returns the array). Closer is the LATEST of
		// either type, which captures the case  where the
		// caller wants both JSON values returned for downstream multi-value
		// parsing — not just the leading array.
		start := firstObj
		if firstArr < firstObj {
			start = firstArr
		}
		end := lastObj
		if lastArr > lastObj {
			end = lastArr
		}
		return s[start : end+1]
	case objOK:
		return s[firstObj : lastObj+1]
	case arrOK:
		return s[firstArr : lastArr+1]
	}
	return s
}
