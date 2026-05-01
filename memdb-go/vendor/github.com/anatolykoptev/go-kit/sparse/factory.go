package sparse

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// New constructs the appropriate SparseEmbedder from cfg.
//
// Supported Config.Type values:
//
//   - "http" — NewHTTPSparseEmbedder
//
// Any other value (including "") returns an error. logger=nil falls back
// to slog.Default() inside the backend constructor.
func New(cfg Config, logger *slog.Logger) (SparseEmbedder, error) {
	if logger == nil {
		logger = slog.Default()
	}
	switch cfg.Type {
	case "http", "":
		return newHTTPFromConfig(cfg, logger)
	default:
		return nil, fmt.Errorf("sparse: unknown type %q (valid: http)", cfg.Type)
	}
}

// NewFromEnv constructs a SparseEmbedder from environment variables.
//
// Recognised variables:
//
//	SPARSE_BACKEND       — only "http" supported in v1; default "http"
//	SPARSE_HTTP_BASE_URL — embed-server URL (required for http)
//	SPARSE_MODEL         — default "splade-v3-distilbert"
//	SPARSE_HTTP_TIMEOUT  — Go duration; default 30s
//	SPARSE_TOP_K         — int; 0/unset uses server default (256)
//	SPARSE_MIN_WEIGHT    — float; 0/unset uses server default (0.0)
//	SPARSE_VOCAB_SIZE    — int; 0/unset uses 30522 (BERT base)
//
// Returns an error if SPARSE_BACKEND is "http" and SPARSE_HTTP_BASE_URL is
// empty. Mirrors embed/'s env-driven factory pattern.
func NewFromEnv(logger *slog.Logger) (SparseEmbedder, error) {
	backend := getenv("SPARSE_BACKEND", "http")
	if backend != "http" {
		return nil, fmt.Errorf("sparse: SPARSE_BACKEND=%q (only http is supported in v1)", backend)
	}
	cfg := Config{
		Type:        "http",
		HTTPBaseURL: os.Getenv("SPARSE_HTTP_BASE_URL"),
		Model:       getenv("SPARSE_MODEL", ""),
	}
	if v, ok := envInt("SPARSE_VOCAB_SIZE"); ok {
		cfg.VocabSize = v
	}
	if v, ok := envInt("SPARSE_TOP_K"); ok {
		cfg.TopK = v
	}
	if v, ok := envFloat32("SPARSE_MIN_WEIGHT"); ok {
		cfg.MinWeight = v
	}

	timeout, err := envDuration("SPARSE_HTTP_TIMEOUT")
	if err != nil {
		return nil, err
	}

	return newHTTPFromConfigWithTimeout(cfg, timeout, logger)
}

// newFromInternal builds a SparseEmbedder from an already-resolved
// cfgInternal. Used by NewClient (v2). v1 New(cfg, logger) calls
// newHTTPFromConfig directly.
func newFromInternal(cfg *cfgInternal) (SparseEmbedder, error) {
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	if cfg.customEmbedder != nil {
		return cfg.customEmbedder, nil
	}
	switch cfg.backend {
	case "http", "":
		if cfg.url == "" {
			return nil, errors.New("sparse: http backend requires url (pass to NewClient)")
		}
		model := cfg.model
		if model == "" {
			model = httpSparseDefaultModel
		}
		var opts []HTTPSparseOption
		if cfg.timeout > 0 {
			opts = append(opts, WithHTTPTimeout(cfg.timeout))
		}
		if cfg.topK > 0 {
			opts = append(opts, WithTopK(cfg.topK))
		}
		if cfg.minWeight > 0 {
			opts = append(opts, WithMinWeight(cfg.minWeight))
		}
		if cfg.vocabSize > 0 {
			opts = append(opts, WithVocabSize(cfg.vocabSize))
		}
		if cfg.observer != nil {
			opts = append(opts, WithHTTPObserver(cfg.observer))
		}
		// Propagate the configured retry policy. cfg.retry is the zero
		// RetryConfig (MaxAttempts=0) when the caller did NOT pass
		// WithRetry on a path that doesn't go through defaultCfg(); in
		// that case we leave the backend's default in place. NoRetry has
		// MaxAttempts=1 (>0), so opt-out flows through correctly.
		if cfg.retry.MaxAttempts > 0 {
			opts = append(opts, WithHTTPRetry(cfg.retry))
		}
		return NewHTTPSparseEmbedder(cfg.url, model, cfg.logger, opts...), nil
	default:
		return nil, fmt.Errorf("sparse: unknown backend %q (valid: http)", cfg.backend)
	}
}

// newHTTPFromConfig wires an HTTPSparseEmbedder from Config.
func newHTTPFromConfig(cfg Config, logger *slog.Logger) (SparseEmbedder, error) {
	return newHTTPFromConfigWithTimeout(cfg, 0, logger)
}

func newHTTPFromConfigWithTimeout(cfg Config, timeout time.Duration, logger *slog.Logger) (SparseEmbedder, error) {
	if cfg.HTTPBaseURL == "" {
		return nil, errors.New("sparse: http requires HTTPBaseURL")
	}
	model := cfg.Model
	if model == "" {
		model = httpSparseDefaultModel
	}
	var opts []HTTPSparseOption
	if timeout > 0 {
		opts = append(opts, WithHTTPTimeout(timeout))
	}
	if cfg.TopK > 0 {
		opts = append(opts, WithTopK(cfg.TopK))
	}
	if cfg.MinWeight > 0 {
		opts = append(opts, WithMinWeight(cfg.MinWeight))
	}
	if cfg.VocabSize > 0 {
		opts = append(opts, WithVocabSize(cfg.VocabSize))
	}
	e := NewHTTPSparseEmbedder(cfg.HTTPBaseURL, model, logger, opts...)
	logger.Info("sparse: http",
		slog.String("url", cfg.HTTPBaseURL),
		slog.String("model", model),
	)
	return e, nil
}

// --- env helpers ---

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func envFloat32(key string) (float32, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 32)
	if err != nil {
		return 0, false
	}
	return float32(f), true
}

func envDuration(key string) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("sparse: bad %s=%q: %w", key, v, err)
	}
	return d, nil
}
