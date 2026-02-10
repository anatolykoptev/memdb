// Package server sets up the HTTP server with all routes and middleware.
package server

import (
	"log/slog"
	"net/http"

	"github.com/memtensor/memdb-go/internal/config"
	"github.com/memtensor/memdb-go/internal/handlers"
	"github.com/memtensor/memdb-go/internal/rpc"
	mw "github.com/memtensor/memdb-go/internal/server/middleware"
)

// New creates a fully configured HTTP server.
func New(cfg *config.Config, logger *slog.Logger) *http.Server {
	// Initialize Python proxy client
	pythonClient := rpc.NewPythonClient(cfg.PythonBackendURL, logger)

	// Initialize handlers
	h := handlers.NewHandler(pythonClient, logger)

	// Create router using Go 1.22+ stdlib ServeMux
	mux := http.NewServeMux()

	// ─── Health endpoints (native Go, no proxy) ─────────────────────────
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /ready", h.ReadinessCheck)

	// ─── Product Router (maps to product_router.py) ─────────────────────

	// Configuration
	mux.HandleFunc("POST /product/configure", h.ProxyToProduct)
	mux.HandleFunc("GET /product/configure/{user_id}", h.ProxyToProduct)

	// User management
	mux.HandleFunc("POST /product/users/register", h.ProxyToProduct)
	mux.HandleFunc("GET /product/users", h.ProxyToProduct)
	mux.HandleFunc("GET /product/users/{user_id}", h.ProxyToProduct)
	mux.HandleFunc("GET /product/users/{user_id}/config", h.ProxyToProduct)
	mux.HandleFunc("PUT /product/users/{user_id}/config", h.ProxyToProduct)

	// Suggestions
	mux.HandleFunc("GET /product/suggestions/{user_id}", h.ProxyToProduct)
	mux.HandleFunc("POST /product/suggestions", h.ProxyToProduct)

	// Memory CRUD (Product)
	mux.HandleFunc("POST /product/get_all", h.ProxyToProduct)
	mux.HandleFunc("POST /product/add", h.ProxyToProduct)
	mux.HandleFunc("POST /product/search", h.ProxyToProduct)

	// Chat (Product)
	mux.HandleFunc("POST /product/chat", h.ProxyToProduct)
	mux.HandleFunc("POST /product/chat/complete", h.ProxyToProduct)
	mux.HandleFunc("POST /product/chat/stream", h.ProxyToProduct)
	mux.HandleFunc("POST /product/chat/stream/playground", h.ProxyToProduct)

	// Instance info
	mux.HandleFunc("GET /product/instances/status", h.ProxyToProduct)
	mux.HandleFunc("GET /product/instances/count", h.ProxyToProduct)

	// ─── Server Router (maps to server_router.py) ───────────────────────

	// Scheduler
	mux.HandleFunc("GET /product/scheduler/allstatus", h.ProxyToProduct)
	mux.HandleFunc("GET /product/scheduler/status", h.ProxyToProduct)
	mux.HandleFunc("GET /product/scheduler/task_queue_status", h.ProxyToProduct)
	mux.HandleFunc("POST /product/scheduler/wait", h.ProxyToProduct)
	mux.HandleFunc("GET /product/scheduler/wait/stream", h.ProxyToProduct)

	// Memory (Server)
	mux.HandleFunc("POST /product/get_memory", h.ProxyToProduct)
	mux.HandleFunc("GET /product/get_memory/{memory_id}", h.ProxyToProduct)
	mux.HandleFunc("POST /product/get_memory_by_ids", h.ProxyToProduct)
	mux.HandleFunc("POST /product/delete_memory", h.ProxyToProduct)

	// Feedback
	mux.HandleFunc("POST /product/feedback", h.ProxyToProduct)

	// Internal
	mux.HandleFunc("POST /product/get_user_names_by_memory_ids", h.ProxyToProduct)
	mux.HandleFunc("POST /product/exist_mem_cube_id", h.ProxyToProduct)

	// ─── Apply middleware stack ──────────────────────────────────────────
	// Order: outermost wrapper first → innermost last
	var handler http.Handler = mux
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
