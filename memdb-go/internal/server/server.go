// Package server sets up the HTTP server with all routes and middleware.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/rpc"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
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

	// Initialize LLM extractor (chat client with LLM API config is wired below).
	extractor := initLLMExtractor(cfg, h, logger)

	// Initialize chat LLM client (reuses LLM API config, same default model)
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
	if rd != nil {
		reorg := initReorganizer(ctx, cfg, pg, rd, emb, wmCache, extractor, profiler, logger)
		go scheduler.NewWorker(rd.Client(), reorg, logger).Run(ctx)
		if reorg != nil {
			h.SetReorganizer(reorg)
		}
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
