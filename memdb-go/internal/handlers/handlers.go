// Package handlers implements HTTP handlers for the MemDB Go API.
// Phase 1: All endpoints proxy to the Python backend.
// Phase 2+: Handlers will be replaced with native Go implementations.
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/rpc"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	python   *rpc.PythonClient
	logger   *slog.Logger
	postgres *db.Postgres // nil = not initialized, fall back to proxy
	qdrant   *db.Qdrant   // nil = not initialized
	redis    *db.Redis    // nil = not initialized
}

// NewHandler creates a new Handler with the given dependencies.
func NewHandler(python *rpc.PythonClient, logger *slog.Logger) *Handler {
	return &Handler{
		python: python,
		logger: logger,
	}
}

// SetDBClients sets optional database clients for native handlers.
// When set, supported endpoints use direct DB access instead of proxying.
func (h *Handler) SetDBClients(pg *db.Postgres, qd *db.Qdrant, rd *db.Redis) {
	h.postgres = pg
	h.qdrant = qd
	h.redis = rd
}

// --- Health endpoints (handled directly by Go, no proxy) ---

// Health returns a simple health check response.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"service":    "memdb-go",
			"status":     "healthy",
			"go_version": runtime.Version(),
			"hostname":   hostname,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// ReadinessCheck verifies that the Python backend and all configured databases are reachable.
func (h *Handler) ReadinessCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	checks := map[string]string{}

	// Python backend
	if err := h.python.HealthCheck(ctx); err != nil {
		checks["python"] = err.Error()
	} else {
		checks["python"] = "ok"
	}

	// Native DB clients (only check if initialized)
	if h.postgres != nil {
		if err := h.postgres.Ping(ctx); err != nil {
			checks["postgres"] = err.Error()
		} else {
			checks["postgres"] = "ok"
		}
	}
	if h.qdrant != nil {
		if err := h.qdrant.Ping(ctx); err != nil {
			checks["qdrant"] = err.Error()
		} else {
			checks["qdrant"] = "ok"
		}
	}
	if h.redis != nil {
		if err := h.redis.Ping(ctx); err != nil {
			checks["redis"] = err.Error()
		} else {
			checks["redis"] = "ok"
		}
	}

	// If Python is down, report 503
	if checks["python"] != "ok" {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "backend unavailable",
			"data":    checks,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ready",
		"data":    checks,
	})
}

// --- Proxy endpoints (Phase 1: forward everything to Python) ---

// ProxyToProduct proxies requests to the Python /product/* endpoints.
func (h *Handler) ProxyToProduct(w http.ResponseWriter, r *http.Request) {
	h.python.ProxyRequest(r.Context(), w, r)
}

// --- Helpers ---

func (h *Handler) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		h.logger.Error("failed to encode JSON response", slog.Any("error", err))
	}
}
