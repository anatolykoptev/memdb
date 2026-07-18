package embedder

import (
	"errors"
	"fmt"
	"log/slog"

	gokitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
)

// Config holds all embedder configuration in one typed struct.
// Populated from environment variables via config.Config.
type Config struct {
	Type         string // "onnx" | "voyage" | "ollama" | "http"
	ONNXModelDir string
	VoyageAPIKey string
	Model        string // voyage, ollama, or http model name
	OllamaURL    string
	OllamaDim    int    // 0 = auto-detect from first response
	OllamaPrefix string // client-side document prefix (e.g. "passage: ")
	OllamaQuery  string // client-side query prefix (e.g. "query: ")
	HTTPBaseURL  string // for type="http" — URL of embed-server sidecar
	HTTPDim      int    // dimension override (default 1024)

	// HTTPCacheClient enables Redis-backed embedding cache for type="http".
	// When non-nil, every (model, dim, prefix, text) lookup short-circuits the
	// embed-server call on cache hit. Saves the HTTP round trip + ONNX
	// inference. nil = caching disabled (legacy behaviour, default for
	// non-http backends). Wired from cmd/server/main.go after redis.NewClient.
	HTTPCacheClient *cache.Client

	// HTTPCircuit enables a circuit breaker for type="http". When non-nil,
	// the embedder short-circuits after N consecutive backend failures
	// instead of stacking timeouts. Mirrors the rerank CB pattern
	// (MEMDB_RERANK_CIRCUIT). nil = disabled (legacy behaviour).
	HTTPCircuit *gokitembed.CircuitConfig
}

// New constructs the appropriate Embedder from cfg.
// Returns an error if the type is unknown or required config is missing.
func New(cfg Config, logger *slog.Logger) (Embedder, error) {
	switch cfg.Type {
	case "ollama":
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
		logger.Info("embedder: ollama",
			slog.String("url", url),
			slog.String("model", model),
			slog.String("doc_prefix", cfg.OllamaPrefix),
			slog.String("query_prefix", cfg.OllamaQuery),
		)
		return c, nil

	case "voyage":
		if cfg.VoyageAPIKey == "" {
			return nil, errors.New("embedder: voyage requires VOYAGE_API_KEY")
		}
		model := cfg.Model
		if model == "" {
			model = defaultModel
		}
		c := NewVoyageClient(cfg.VoyageAPIKey, model, logger)
		logger.Info("embedder: voyage", slog.String("model", model))
		return c, nil

	case "onnx", "":
		if cfg.ONNXModelDir == "" {
			return nil, errors.New("embedder: onnx requires MEMDB_ONNX_MODEL_DIR")
		}
		modelCfg := DefaultONNXConfig()
		if mc, ok := knownONNXModels[cfg.Model]; ok {
			modelCfg = mc
		}
		e, err := NewONNXEmbedder(cfg.ONNXModelDir, modelCfg, logger)
		if err != nil {
			return nil, fmt.Errorf("embedder: onnx init: %w", err)
		}
		logger.Info("embedder: onnx", slog.String("model_dir", cfg.ONNXModelDir))
		return e, nil

	case "http":
		if cfg.HTTPBaseURL == "" {
			return nil, errors.New("embedder: http requires MEMDB_EMBED_URL")
		}
		dim := cfg.HTTPDim
		if dim == 0 {
			dim = 1024
		}
		model := cfg.Model
		if model == "" {
			model = "multilingual-e5-large"
		}
		opts := HTTPEmbedderOpts{}
		if cfg.HTTPCacheClient != nil {
			opts.Cache = NewRedisEmbedCache(cfg.HTTPCacheClient, 0, logger)
		}
		if cfg.HTTPCircuit != nil {
			opts.Circuit = cfg.HTTPCircuit
		}
		e := NewHTTPEmbedderWithOpts(cfg.HTTPBaseURL, model, dim, logger, opts)
		logger.Info("embedder: http",
			slog.String("url", cfg.HTTPBaseURL),
			slog.String("model", model),
			slog.Bool("cache_enabled", opts.Cache != nil),
			slog.Bool("circuit_enabled", opts.Circuit != nil),
		)
		return e, nil

	default:
		return nil, fmt.Errorf("embedder: unknown type %q (valid: onnx, voyage, ollama, http)", cfg.Type)
	}
}
