package embed

import (
	"log/slog"
	"time"
)

// Config holds all embedder configuration in one typed struct.
// Populated from environment variables by callers.
//
// Type selects the backend:
//
//   - "http"   — OpenAI-compatible /v1/embeddings endpoint (HTTPBaseURL).
//   - "ollama" — Ollama /api/embed (OllamaURL).
//   - "voyage" — Voyage AI hosted /v1/embeddings (VoyageAPIKey).
//   - "onnx"   — local ONNX Runtime; requires the embed/onnx subpackage
//     factory because it depends on cgo.
//
// Fields not relevant to the chosen Type are ignored.
type Config struct {
	Type         string // "http" | "ollama" | "voyage" | "onnx"
	ONNXModelDir string
	VoyageAPIKey string
	Model        string // voyage, ollama, or http model name
	OllamaURL    string
	OllamaDim    int    // 0 = auto-detect from first response
	OllamaPrefix string // client-side document prefix (e.g. "passage: ")
	OllamaQuery  string // client-side query prefix (e.g. "query: ")
	HTTPBaseURL  string // for type="http" — URL of embed-server sidecar
	HTTPDim      int    // dimension override (default 1024)
}

// cfgInternal holds resolved configuration after Opt application. Built by
// NewClient via functional options OR translated from Config by v1 New().
type cfgInternal struct {
	// Backend selection
	backend string // "http"|"ollama"|"voyage" (onnx via subpackage)

	// Common
	url     string
	model   string
	dim     int
	timeout time.Duration

	// Ollama-specific
	ollamaDocPrefix   string
	ollamaQueryPrefix string
	ollamaDim         int

	// Voyage-specific
	voyageAPIKey string

	// Caller-supplied embedder (e.g. *onnx.Embedder from embed/onnx, or any
	// custom implementation). When non-nil, takes precedence over backend
	// factory dispatch — NewClient returns it directly. This allows ONNX
	// callers to import embed/onnx separately, build *onnx.Embedder (which
	// requires cgo), and receive all future E1+ wrapping without forcing cgo
	// on pure-HTTP callers.
	customEmbedder Embedder

	// Observability
	observer Observer
	logger   *slog.Logger

	// E1: resiliency
	retry    RetryPolicy
	circuit  *CircuitBreaker
	fallback *Client

	// E3: pluggable cache interface, opt-in via WithCache. nil = disabled.
	cache Cache
}

// Opt is a functional option for NewClient.
type Opt func(*cfgInternal)

// defaultCfg returns a cfgInternal with sensible defaults.
// E1: retry policy is ON by default (defaultRetryPolicy). circuit and fallback are OFF.
func defaultCfg() *cfgInternal {
	return &cfgInternal{
		observer: noopObserver{},
		timeout:  30 * time.Second,
		retry:    defaultRetryPolicy(),
		circuit:  nil,
		fallback: nil,
	}
}

// --- Common options ---

// WithModel sets the backend model name.
func WithModel(model string) Opt {
	return func(c *cfgInternal) { c.model = model }
}

// WithDim sets the expected embedding dimension. Zero = auto-detect from response.
func WithDim(dim int) Opt {
	return func(c *cfgInternal) { c.dim = dim }
}

// WithTimeout sets the per-request HTTP timeout.
func WithTimeout(d time.Duration) Opt {
	return func(c *cfgInternal) { c.timeout = d }
}

// WithObserver registers a lifecycle Observer. nil-ignored (noopObserver stays active).
func WithObserver(obs Observer) Opt {
	return func(c *cfgInternal) {
		if obs != nil {
			c.observer = obs
		}
	}
}

// WithLogger sets the slog.Logger. nil-ignored (backends fall back to slog.Default()).
func WithLogger(l *slog.Logger) Opt {
	return func(c *cfgInternal) {
		if l != nil {
			c.logger = l
		}
	}
}

// --- Backend selectors ---

// WithBackend sets the backend type explicitly. Valid: "http" | "ollama" | "voyage".
// Mutually exclusive with WithEmbedder — if both are set, WithEmbedder wins.
func WithBackend(name string) Opt {
	return func(c *cfgInternal) { c.backend = name }
}

// WithEmbedder accepts a pre-built Embedder (e.g. *onnx.Embedder from the
// embed/onnx subpackage, or a custom impl). NewClient skips backend factory
// dispatch and wires this Embedder as the inner backend of the returned *Client.
// Required for ONNX usage via NewClient (avoids forcing cgo on pure-HTTP callers).
//
// ONNX usage:
//
//	import "github.com/anatolykoptev/go-kit/embed/onnx"
//
//	onnxEmb, _ := onnx.New(onnx.Config{...}, logger)
//	c, _ := embed.NewClient("", embed.WithEmbedder(onnxEmb))
//
// Note: when WithEmbedder is set, WithBackend is silently ignored. To make the
// override explicit, set only one of the two.
//
// nil is ignored (backend dispatch proceeds normally).
func WithEmbedder(e Embedder) Opt {
	return func(c *cfgInternal) {
		if e != nil {
			c.customEmbedder = e
		}
	}
}

// --- Backend-specific options ---

// WithVoyageAPIKey sets the API key for the Voyage backend.
func WithVoyageAPIKey(key string) Opt {
	return func(c *cfgInternal) { c.voyageAPIKey = key }
}

// WithOllamaDocPrefix sets the document-mode prefix for Ollama (e.g. "passage: ").
// Mirrors existing WithTextPrefix on OllamaClient — exposed at package level.
func WithOllamaDocPrefix(prefix string) Opt {
	return func(c *cfgInternal) { c.ollamaDocPrefix = prefix }
}

// WithOllamaQueryPrefix sets the query-mode prefix for Ollama (e.g. "query: ").
func WithOllamaQueryPrefix(prefix string) Opt {
	return func(c *cfgInternal) { c.ollamaQueryPrefix = prefix }
}

// WithOllamaDim sets the Ollama-side dimension override.
func WithOllamaDim(dim int) Opt {
	return func(c *cfgInternal) { c.ollamaDim = dim }
}

// --- E1: Resiliency options ---

// WithRetry configures the retry policy for transient errors (5xx HTTP status).
// Pass embed.NoRetry to disable retries entirely.
// Default: defaultRetryPolicy() (3 attempts, exp backoff 200ms→5s, jitter 10%).
func WithRetry(p RetryPolicy) Opt {
	return func(c *cfgInternal) { c.retry = p }
}

// WithCircuit enables the circuit breaker with the given configuration.
// By default the circuit breaker is OFF (nil). Wiring the observer for
// OnCircuitTransition happens in newClientFromInternal after all opts are applied.
// A sentinel *CircuitBreaker is stored here; the final one (with model+obs hook)
// is built in newClientFromInternal.
func WithCircuit(cfg CircuitConfig) Opt {
	return func(c *cfgInternal) {
		// Store a placeholder with the cfg. The final CB (with model + observer
		// wired) is built in newClientFromInternal once all opts have been applied.
		c.circuit = &CircuitBreaker{cfg: cfg}
	}
}

// WithFallback sets a secondary *Client to try when the primary returns
// StatusDegraded with a non-4xx error. Fallback depth is capped at 1.
func WithFallback(secondary *Client) Opt {
	return func(c *cfgInternal) { c.fallback = secondary }
}
