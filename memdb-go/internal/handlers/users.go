package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"
)

// Cache TTL for user-related queries (users list, instance count).
const usersCacheTTL = 120 * time.Second

// NativeListUsers handles GET /product/users natively via PostgreSQL.
func (h *Handler) NativeListUsers(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	ctx := r.Context()
	cacheKey := cachePrefix + "users:list"

	// Check cache
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached) //nolint:errcheck
		return
	}

	users, err := h.postgres.ListUsers(ctx)
	if err != nil {
		h.logger.Debug("native list_users failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.ProxyToProduct(w, r)
		return
	}
	if users == nil {
		users = []string{}
	}

	resp := map[string]any{
		"code":    200,
		"message": "ok",
		"data":    users,
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, usersCacheTTL)
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// NativeGetUser handles GET /product/users/{user_id} natively via PostgreSQL.
func (h *Handler) NativeGetUser(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	userID := r.PathValue("user_id")
	if userID == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    400,
			"message": "user_id is required",
			"data":    nil,
		})
		return
	}

	exists, err := h.postgres.ExistUser(r.Context(), userID)
	if err != nil {
		h.logger.Debug("native get_user failed, falling back to proxy",
			slog.String("user_id", userID),
			slog.Any("error", err),
		)
		h.ProxyToProduct(w, r)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"user_id": userID,
			"exists":  exists,
		},
	})
}

// registerRequest validates POST /product/users/register.
type registerRequest struct {
	UserID *string `json:"user_id"`
}

// NativeRegisterUser handles POST /product/users/register.
// Stub: echoes back user_id. No DB write — MemDB auto-creates users on first add.
func (h *Handler) NativeRegisterUser(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req registerRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	userID := "default"
	if req.UserID != nil && *req.UserID != "" {
		userID = *req.UserID
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"user_id":    userID,
			"registered": true,
		},
	})
}

// NativeInstancesStatus handles GET /product/instances/status.
// Returns hardcoded status since Go gateway is always running.
func (h *Handler) NativeInstancesStatus(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"status":     "running",
			"hostname":   hostname,
			"go_version": runtime.Version(),
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// NativeInstancesCount handles GET /product/instances/count natively via PostgreSQL.
func (h *Handler) NativeInstancesCount(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	ctx := r.Context()
	cacheKey := cachePrefix + "users:count"

	// Check cache
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached) //nolint:errcheck
		return
	}

	count, err := h.postgres.CountDistinctUsers(ctx)
	if err != nil {
		h.logger.Debug("native instances_count failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.ProxyToProduct(w, r)
		return
	}

	resp := map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"count": count,
		},
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, usersCacheTTL)
	}

	h.writeJSON(w, http.StatusOK, resp)
}

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
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"user_id": userID,
			"config":  map[string]any{},
		},
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
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"user_id": userID,
			"config":  config,
		},
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

	// Invalidate the fast cache
	h.cacheDelete(r.Context(), cachePrefix+"config:"+userID)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"user_id": userID,
			"config":  cfg,
		},
	})
}

// NativeExistMemCube handles POST /product/exist_mem_cube_id natively via PostgreSQL.
func (h *Handler) NativeExistMemCube(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedExistMemCube(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req existMemCubeRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemCubeID == nil || *req.MemCubeID == "" {
		h.writeValidationError(w, []string{"mem_cube_id is required"})
		return
	}

	exists, err := h.postgres.ExistUser(r.Context(), *req.MemCubeID)
	if err != nil {
		h.logger.Debug("native exist_mem_cube_id failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    exists,
	})
}

// getUserNamesByMemoryIDsRequest validates POST /product/get_user_names_by_memory_ids.
type getUserNamesByMemoryIDsRequest struct {
	MemoryIDs *[]string `json:"memory_ids"`
}

// NativeGetUserNamesByMemoryIDs handles POST /product/get_user_names_by_memory_ids natively.
func (h *Handler) NativeGetUserNamesByMemoryIDs(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getUserNamesByMemoryIDsRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemoryIDs == nil || len(*req.MemoryIDs) == 0 {
		h.writeValidationError(w, []string{"memory_ids is required and must be non-empty"})
		return
	}

	result, err := h.postgres.GetUserNamesByMemoryIDs(r.Context(), *req.MemoryIDs)
	if err != nil {
		h.logger.Debug("native get_user_names_by_memory_ids failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    result,
	})
}
