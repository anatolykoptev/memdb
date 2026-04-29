package embed

import (
	"errors"
	"fmt"
	"log/slog"
)

// Default model and dimension applied when Config leaves them unset.
const (
	defaultHTTPModel = "multilingual-e5-large"
	defaultHTTPDim   = 1024
)

// ErrONNXNotInFactory is returned by [New] when Config.Type == "onnx".
//
// ONNX requires cgo + libonnxruntime + libtokenizers, which is too heavy a
// dependency for the default factory. Callers that need ONNX should import
// the subpackage github.com/anatolykoptev/go-kit/embed/onnx and call
// onnx.New(cfg, logger) directly. memdb-go does this in its server-init
// wiring; pure-HTTP/Ollama/Voyage callers never link the cgo deps.
var ErrONNXNotInFactory = errors.New(
	"embed.New: type=\"onnx\" not supported by this factory; " +
		"import github.com/anatolykoptev/go-kit/embed/onnx and call onnx.New",
)

// New constructs the appropriate Embedder from cfg.
//
// Supported Config.Type values:
//
//   - "http"   — [NewHTTPEmbedder]
//   - "ollama" — [NewOllamaClient] with prefix/dim options applied
//   - "voyage" — [NewVoyageClient]
//   - "onnx"   — returns [ErrONNXNotInFactory]; use embed/onnx subpackage
//
// Returns an error if the type is unknown or required config is missing.
// logger=nil falls back to slog.Default() inside each backend constructor.
func New(cfg Config, logger *slog.Logger) (Embedder, error) {
	if logger == nil {
		logger = slog.Default()
	}
	switch cfg.Type {
	case "ollama":
		return newOllamaFromConfig(cfg, logger), nil
	case "voyage":
		return newVoyageFromConfig(cfg, logger)
	case "http":
		return newHTTPFromConfig(cfg, logger)
	case "onnx", "":
		return nil, ErrONNXNotInFactory
	default:
		return nil, fmt.Errorf("embed: unknown type %q (valid: http, ollama, voyage, onnx)", cfg.Type)
	}
}

// newFromInternal builds an Embedder from an already-resolved cfgInternal.
// Used by NewClient (v2). v1 New(cfg, logger) continues to call the private
// per-backend helpers directly for full backward compatibility.
//
// If cfg.customEmbedder is set (via WithEmbedder), it is returned immediately
// without backend factory dispatch. This is the ONNX path: caller imports
// embed/onnx, builds *onnx.Embedder, and passes it via WithEmbedder.
func newFromInternal(cfg *cfgInternal) (Embedder, error) {
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	if cfg.customEmbedder != nil {
		return cfg.customEmbedder, nil
	}
	switch cfg.backend {
	case "ollama":
		var opts []OllamaOption
		if cfg.ollamaDim > 0 {
			opts = append(opts, WithOllamaDimension(cfg.ollamaDim))
		}
		if cfg.ollamaDocPrefix != "" {
			opts = append(opts, WithTextPrefix(cfg.ollamaDocPrefix))
		}
		if cfg.ollamaQueryPrefix != "" {
			opts = append(opts, WithQueryPrefix(cfg.ollamaQueryPrefix))
		}
		if cfg.timeout > 0 {
			opts = append(opts, WithOllamaTimeout(cfg.timeout))
		}
		url := cfg.url
		if url == "" {
			url = ollamaDefaultURL
		}
		model := cfg.model
		if model == "" {
			model = ollamaDefaultModel
		}
		return NewOllamaClient(url, model, cfg.logger, opts...), nil
	case "voyage":
		if cfg.voyageAPIKey == "" {
			return nil, errors.New("embed: voyage requires voyageAPIKey (use WithVoyageAPIKey)")
		}
		model := cfg.model
		if model == "" {
			model = voyageDefaultModel
		}
		return NewVoyageClient(cfg.voyageAPIKey, model, cfg.logger), nil
	case "http", "":
		if cfg.url == "" {
			return nil, errors.New("embed: http backend requires url (pass to NewClient)")
		}
		model := cfg.model
		if model == "" {
			model = defaultHTTPModel
		}
		dim := cfg.dim
		if dim == 0 {
			dim = defaultHTTPDim
		}
		return NewHTTPEmbedder(cfg.url, model, dim, cfg.logger), nil
	default:
		return nil, fmt.Errorf("embed: unknown backend %q (valid: http, ollama, voyage)", cfg.backend)
	}
}

// newOllamaFromConfig wires an OllamaClient from Config and logs the choice.
func newOllamaFromConfig(cfg Config, logger *slog.Logger) Embedder {
	model := cfg.Model
	if model == "" {
		model = ollamaDefaultModel
	}
	url := cfg.OllamaURL
	if url == "" {
		url = ollamaDefaultURL
	}
	var opts []OllamaOption
	if cfg.OllamaDim > 0 {
		opts = append(opts, WithOllamaDimension(cfg.OllamaDim))
	}
	if cfg.OllamaPrefix != "" {
		opts = append(opts, WithTextPrefix(cfg.OllamaPrefix))
	}
	if cfg.OllamaQuery != "" {
		opts = append(opts, WithQueryPrefix(cfg.OllamaQuery))
	}
	c := NewOllamaClient(url, model, logger, opts...)
	logger.Info("embed: ollama",
		slog.String("url", url),
		slog.String("model", model),
		slog.String("doc_prefix", cfg.OllamaPrefix),
		slog.String("query_prefix", cfg.OllamaQuery),
	)
	return c
}

// newVoyageFromConfig wires a VoyageClient from Config.
func newVoyageFromConfig(cfg Config, logger *slog.Logger) (Embedder, error) {
	if cfg.VoyageAPIKey == "" {
		return nil, errors.New("embed: voyage requires VoyageAPIKey")
	}
	model := cfg.Model
	if model == "" {
		model = voyageDefaultModel
	}
	c := NewVoyageClient(cfg.VoyageAPIKey, model, logger)
	logger.Info("embed: voyage", slog.String("model", model))
	return c, nil
}

// newHTTPFromConfig wires an HTTPEmbedder from Config.
func newHTTPFromConfig(cfg Config, logger *slog.Logger) (Embedder, error) {
	if cfg.HTTPBaseURL == "" {
		return nil, errors.New("embed: http requires HTTPBaseURL")
	}
	dim := cfg.HTTPDim
	if dim == 0 {
		dim = defaultHTTPDim
	}
	model := cfg.Model
	if model == "" {
		model = defaultHTTPModel
	}
	e := NewHTTPEmbedder(cfg.HTTPBaseURL, model, dim, logger)
	logger.Info("embed: http", slog.String("url", cfg.HTTPBaseURL), slog.String("model", model))
	return e, nil
}
