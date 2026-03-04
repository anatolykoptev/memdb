// Package server sets up the HTTP server with all routes and middleware.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/MemDBai/MemDB/memdb-go/internal/cache"
	"github.com/MemDBai/MemDB/memdb-go/internal/config"
	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
	"github.com/MemDBai/MemDB/memdb-go/internal/handlers"
	"github.com/MemDBai/MemDB/memdb-go/internal/llm"
	"github.com/MemDBai/MemDB/memdb-go/internal/rpc"
	"github.com/MemDBai/MemDB/memdb-go/internal/scheduler"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
	mw "github.com/MemDBai/MemDB/memdb-go/internal/server/middleware"
)

// New creates a fully configured HTTP server and returns a cleanup function
// that closes all database connections on shutdown.
// ctx is used to control the lifetime of background workers (scheduler).
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*http.Server, func()) {
	// Initialize cache client (non-fatal if unavailable)
	var cacheClient *cache.Client
	if cfg.CacheEnabled {
		var err error
		cacheClient, err = cache.New(cfg.RedisURL, logger)
		if err != nil {
			logger.Warn("cache disabled: redis unavailable", slog.Any("error", err))
		}
	}

	// Initialize Python proxy client and base handler
	h := handlers.NewHandler(rpc.NewPythonClient(cfg.PythonBackendURL, logger), logger)

	// Initialize database clients for Phase 2 native handlers (non-fatal)
	pg, qd, rd, wmCache := initDBClients(ctx, cfg, h, logger)

	// Initialize embedder via factory (non-fatal: server starts without embedder)
	emb := initEmbedder(cfg, h, logger)

	// Initialize search service with optional LLM features and profiler
	searchSvc, profiler := initSearchService(cfg, pg, qd, emb, rd, h, logger)
	h.SetSearchService(searchSvc)

	// Configure LLM proxy (CLIProxyAPI) and initialize LLM extractor
	handlers.SetLLMProxy(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel)
	extractor := initLLMExtractor(cfg, h, logger)

	// Initialize chat LLM client (reuses CLIProxyAPI config, same default model)
	if cfg.LLMProxyURL != "" {
		chatClient := llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, cfg.LLMFallbackModels, logger)
		h.SetChatLLM(chatClient)
		logger.Info("chat LLM client initialized", slog.String("model", cfg.LLMDefaultModel))
	}

	// Configure buffer zone (batches default-mode adds before LLM extraction)
	if cfg.BufferEnabled {
		h.SetBufferConfig(handlers.BufferConfig{
			Enabled: true,
			Size:    cfg.BufferSize,
			TTL:     cfg.BufferTTL,
		})
		go h.StartBufferFlusher(ctx)
		logger.Info("buffer zone enabled",
			slog.Int("size", cfg.BufferSize),
			slog.Duration("ttl", cfg.BufferTTL))
	}

	// Configure ingestion queue (bounded concurrency for /product/add)
	if cfg.AddWorkers > 0 {
		h.SetAddQueue(cfg.AddWorkers, cfg.AddQueueSize)
		logger.Info("add ingestion queue enabled",
			slog.Int("workers", cfg.AddWorkers),
			slog.Int("queue_size", cfg.AddQueueSize))
	}

	// Start scheduler Worker (after embedder is initialized).
	// Uses its own consumer group (memdb_go_scheduler), independent from Python's scheduler_group.
	if rd != nil {
		reorg := initReorganizer(ctx, cfg, pg, emb, wmCache, extractor, profiler, logger)
		go scheduler.NewWorker(rd.Client(), reorg, logger).Run(ctx)
		h.SetTaskTracker(scheduler.NewTaskStatusTracker(rd.Client()))
		logger.Info("scheduler worker started")
	}

	// Create router and apply middleware
	mux := http.NewServeMux()
	registerRoutes(mux, h)

	srv := &http.Server{
		Addr:         ":" + cfg.PortStr(),
		Handler:      applyMiddleware(mux, cfg, cacheClient, logger),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	return srv, func() {
		h.Close()
		if cacheClient != nil {
			cacheClient.Close()
		}
	}
}

// initEmbedder initializes the embedder via factory (non-fatal if unavailable).
func initEmbedder(cfg *config.Config, h *handlers.Handler, logger *slog.Logger) embedder.Embedder {
	embCfg := embedder.Config{
		Type:         cfg.EmbedderType,
		ONNXModelDir: cfg.ONNXModelDir,
		VoyageAPIKey: cfg.VoyageAPIKey,
		Model:        cfg.EmbedderModel,
		OllamaURL:    cfg.OllamaURL,
		OllamaDim:    cfg.OllamaDim,
		OllamaPrefix: cfg.OllamaPrefix,
		OllamaQuery:  cfg.OllamaQuery,
	}
	e, err := embedder.New(embCfg, logger)
	if err != nil {
		logger.Warn("embedder init failed (native search disabled)", slog.Any("error", err))
		return nil
	}
	h.SetEmbedder(e)
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
	logger *slog.Logger,
) (*search.SearchService, *scheduler.Profiler) {
	svc := search.NewSearchService(pg, qd, emb, logger)

	// Enable LLM reranker + iterative expansion if CLIProxyAPI is configured.
	// Both use the cheaper search model (gemini-2.0-flash by default).
	// Neither fires by default — must be enabled per-request via profile or llm_rerank/num_stages fields.
	if cfg.LLMProxyURL != "" {
		svc.LLMReranker = search.LLMRerankConfig{
			APIURL: cfg.LLMProxyURL,
			APIKey: cfg.LLMProxyAPIKey,
			Model:  cfg.LLMSearchModel,
		}
		svc.Iterative = search.IterativeConfig{
			APIURL: cfg.LLMProxyURL,
			APIKey: cfg.LLMProxyAPIKey,
			Model:  cfg.LLMSearchModel,
		}
		svc.Enhance = search.EnhanceConfig{
			APIURL: cfg.LLMProxyURL,
			APIKey: cfg.LLMProxyAPIKey,
			Model:  cfg.LLMSearchModel,
		}
		svc.Fine = search.FineConfig{
			APIURL: cfg.LLMProxyURL,
			APIKey: cfg.LLMProxyAPIKey,
			Model:  cfg.LLMSearchModel,
		}
		logger.Info("fine search mode enabled")
	}

	// Enable internet search via SearXNG if URL is configured.
	if cfg.SearXNGURL != "" {
		svc.Internet = search.NewInternetSearcher(cfg.SearXNGURL, search.DefaultInternetLimit)
		logger.Info("internet search enabled", slog.String("searxng_url", cfg.SearXNGURL))
	}

	// Enable Memobase-style user profile summaries if both LLM and Redis are available.
	// Profiler generates a paragraph profile from UserMemory nodes and caches it 1hr in Redis.
	// TriggerRefresh is called fire-and-forget from add_fine and async worker after each add operation.
	var profiler *scheduler.Profiler
	if rd != nil && cfg.LLMProxyURL != "" {
		profiler = scheduler.NewProfiler(pg, rd, cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, logger)
		svc.Profiler = profiler
		h.SetProfiler(profiler)
		logger.Info("user profile summarizer initialized")
	}

	return svc, profiler
}

// initLLMExtractor creates the LLM extractor for fine-mode native add (non-fatal if URL not set).
// Shared between HTTP handler (sync fine add) and scheduler worker (async mem_read).
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

// registerRoutes mounts all HTTP handlers on the provided ServeMux.
func registerRoutes(mux *http.ServeMux, h *handlers.Handler) {
	// ─── Health endpoints (native Go, no proxy) ─────────────────────────
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /ready", h.ReadinessCheck)

	// ─── OpenAPI spec + Swagger UI ───────────────────────────────────────
	registerOpenAPIRoutes(mux)

	// ─── OpenAI-compatible embeddings (internal, no auth) ────────────────
	mux.HandleFunc("POST /v1/embeddings", h.OpenAIEmbeddings)

	// ─── Server Router Endpoints (server_router.py — deployed) ─────────

	// Memory CRUD — native or validated
	mux.HandleFunc("POST /product/get_all", h.NativeGetAll)
	mux.HandleFunc("POST /product/add", h.NativeAdd)
	mux.HandleFunc("POST /product/search", h.NativeSearch)

	// Chat — native with proxy fallback (playground stays proxied)
	mux.HandleFunc("POST /product/chat/complete", h.NativeChatComplete)
	mux.HandleFunc("POST /product/chat/stream", h.NativeChatStream)
	mux.HandleFunc("POST /product/chat/stream/playground", h.ProxyToProduct)

	// LLM proxy — direct CLIProxyAPI (no memory retrieval)
	mux.HandleFunc("POST /product/llm/complete", h.ProxyLLMComplete)

	// Suggestions
	mux.HandleFunc("POST /product/suggestions", h.ProxyToProduct)
	mux.HandleFunc("GET /product/suggestions/{user_id}", h.ProxyToProduct)

	// Scheduler — native Go (queries Redis Streams consumer group directly)
	mux.HandleFunc("GET /product/scheduler/allstatus", h.NativeSchedulerAllStatus)
	mux.HandleFunc("GET /product/scheduler/status", h.NativeSchedulerStatus)
	mux.HandleFunc("GET /product/scheduler/task_queue_status", h.NativeSchedulerTaskQueueStatus)
	mux.HandleFunc("POST /product/scheduler/wait", h.NativeSchedulerWait)
	mux.HandleFunc("GET /product/scheduler/wait/stream", h.NativeSchedulerWaitStream)

	// Memory (Server) — native with proxy fallback
	mux.HandleFunc("POST /product/get_memory", h.NativePostGetMemory)
	mux.HandleFunc("GET /product/get_memory/{memory_id}", h.NativeGetMemory)
	mux.HandleFunc("POST /product/get_memory_by_ids", h.NativeGetMemoryByIDs)
	mux.HandleFunc("POST /product/delete_memory", h.NativeDelete)
	mux.HandleFunc("POST /product/delete_all_memories", h.NativeDeleteAll)

	// Feedback — validated
	mux.HandleFunc("POST /product/feedback", h.ValidatedFeedback)

	// Internal — native with proxy fallback
	mux.HandleFunc("POST /product/get_user_names_by_memory_ids", h.NativeGetUserNamesByMemoryIDs)
	mux.HandleFunc("POST /product/exist_mem_cube_id", h.NativeExistMemCube)

	// ─── Product Router Endpoints (product_router.py — migration) ───────

	// Configuration — native stubs with proxy fallback
	mux.HandleFunc("POST /product/configure", h.NativeConfigure)
	mux.HandleFunc("GET /product/configure/{user_id}", h.NativeGetConfig)

	// User management — native with proxy fallback
	mux.HandleFunc("POST /product/users/register", h.NativeRegisterUser)
	mux.HandleFunc("GET /product/users", h.NativeListUsers)
	mux.HandleFunc("GET /product/users/{user_id}", h.NativeGetUser)
	mux.HandleFunc("GET /product/users/{user_id}/config", h.NativeGetUserConfig)
	mux.HandleFunc("PUT /product/users/{user_id}/config", h.NativeUpdateUserConfig)

	// Chat (product_router variant — SSE streaming)
	mux.HandleFunc("POST /product/chat", h.NativeChatStream)

	// Instance monitoring — native
	mux.HandleFunc("GET /product/instances/status", h.NativeInstancesStatus)
	mux.HandleFunc("GET /product/instances/count", h.NativeInstancesCount)

	// ─── Admin endpoints ────────────────────────────────────────────────
	mux.HandleFunc("POST /product/admin/reprocess", h.AdminReprocess)
}

// applyMiddleware wraps the handler with the full middleware stack.
// Order: outermost wrapper first → innermost last.
func applyMiddleware(next http.Handler, cfg *config.Config, cacheClient *cache.Client, logger *slog.Logger) http.Handler {
	h := next
	h = mw.Cache(logger, mw.CacheConfig{Client: cacheClient})(h)
	h = mw.OTel(logger, cfg.OTelEnabled)(h)
	h = mw.RateLimit(logger, mw.RateLimitConfig{
		Enabled:       cfg.RateLimitEnabled,
		RPS:           cfg.RateLimitRPS,
		Burst:         cfg.RateLimitBurst,
		ServiceSecret: cfg.InternalServiceSecret,
	})(h)
	h = mw.Auth(logger, mw.AuthConfig{
		Enabled:       cfg.AuthEnabled,
		MasterKeyHash: cfg.MasterKeyHash,
		ServiceSecret: cfg.InternalServiceSecret,
	})(h)
	h = mw.CORS(h)
	h = mw.Logging(logger)(h)
	h = mw.RequestID(h)
	h = mw.Recovery(logger)(h)
	return h
}
