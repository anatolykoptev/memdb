package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// NativeConfigure handles POST /product/configure.
// Stub: configuration is managed by environment variables in Go gateway.
func (h *Handler) NativeConfigure(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "configuration is managed via environment variables",
		"data":    nil,
	})
}

// NativeGetConfig handles GET /product/configure/{user_id}.
// Stub: returns empty config since Go gateway uses env vars.
func (h *Handler) NativeGetConfig(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}
	userID := r.PathValue("user_id")
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"user_id": userID, "config": map[string]any{}},
	})
}

// NativeGetUserConfig handles GET /product/users/{user_id}/config.
func (h *Handler) NativeGetUserConfig(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}
	userID := r.PathValue("user_id")
	config, err := h.postgres.GetUserConfig(r.Context(), userID)
	if err != nil {
		h.logger.Debug("native get_user_config failed", slog.Any("error", err))
		h.ProxyToProduct(w, r)
		return
	}
	if config == nil {
		config = map[string]any{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"user_id": userID, "config": config},
	})
}

// NativeUpdateUserConfig handles PUT /product/users/{user_id}/config.
func (h *Handler) NativeUpdateUserConfig(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	userID := r.PathValue("user_id")

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		h.writeValidationError(w, []string{"invalid JSON body: " + err.Error()})
		return
	}

	if err := h.postgres.UpdateUserConfig(r.Context(), userID, cfg); err != nil {
		h.logger.Debug("native update_user_config failed", slog.Any("error", err))
		h.proxyWithBody(w, r, body)
		return
	}

	h.cacheDelete(r.Context(), cachePrefix+"config:"+userID)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"user_id": userID, "config": cfg},
	})
}
