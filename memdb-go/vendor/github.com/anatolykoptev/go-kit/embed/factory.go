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
