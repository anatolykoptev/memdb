package handlers

// memory_delete.go — native DELETE handler: delete memories by property UUIDs.

import (
	"context"
	"log/slog"
	"net/http"
)

// deleteNativeRequest extends deleteRequest with user_id for native handling.
// WritableCubeIDs mirrors Python's DeleteMemoryRequest.writable_cube_ids — when
// present it takes precedence over UserID for user_name scoping of file_ids
// and filter deletes.
type deleteNativeRequest struct {
	UserID          *string                `json:"user_id"`
	WritableCubeIDs *[]string              `json:"writable_cube_ids"`
	MemoryIDs       *[]string              `json:"memory_ids"`
	FileIDs         *[]string              `json:"file_ids"`
	Filter          map[string]interface{} `json:"filter"`
}

// NativeDelete handles POST /product/delete_memory natively via PostgreSQL+Qdrant.
// Supports three mutually exclusive delete shapes: memory_ids, file_ids, filter.
// Falls back to the Python proxy ONLY when postgres is not initialised
// (safety net — normal production path is always native).
func (h *Handler) NativeDelete(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedDelete(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req deleteNativeRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	hasMemoryIDs := req.MemoryIDs != nil && len(*req.MemoryIDs) > 0
	hasFileIDs := req.FileIDs != nil && len(*req.FileIDs) > 0
	hasFilter := len(req.Filter) > 0

	if !hasMemoryIDs && !hasFileIDs && !hasFilter {
		h.writeValidationError(w, []string{"at least one of memory_ids, file_ids, or filter is required"})
		return
	}
	// Mirror Python memory_handler:handle_delete_memories — exactly one shape.
	provided := 0
	if hasMemoryIDs {
		provided++
	}
	if hasFileIDs {
		provided++
	}
	if hasFilter {
		provided++
	}
	if provided > 1 {
		h.writeValidationError(w, []string{"exactly one of memory_ids, file_ids, or filter must be provided"})
		return
	}

	if hasFileIDs {
		h.handleDeleteByFileIDs(w, r, &req)
		return
	}
	if hasFilter {
		h.handleDeleteByFilter(w, r, &req)
		return
	}

	// Native path: delete by memory_ids — requires user_id.
	if req.UserID == nil || *req.UserID == "" {
		h.writeValidationError(w, []string{"user_id is required for memory_ids delete"})
		return
	}

	ctx := r.Context()
	deleted, err := h.postgres.DeleteByPropertyIDs(ctx, *req.MemoryIDs, *req.UserID)
	if err != nil {
		h.logger.Error("native delete by memory_ids failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    http.StatusInternalServerError,
			"message": "delete by memory_ids failed",
			"data":    map[string]any{"error": err.Error()},
		})
		return
	}

	h.deleteFromPreferenceCollections(r.Context(), *req.MemoryIDs)
	h.evictFromVSetBatch(ctx, *req.UserID, *req.MemoryIDs)
	h.invalidateDeleteCaches(ctx, *req.UserID, *req.MemoryIDs)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    map[string]any{"deleted_count": deleted},
	})
}

// deleteFromPreferenceCollections deletes from Qdrant preference collections (best-effort).
func (h *Handler) deleteFromPreferenceCollections(ctx context.Context, ids []string) {
	if h.qdrant == nil {
		return
	}
	for _, collection := range []string{"explicit_preference", "implicit_preference"} {
		if err := h.qdrant.DeleteByIDs(ctx, collection, ids); err != nil {
			h.logger.Warn("qdrant delete from preference collection failed",
				slog.String("collection", collection),
				slog.Any("error", err),
			)
		}
	}
}

// evictFromVSetBatch removes deleted memory IDs from the VSET hot cache (non-fatal).
func (h *Handler) evictFromVSetBatch(ctx context.Context, cubeID string, ids []string) {
	if h.wmCache == nil || len(ids) == 0 {
		return
	}
	if err := h.wmCache.VRemBatch(ctx, cubeID, ids); err != nil {
		h.logger.Warn("vset evict batch failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
	}
}

// invalidateDeleteCaches invalidates get_all, search, users, and per-memory caches after deletion.
func (h *Handler) invalidateDeleteCaches(ctx context.Context, userID string, ids []string) {
	patterns := make([]string, 0, 4+len(ids))
	patterns = append(patterns,
		cachePrefix+"get_all:"+userID+":*",
		cachePrefix+"search:*:"+userID+":*",
		cachePrefix+"users:*",
	)
	for _, id := range ids {
		patterns = append(patterns, cachePrefix+"memory:"+id)
	}
	h.cacheInvalidate(ctx, patterns...)
}
