package server

// server_init_search.go — search, embedder and LLM component initialization.
// Covers: initEmbedder, initSearchService, initLLMExtractor.

import (
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
	localrerank "github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

// initEmbedder initializes the embedder via factory (non-fatal if unavailable).
// When ONNXModelDirCode is set, also loads a second ONNX model and creates a Registry.
//
// 2026-05-01: cacheClient param wires a Redis-backed embedding cache via
// go-kit/embed.WithCache (only for cfg.EmbedderType=="http"). Saves the
// embed-server round trip + ONNX inference on idempotent re-embed (reverse-
// role pass, query rewrites, scheduler reorganiser sweeps). Pass nil to
// disable — embedder degrades gracefully to the no-cache path.
func initEmbedder(cfg *config.Config, h *handlers.Handler, cacheClient *cache.Client, logger *slog.Logger) embedder.Embedder {
	embCfg := embedder.Config{
		Type:            cfg.EmbedderType,
		ONNXModelDir:    cfg.ONNXModelDir,
		VoyageAPIKey:    cfg.VoyageAPIKey,
		Model:           cfg.EmbedderModel,
		OllamaURL:       cfg.OllamaURL,
		OllamaDim:       cfg.OllamaDim,
		OllamaPrefix:    cfg.OllamaPrefix,
		OllamaQuery:     cfg.OllamaQuery,
		HTTPBaseURL:     cfg.EmbedURL,
		HTTPCacheClient: cacheClient,
	}
	e, err := embedder.New(embCfg, logger)
	if err != nil {
		logger.Warn("embedder init failed (native search disabled)", slog.Any("error", err))
		return nil
	}
	h.SetEmbedder(e)

	// Multi-model registry: HTTP embedder uses sidecar(s) for models;
	// EmbedURLCode overrides jina URL when set (separate Python sidecar).
	if cfg.EmbedderType == "http" && cfg.EmbedURL != "" {
		registry := embedder.NewRegistry("multilingual-e5-large")
		registry.Register("multilingual-e5-large", e)

		codeURL := cfg.EmbedURL
		if cfg.EmbedURLCode != "" {
			codeURL = cfg.EmbedURLCode
		}
		codeEmb := embedder.NewHTTPEmbedder(codeURL, "jina-code-v2", 768, logger)
		registry.Register("jina-code-v2", codeEmb)
		logger.Info("code embedder loaded (http)",
			slog.String("model", "jina-code-v2"),
			slog.String("url", codeURL),
			slog.Int("dim", 768),
		)
		h.SetEmbedRegistry(registry)
	} else if cfg.ONNXModelDirCode != "" {
		registry := embedder.NewRegistry("multilingual-e5-large")
		registry.Register("multilingual-e5-large", e)

		codeCfg, ok := embedder.KnownONNXModels()["jina-code-v2"]
		if !ok {
			codeCfg = embedder.ONNXModelConfig{Dim: 768, MaxLen: 512, PadID: 0}
		}
		codeEmb, codeErr := embedder.NewONNXEmbedder(cfg.ONNXModelDirCode, codeCfg, logger)
		if codeErr != nil {
			logger.Warn("code embedder init failed", slog.Any("error", codeErr))
		} else {
			registry.Register("jina-code-v2", codeEmb)
			logger.Info("code embedder loaded",
				slog.String("model", "jina-code-v2"),
				slog.Int("dim", codeCfg.Dim),
			)
		}
		h.SetEmbedRegistry(registry)
	}

	return e
}

// initSearchService creates the SearchService and wires up optional LLM features and profiler.
func initSearchService(
	cfg *config.Config,
	pg *db.Postgres,
	qd *db.Qdrant,
	emb embedder.Embedder,
	rd *db.Redis,
	h *handlers.Handler,
	cacheClient *cache.Client,
	logger *slog.Logger,
) (*search.SearchService, *scheduler.Profiler) {
	svc := search.NewSearchService(pg, qd, emb, logger)

	// Hybrid retrieval (Step D): wire the same SPLADE client used on ingest
	// path (handlers.Handler.SparseEmbedder) into the search service. The
	// embedder field on Handler is set in server.go BEFORE this function runs
	// (initEmbedder → SetSparseEmbedder), so by the time we reach here the
	// embedder is either nil (sparse disabled) or ready. Nil-safe both sides:
	// SearchService.SparseEmbedder=nil disables the sparse leg in
	// spawnSparseTextSearch.
	if sp := h.SparseEmbedder(); sp != nil {
		svc.SetSparseEmbedder(sp)
		logger.Info("hybrid retrieval enabled (SPLADE sparse leg + RRF fusion)")
	}

	// Cross-encoder rerank client (step 6.05). Zero URL disables the step.
	// APIKey supports hosted providers (Cohere/Jina/Voyage/Mixedbread);
	// leave empty for self-hosted TEI/embed-server. MaxCharsPerDoc caps
	// per-doc length (rune-aware) to bound O(seq²) attention compute.
	//
	// M15: optional CircuitBreaker — enabled by MEMDB_RERANK_CIRCUIT=1 (default OFF).
	// When enabled, after MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD consecutive failures
	// the circuit opens for MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S seconds, then
	// allows MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES probe(s) before re-closing.
	// Fail-rate window is MEMDB_RERANK_CIRCUIT_FAIL_WINDOW_S seconds.
	// Circuit-open results in pass-through (input order, Score=0) — search degrades
	// gracefully instead of piling up retries against a dead embed-server.
	rerankOpts := []rerank.Opt{
		rerank.WithModel(cfg.CrossEncoderModel),
		rerank.WithAPIKey(cfg.CrossEncoderAPIKey),
		rerank.WithTimeout(cfg.CrossEncoderTimeout),
		rerank.WithMaxDocs(cfg.CrossEncoderMaxDocs),
		rerank.WithMaxCharsPerDoc(cfg.CrossEncoderMaxCharsPerDoc),
		// 2026-05-01 — disable gokit retry (prior art from go-search engine/init.go).
		// gokit default = 3 attempts × exp backoff 200ms→2s, retries on 5xx/timeout.
		// Problem: every CE call burning 3× timeout budget before MathReranker
		// fallback kicks in. Eval measured 84% degraded fallback even with 8s
		// timeout; retries inflated effective per-call latency past timeout
		// before the deadline fired. With NoRetry, fast-fail to MathReranker
		// (cosine in microseconds) is strictly better than retrying a CE that
		// just timed out — the answer is already in the embedding vectors.
		rerank.WithRetry(rerank.NoRetry),
	}
	// Redis-backed score cache for the cross-encoder. Same cache.Client as
	// the embed cache (separate Redis namespace prefix). Saves the cross-
	// encoder HTTP round trip + ~200-500ms compute on (model, query, doc)
	// triples that recur across query workers (D4/D7/D10 rewrite variants of
	// the same user query, plus the original).
	if cacheClient != nil {
		rerankOpts = append(rerankOpts, rerank.WithCache(localrerank.NewRedisRerankCache(cacheClient, 0, logger)))
		logger.Info("rerank cache enabled (redis-backed)")
	}
	if circuitEnabled() {
		cbCfg := rerank.CircuitConfig{
			FailThreshold:  envcfg.IntRange("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", 5, 1, 1<<30),
			OpenDuration:   time.Duration(envcfg.IntRange("MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S", 30, 1, 1<<30)) * time.Second,
			HalfOpenProbes: envcfg.IntRange("MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES", 1, 1, 1<<30),
			FailRateWindow: time.Duration(envcfg.IntRange("MEMDB_RERANK_CIRCUIT_FAIL_WINDOW_S", 60, 1, 1<<30)) * time.Second,
		}
		rerankOpts = append(rerankOpts, rerank.WithCircuit(cbCfg))
		logger.Info("rerank circuit breaker enabled",
			slog.Int("fail_threshold", cbCfg.FailThreshold),
			slog.Duration("open_duration", cbCfg.OpenDuration),
			slog.Int("half_open_probes", cbCfg.HalfOpenProbes),
			slog.Duration("fail_rate_window", cbCfg.FailRateWindow),
		)
	}
	svc.RerankClient = rerank.NewClient(cfg.CrossEncoderURL, rerankOpts...)
	if svc.RerankClient.Available() {
		logger.Info("cross_encoder rerank enabled",
			slog.String("url", cfg.CrossEncoderURL),
			slog.String("model", cfg.CrossEncoderModel),
			slog.Bool("auth", cfg.CrossEncoderAPIKey != ""),
			slog.Duration("timeout", cfg.CrossEncoderTimeout),
			slog.Int("max_docs", cfg.CrossEncoderMaxDocs),
			slog.Int("max_chars_per_doc", cfg.CrossEncoderMaxCharsPerDoc),
		)
	}

	if cfg.LLMProxyURL != "" {
		svc.LLMReranker = search.LLMRerankConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		svc.Iterative = search.IterativeConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		svc.Enhance = search.EnhanceConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		svc.Fine = search.FineConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		logger.Info("fine search mode enabled")

		// D11 CoT decomposer — env-gated (MEMDB_COT_DECOMPOSE), default OFF.
		// Constructed unconditionally when the LLM proxy is configured so the
		// gate can flip at runtime without a rebuild; Decompose() short-
		// circuits when cfg.Enabled is false.
		svc.CoTDecomposer = search.NewCoTDecomposer(search.CoTDecomposerConfig{
			APIURL:        cfg.LLMProxyURL,
			APIKey:        cfg.LLMProxyAPIKey,
			Model:         cfg.LLMSearchModel,
			Enabled:       cfg.CoTDecompose,
			MaxSubQueries: cfg.CoTMaxSubqueries,
			Timeout:       time.Duration(cfg.CoTTimeoutMS) * time.Millisecond,
		})
		if cfg.CoTDecompose {
			logger.Info("d11 cot decomposer enabled",
				slog.Int("max_subqueries", cfg.CoTMaxSubqueries),
				slog.Int("timeout_ms", cfg.CoTTimeoutMS),
			)
		}
		// F2 reflection-loop deep-search agent — env-gated by MEMDB_F2_REFLECTION
		// (default OFF). Constructed when LLM proxy is configured so flipping
		// the env at runtime activates the stage without a rebuild. Reuses the
		// same LLMSearchModel as the rest of the search service.
		reflectionClient := llm.NewSimpleClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMSearchModel)
		svc.SetReflectionAgent(search.NewReflectionAgent(reflectionClient))
		logger.Info("f2 reflection agent wired (gated by MEMDB_F2_REFLECTION)")

		// D7 (MEMDB_SEARCH_COT) + D11 (MEMDB_COT_DECOMPOSE) both enabled:
		// each query may trigger 2 LLM round-trips + up to 6 extra VectorSearch
		// calls. Fine for ablation; not recommended for production.
		if os.Getenv("MEMDB_SEARCH_COT") == "true" && cfg.CoTDecompose {
			logger.Warn("both D7 (search_cot) and D11 (cot_decompose) enabled — expect 2 LLM round-trips + up to 6 extra VectorSearch calls per query; intended for ablation, not prod")
		}
	}

	if cfg.SearXNGURL != "" {
		bc := initBrowserClient(cfg, logger)
		svc.Internet = search.NewInternetSearcher(search.InternetSearcherConfig{
			SearXNGURL: cfg.SearXNGURL,
			Limit:      search.DefaultInternetLimit,
			Browser:    bc,
		})
		logger.Info("internet search enabled",
			slog.String("searxng_url", cfg.SearXNGURL),
			slog.Bool("direct_scraping", bc != nil),
		)
	}

	var profiler *scheduler.Profiler
	if rd != nil && cfg.LLMProxyURL != "" {
		profiler = scheduler.NewProfiler(pg, rd, cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, logger)
		svc.Profiler = profiler
		h.SetProfiler(profiler)
		logger.Info("user profile summarizer initialized")
	}

	return svc, profiler
}

// circuitEnabled reports whether the rerank circuit breaker is enabled.
// Controlled by MEMDB_RERANK_CIRCUIT=1; default OFF.
func circuitEnabled() bool {
	return os.Getenv("MEMDB_RERANK_CIRCUIT") == "1"
}

// initLLMExtractor creates the LLM extractor for fine-mode native add (non-fatal if URL not set).
func initLLMExtractor(cfg *config.Config, h *handlers.Handler, logger *slog.Logger) *llm.LLMExtractor {
	if cfg.LLMProxyURL == "" {
		return nil
	}
	client := llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMExtractModel, cfg.LLMFallbackModels, logger)
	extractor := llm.NewLLMExtractorWithClient(client)
	h.SetLLMExtractor(extractor)
	logger.Info("llm extractor initialized",
		slog.String("model", extractor.Model()),
		slog.String("url", cfg.LLMProxyURL),
		slog.Any("fallback_models", cfg.LLMFallbackModels),
	)
	return extractor
}
