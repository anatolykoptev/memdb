// Package server sets up the HTTP server with all routes and middleware.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/MemDBai/MemDB/memdb-go/internal/cache"
	"github.com/MemDBai/MemDB/memdb-go/internal/config"
	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/handlers"
	"github.com/MemDBai/MemDB/memdb-go/internal/rpc"
	mw "github.com/MemDBai/MemDB/memdb-go/internal/server/middleware"
)

// New creates a fully configured HTTP server.
func New(cfg *config.Config, logger *slog.Logger) *http.Server {
	// Initialize cache client (non-fatal if unavailable)
	var cacheClient *cache.Client
	if cfg.CacheEnabled {
		var err error
		cacheClient, err = cache.New(cfg.RedisURL, logger)
		if err != nil {
			logger.Warn("cache disabled: redis unavailable", slog.Any("error", err))
		}
	}

	// Initialize Python proxy client
	pythonClient := rpc.NewPythonClient(cfg.PythonBackendURL, logger)

	// Initialize handlers
	h := handlers.NewHandler(pythonClient, logger)

	// Initialize database clients for Phase 2 native handlers (non-fatal)
	initDBClients(cfg, h, logger)

	// Create router using Go 1.22+ stdlib ServeMux
	mux := http.NewServeMux()

	// ─── Health endpoints (native Go, no proxy) ─────────────────────────
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /ready", h.ReadinessCheck)

	// ─── Server Router Endpoints (server_router.py — deployed) ─────────

	// Memory CRUD — validated
	mux.HandleFunc("POST /product/get_all", h.ValidatedGetAll)
	mux.HandleFunc("POST /product/add", h.ValidatedAdd)
	mux.HandleFunc("POST /product/search", h.ValidatedSearch)

	// Chat — validated
	mux.HandleFunc("POST /product/chat/complete", h.ValidatedChatComplete)
	mux.HandleFunc("POST /product/chat/stream", h.ValidatedChatStream)
	mux.HandleFunc("POST /product/chat/stream/playground", h.ProxyToProduct)

	// Suggestions
	mux.HandleFunc("POST /product/suggestions", h.ProxyToProduct)
	mux.HandleFunc("GET /product/suggestions/{user_id}", h.ProxyToProduct)

	// Scheduler
	mux.HandleFunc("GET /product/scheduler/allstatus", h.ProxyToProduct)
	mux.HandleFunc("GET /product/scheduler/status", h.ProxyToProduct)
	mux.HandleFunc("GET /product/scheduler/task_queue_status", h.ProxyToProduct)
	mux.HandleFunc("POST /product/scheduler/wait", h.ProxyToProduct)
	mux.HandleFunc("GET /product/scheduler/wait/stream", h.ProxyToProduct)

	// Memory (Server) — native with proxy fallback
	mux.HandleFunc("POST /product/get_memory", h.ValidatedGetMemory)
	mux.HandleFunc("GET /product/get_memory/{memory_id}", h.NativeGetMemory)
	mux.HandleFunc("POST /product/get_memory_by_ids", h.NativeGetMemoryByIDs)
	mux.HandleFunc("POST /product/delete_memory", h.ValidatedDelete)

	// Feedback — validated
	mux.HandleFunc("POST /product/feedback", h.ValidatedFeedback)

	// Internal
	mux.HandleFunc("POST /product/get_user_names_by_memory_ids", h.ProxyToProduct)
	mux.HandleFunc("POST /product/exist_mem_cube_id", h.ValidatedExistMemCube)

	// ─── Product Router Endpoints (product_router.py — migration) ───────

	// Configuration
	mux.HandleFunc("POST /product/configure", h.ProxyToProduct)
	mux.HandleFunc("GET /product/configure/{user_id}", h.ProxyToProduct)

	// User management
	mux.HandleFunc("POST /product/users/register", h.ProxyToProduct)
	mux.HandleFunc("GET /product/users", h.ProxyToProduct)
	mux.HandleFunc("GET /product/users/{user_id}", h.ProxyToProduct)
	mux.HandleFunc("GET /product/users/{user_id}/config", h.ProxyToProduct)
	mux.HandleFunc("PUT /product/users/{user_id}/config", h.ProxyToProduct)

	// Chat (product_router variant — SSE streaming)
	mux.HandleFunc("POST /product/chat", h.ValidatedChatStream)

	// Instance monitoring
	mux.HandleFunc("GET /product/instances/status", h.ProxyToProduct)
	mux.HandleFunc("GET /product/instances/count", h.ProxyToProduct)

	// ─── Apply middleware stack ──────────────────────────────────────────
	// Order: outermost wrapper first → innermost last
	var handler http.Handler = mux
	handler = mw.Cache(logger, mw.CacheConfig{Client: cacheClient})(handler)
	handler = mw.OTel(logger, cfg.OTelEnabled)(handler)
	handler = mw.RateLimit(logger, mw.RateLimitConfig{
		Enabled:       cfg.RateLimitEnabled,
		RPS:           cfg.RateLimitRPS,
		Burst:         cfg.RateLimitBurst,
		ServiceSecret: cfg.InternalServiceSecret,
	})(handler)
	handler = mw.Auth(logger, mw.AuthConfig{
		Enabled:       cfg.AuthEnabled,
		MasterKeyHash: cfg.MasterKeyHash,
		ServiceSecret: cfg.InternalServiceSecret,
	})(handler)
	handler = mw.CORS(handler)
	handler = mw.Logging(logger)(handler)
	handler = mw.RequestID(handler)
	handler = mw.Recovery(logger)(handler)

	return &http.Server{
		Addr:         ":" + cfg.PortStr(),
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
}

// initDBClients connects to databases for native handlers.
// All connections are optional — handlers fall back to proxy if nil.
func initDBClients(cfg *config.Config, h *handlers.Handler, logger *slog.Logger) {
	ctx := context.Background()

	var pg *db.Postgres
	var qd *db.Qdrant
	var rd *db.Redis

	if cfg.PostgresURL != "" {
		var err error
		pg, err = db.NewPostgres(ctx, cfg.PostgresURL, logger)
		if err != nil {
			logger.Warn("postgres unavailable, native handlers will proxy", slog.Any("error", err))
		}
	}

	if cfg.QdrantAddr != "" {
		var err error
		qd, err = db.NewQdrant(ctx, cfg.QdrantAddr, logger)
		if err != nil {
			logger.Warn("qdrant unavailable", slog.Any("error", err))
		}
	}

	if cfg.DBRedisURL != "" {
		var err error
		rd, err = db.NewRedis(ctx, cfg.DBRedisURL, logger)
		if err != nil {
			logger.Warn("redis unavailable", slog.Any("error", err))
		}
	}

	if pg != nil || qd != nil || rd != nil {
		h.SetDBClients(pg, qd, rd)
		logger.Info("native db clients initialized",
			slog.Bool("postgres", pg != nil),
			slog.Bool("qdrant", qd != nil),
			slog.Bool("redis", rd != nil),
		)
	}
}
