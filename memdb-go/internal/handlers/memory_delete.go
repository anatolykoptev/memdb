package handlers

// memory_delete.go — native DELETE handler: delete memories by property UUIDs.

import (
	"context"
	"log/slog"
	"net/http"
)

// deleteNativeRequest extends deleteRequest with user_id for native handling.
type deleteNativeRequest struct {
	UserID    *string                `json:"user_id"`
	MemoryIDs *[]string              `json:"memory_ids"`
	FileIDs   *[]string              `json:"file_ids"`
	Filter    map[string]interface{} `json:"filter"`
}

// NativeDelete handles POST /product/delete_memory natively via PostgreSQL+Qdrant.
// Falls back to Python proxy if Postgres is not initialized or for complex filters/file_ids.
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

	// Complex cases: fall back to proxy.
	if hasFileIDs || hasFilter {
		h.proxyWithBody(w, r, body)
		return
	}

	// Native path: delete by memory_ids — requires user_id.
	if req.UserID == nil || *req.UserID == "" {
		h.proxyWithBody(w, r, body)
		return
	}

	ctx := r.Context()
	deleted, err := h.postgres.DeleteByPropertyIDs(ctx, *req.MemoryIDs, *req.UserID)
	if err != nil {
		h.logger.Debug("native delete failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	h.deleteFromPreferenceCollections(r.Context(), *req.MemoryIDs)
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
			h.logger.Debug("qdrant delete from preference collection failed",
				slog.String("collection", collection),
				slog.Any("error", err),
			)
		}
	}
}

// invalidateDeleteCaches invalidates get_all, users, and per-memory caches after deletion.
func (h *Handler) invalidateDeleteCaches(ctx context.Context, userID string, ids []string) {
	patterns := make([]string, 0, 2+len(ids))
	patterns = append(patterns, cachePrefix+"get_all:"+userID+":*", cachePrefix+"users:*")
	for _, id := range ids {
		patterns = append(patterns, cachePrefix+"memory:"+id)
	}
	h.cacheInvalidate(ctx, patterns...)
}
