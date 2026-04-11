package handlers

// memory_delete_filter.go — native delete branches for file_ids and filter.
//
// Split out of memory_delete.go to keep that file under the 200-line budget.
// These helpers are invoked from NativeDelete when the request carries a
// populated file_ids list or filter map. They delegate to Postgres.DeleteByFileIDs
// / Postgres.DeleteByFilter (see internal/db/postgres_filter_delete.go) and
// apply the same post-delete cleanup as the memory_ids branch: Qdrant
// preference eviction, VSET hot-cache eviction and Redis cache invalidation.

import (
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/filter"
)

// resolveDeleteCubeIDs reproduces the Python precedence rule for writable_cube_ids.
// If the request provided an explicit list, use it verbatim. Otherwise fall
// back to [user_id] — MemDB's convention that cube_id == user_id for a solo
// cube. Returns nil when no scoping information is available.
func resolveDeleteCubeIDs(req *deleteNativeRequest) []string {
	if req.WritableCubeIDs != nil && len(*req.WritableCubeIDs) > 0 {
		return *req.WritableCubeIDs
	}
	if req.UserID != nil && *req.UserID != "" {
		return []string{*req.UserID}
	}
	return nil
}

// handleDeleteByFileIDs runs the native file_ids delete path. Assumes the
// caller has already verified hasFileIDs and that postgres != nil.
func (h *Handler) handleDeleteByFileIDs(w http.ResponseWriter, r *http.Request, req *deleteNativeRequest) {
	cubeIDs := resolveDeleteCubeIDs(req)
	if len(cubeIDs) == 0 {
		h.writeValidationError(w, []string{"user_id or writable_cube_ids is required for file_ids delete"})
		return
	}
	ctx := r.Context()
	deleted, ids, err := h.postgres.DeleteByFileIDs(ctx, cubeIDs, *req.FileIDs)
	if err != nil {
		h.logger.Error("native delete by file_ids failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    http.StatusInternalServerError,
			"message": "delete by file_ids failed",
			"data":    map[string]any{"error": err.Error()},
		})
		return
	}
	h.finishNativeDelete(w, r, cubeIDs[0], ids, deleted)
}

// handleDeleteByFilter runs the native filter delete path. Assumes the caller
// has already verified hasFilter and that postgres != nil.
func (h *Handler) handleDeleteByFilter(w http.ResponseWriter, r *http.Request, req *deleteNativeRequest) {
	cubeIDs := resolveDeleteCubeIDs(req)
	if len(cubeIDs) == 0 {
		h.writeValidationError(w, []string{"user_id or writable_cube_ids is required for filter delete"})
		return
	}
	f, err := filter.Parse(req.Filter)
	if err != nil {
		h.writeValidationError(w, []string{"invalid filter: " + err.Error()})
		return
	}
	conditions, err := filter.BuildAGEWhereConditions(f)
	if err != nil {
		h.writeValidationError(w, []string{"invalid filter: " + err.Error()})
		return
	}
	if len(conditions) == 0 {
		h.writeValidationError(w, []string{"filter produced no conditions"})
		return
	}
	ctx := r.Context()
	deleted, ids, err := h.postgres.DeleteByFilter(ctx, cubeIDs, conditions)
	if err != nil {
		h.logger.Error("native delete by filter failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    http.StatusInternalServerError,
			"message": "delete by filter failed",
			"data":    map[string]any{"error": err.Error()},
		})
		return
	}
	h.finishNativeDelete(w, r, cubeIDs[0], ids, deleted)
}

// finishNativeDelete runs the shared post-delete cleanup for file_ids and
// filter branches: Qdrant preference eviction, VSET eviction, Redis cache
// invalidation, and the OK response body. cacheCube is the first cube_id in
// the request — used as the user scope for cache keys. For a multi-cube
// delete the caller is responsible for invalidating the other cubes if needed
// (the current surface area of the handler never emits multi-cube deletes).
func (h *Handler) finishNativeDelete(w http.ResponseWriter, r *http.Request, cacheCube string, ids []string, deleted int64) {
	ctx := r.Context()
	h.deleteFromPreferenceCollections(ctx, ids)
	h.evictFromVSetBatch(ctx, cacheCube, ids)
	h.invalidateDeleteCaches(ctx, cacheCube, ids)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    http.StatusOK,
		"message": "ok",
		"data":    map[string]any{"deleted_count": deleted},
	})
}
