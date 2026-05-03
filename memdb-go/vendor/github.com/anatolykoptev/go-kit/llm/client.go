// Package llm provides an OpenAI-compatible LLM client with retry and fallback keys.
// Supports text and multimodal (vision) requests. Zero external dependencies
// beyond net/http. Designed to replace duplicated LLM clients across go-* services.
package llm

import (
	"context"
	"errors"
	"net/http"
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
	baseURL      string
	apiKey       string
	model        string
	maxTokens    int
	temperature  *float64 // nil = omit from request (some models reject it)
	httpClient   *http.Client
	fallbackKeys []string
	maxRetries   int
	endpoints    []Endpoint
	middleware   []Middleware
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
func NewClient(baseURL, apiKey, model string, opts ...Option) *Client {
	// Temperature is intentionally nil — see ChatRequest.Temperature comment.
	// Callers who want non-default sampling pass WithTemperature(t).
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		model:      model,
		maxTokens:  defaultMaxTokens,
		maxRetries: defaultMaxRetries,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Complete sends a text completion request with optional system prompt.
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
		parts = append(parts, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: img.URL},
		})
	}
	msgs := []Message{{Role: "user", Content: parts}}
	return c.CompleteRaw(ctx, msgs, opts...)
}

// CompleteRaw sends a chat completion with explicit messages.
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
		Model:       c.model,
		Messages:    messages,
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
