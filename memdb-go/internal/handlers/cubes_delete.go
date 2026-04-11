package handlers

import (
	"log/slog"
	"net/http"
)

// deleteCubeExec performs the actual soft- or hard-delete after the caller has
// already verified ownership. Called by NativeDeleteCube.
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
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"memories_deleted": int64(0), "hard_delete": false},
	})
}
