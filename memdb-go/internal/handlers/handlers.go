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

	"github.com/memtensor/memdb-go/internal/rpc"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	python *rpc.PythonClient
	logger *slog.Logger
}

// NewHandler creates a new Handler with the given dependencies.
func NewHandler(python *rpc.PythonClient, logger *slog.Logger) *Handler {
	return &Handler{
		python: python,
		logger: logger,
	}
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

// ReadinessCheck verifies that the Python backend is reachable.
func (h *Handler) ReadinessCheck(w http.ResponseWriter, r *http.Request) {
	if err := h.python.HealthCheck(r.Context()); err != nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "backend unavailable",
			"data":    map[string]string{"error": err.Error()},
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ready",
		"data":    nil,
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
	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.logger.Error("failed to encode JSON response", slog.Any("error", err))
	}
}
