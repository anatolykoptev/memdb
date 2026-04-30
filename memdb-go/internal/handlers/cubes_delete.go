package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// deleteCubeExec performs the actual soft- or hard-delete after the caller has
// already verified ownership. Called by NativeDeleteCube.
//
// After a successful Postgres delete it invalidates the cube's VSET hot-cache
// in Redis (wmCache.VDrop). Cache invalidation failure is non-fatal: Postgres
// is already gone and the VSET will naturally expire via vsetTTL (30 days).
// A warning is logged and the memdb.cache.vset_invalidate_total{outcome=error}
// counter is bumped so operators can detect and alert on the condition.
func (h *Handler) deleteCubeExec(w http.ResponseWriter, r *http.Request, cubeID, ownerID, requestedBy string, hard bool) {
	ctx := r.Context()
	if hard {
		h.logger.Warn("delete_cube HARD DELETE requested",
			slog.String("cube_id", cubeID),
			slog.String("owner_id", ownerID),
			slog.String("requested_by", requestedBy),
		)
		n, err := h.cubeStore.HardDeleteCube(ctx, cubeID)
		if err != nil {
			h.logger.Error("hard delete failed", slog.Any("error", err))
			h.writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "hard delete failed"})
			return
		}
		h.dropVsetCache(ctx, cubeID)
		h.writeJSON(w, http.StatusOK, map[string]any{
			"code": 200, "message": "ok",
			"data": map[string]any{"memories_deleted": n, "hard_delete": true},
		})
		return
	}
	if err := h.cubeStore.SoftDeleteCube(ctx, cubeID); err != nil {
		h.logger.Error("soft delete failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "soft delete failed"})
		return
	}
	h.dropVsetCache(ctx, cubeID)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"memories_deleted": int64(0), "hard_delete": false},
	})
}

// dropVsetCache invalidates the VSET hot-cache for cubeID after a delete.
// Non-fatal: failure is logged at Warn and metered; the request is NOT aborted.
// Outcome label mapping:
//
//	success — VDrop returned nil
//	error   — VDrop returned a non-nil error (Redis unreachable etc.)
//
// The "miss" outcome is emitted by callers that explicitly detect a missing key
// (currently not used here — VDrop is idempotent on missing keys and returns nil).
func (h *Handler) dropVsetCache(ctx context.Context, cubeID string) {
	if h.wmCache == nil {
		return
	}
	if err := h.wmCache.VDrop(ctx, cubeID); err != nil {
		h.logger.Warn("delete_cube: VSET drop failed",
			slog.String("cube_id", cubeID),
			slog.Any("error", err),
		)
		observability.RecordVsetInvalidate(ctx, "delete_cube", "error")
		return
	}
	observability.RecordVsetInvalidate(ctx, "delete_cube", "success")
}
