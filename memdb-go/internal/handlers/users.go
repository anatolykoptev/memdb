package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Cache TTL for user-related queries (users list, instance count).
const usersCacheTTL = 120 * time.Second

// NativeListUsers handles GET /product/users natively via PostgreSQL.
// Phase 2: returns distinct person identities (user_id slot) shaped as upstream MemOS MOS.list_users().
func (h *Handler) NativeListUsers(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code": 503, "message": "postgres unavailable",
		})
		return
	}

	identities, err := h.postgres.ListDistinctUserIDs(r.Context())
	if err != nil {
		h.logger.Error("list_users query failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "query failed",
		})
		return
	}

	type userRow struct {
		UserID    string    `json:"user_id"`
		UserName  string    `json:"user_name"`
		Role      string    `json:"role"`
		IsActive  bool      `json:"is_active"`
		CreatedAt time.Time `json:"created_at"`
	}
	users := make([]userRow, 0, len(identities))
	for _, u := range identities {
		users = append(users, userRow{
			UserID:    u.UserID,
			UserName:  u.UserID, // single-user deployment — no separate display name
			Role:      "user",
			IsActive:  true,
			CreatedAt: u.FirstSeen,
		})
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    map[string]any{"users": users},
	})
}

// NativeGetUser handles GET /product/users/{user_id} natively via PostgreSQL.
func (h *Handler) NativeGetUser(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	userID := r.PathValue("user_id")
	if userID == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "message": "user_id is required", "data": nil,
		})
		return
	}

	exists, err := h.postgres.ExistUser(r.Context(), userID)
	if err != nil {
		h.logger.Debug("native get_user failed",
			slog.String("user_id", userID), slog.Any("error", err))
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"user_id": userID, "exists": exists},
	})
}

// NativeGetUserInfo handles POST /product/get_user_info.
// Phase 2: returns user metadata plus accessible_cubes (full cube objects via ListCubes).
func (h *Handler) NativeGetUserInfo(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil || h.cubeStore == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code": 503, "message": "cube store unavailable",
		})
		return
	}

	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		h.writeValidationError(w, []string{"user_id is required"})
		return
	}

	cubes, err := h.cubeStore.ListCubes(r.Context(), &req.UserID)
	if err != nil {
		h.logger.Error("get_user_info: list cubes failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "failed",
		})
		return
	}

	accessible := make([]map[string]any, 0, len(cubes))
	for _, c := range cubes {
		accessible = append(accessible, cubeToMap(c))
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"user_id":          req.UserID,
			"user_name":        req.UserID, // single-user: same as user_id
			"role":             "user",
			"is_active":        true,
			"accessible_cubes": accessible,
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
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
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
		"code": 200, "message": "ok",
		"data": map[string]any{"user_id": userID, "registered": true},
	})
}
