package handlers

// memory_delete_all.go — native handler for POST /product/delete_all_memories.
//
// Split out of memory_delete.go so the primary delete handler stays under the
// 200-line budget. Also owns purgeUserPreferences and dropVSet, which are
// delete-all-specific cleanup helpers.

import (
	"context"
	"log/slog"
	"net/http"
)

// deleteAllRequest for the delete_all_memories REST endpoint.
type deleteAllRequest struct {
	UserID *string `json:"user_id"`
}

// NativeDeleteAll handles POST /product/delete_all_memories natively.
// Deletes all activated memories from Postgres AND purges Qdrant preference collections.
func (h *Handler) NativeDeleteAll(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req deleteAllRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.UserID == nil || *req.UserID == "" {
		h.writeValidationError(w, []string{"user_id is required"})
		return
	}

	ctx := r.Context()
	userID := *req.UserID

	deleted, err := h.postgres.DeleteAllByUser(ctx, userID)
	if err != nil {
		h.logger.Warn("native delete_all failed", slog.Any("error", err))
		h.proxyWithBody(w, r, body)
		return
	}

	// Purge Qdrant preference collections to prevent ghost vectors.
	h.purgeUserPreferences(ctx, userID)
	h.dropVSet(ctx, userID)
	h.cacheInvalidate(ctx,
		cachePrefix+"get_all:"+userID+":*",
		cachePrefix+"search:*:"+userID+":*",
		cachePrefix+"users:*",
	)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    http.StatusOK,
		"message": "ok",
		"data":    map[string]any{"deleted_count": deleted},
	})
}

// purgeUserPreferences deletes ALL points for a user from Qdrant preference collections.
// Used by delete-all operations to ensure no ghost vectors remain.
func (h *Handler) purgeUserPreferences(ctx context.Context, userID string) {
	if h.qdrant == nil {
		return
	}
	for _, collection := range []string{"explicit_preference", "implicit_preference"} {
		if err := h.qdrant.PurgeByUserID(ctx, collection, userID); err != nil {
			h.logger.Warn("qdrant purge user preferences failed",
				slog.String("collection", collection),
				slog.String("user_id", userID),
				slog.Any("error", err),
			)
		}
	}
}

// dropVSet removes the entire VSET for a cube (used by delete-all).
func (h *Handler) dropVSet(ctx context.Context, cubeID string) {
	if h.wmCache == nil {
		return
	}
	if err := h.wmCache.VDrop(ctx, cubeID); err != nil {
		h.logger.Warn("vset drop failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
	}
}
