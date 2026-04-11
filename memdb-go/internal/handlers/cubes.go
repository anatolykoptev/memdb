package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// NativeCreateCube handles POST /product/create_cube.
// Idempotent: second call with same cube_id updates metadata (except owner_id).
func (h *Handler) NativeCreateCube(w http.ResponseWriter, r *http.Request) {
	if h.cubeStore == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": 503, "message": "cube store unavailable"})
		return
	}
	var req createCubeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeValidationError(w, []string{"invalid json: " + err.Error()})
		return
	}
	if req.CubeID == "" {
		h.writeValidationError(w, []string{"cube_id is required"})
		return
	}
	ownerID := ""
	switch {
	case req.OwnerID != nil && *req.OwnerID != "":
		ownerID = *req.OwnerID
	case req.UserID != nil && *req.UserID != "":
		ownerID = *req.UserID
	default:
		h.writeValidationError(w, []string{"owner_id or user_id is required"})
		return
	}
	cube, created, err := h.cubeStore.UpsertCube(r.Context(), db.UpsertCubeParams{
		CubeID: req.CubeID, CubeName: req.CubeName, OwnerID: ownerID,
		Description: req.Description, CubePath: req.CubePath, Settings: req.Settings,
	})
	if err != nil {
		h.logger.Error("create_cube failed", slog.String("cube_id", req.CubeID), slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "upsert failed", "data": map[string]any{"error": err.Error()},
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"cube": cubeToMap(cube), "created": created},
	})
}

// NativeListCubes handles POST /product/list_cubes.
func (h *Handler) NativeListCubes(w http.ResponseWriter, r *http.Request) {
	if h.cubeStore == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": 503, "message": "cube store unavailable"})
		return
	}
	var req listCubesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req = listCubesRequest{} // tolerate empty body
	}
	cubes, err := h.cubeStore.ListCubes(r.Context(), req.OwnerID)
	if err != nil {
		h.logger.Error("list_cubes failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "list failed"})
		return
	}
	out := make([]map[string]any, 0, len(cubes))
	for _, c := range cubes {
		out = append(out, cubeToMap(c))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok", "data": map[string]any{"cubes": out},
	})
}

// NativeGetUserCubes is the upstream MemOS alias for list_cubes(owner_id=user_id).
func (h *Handler) NativeGetUserCubes(w http.ResponseWriter, r *http.Request) {
	if h.cubeStore == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": 503, "message": "cube store unavailable"})
		return
	}
	var req getUserCubesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeValidationError(w, []string{"invalid json: " + err.Error()})
		return
	}
	if req.UserID == "" {
		h.writeValidationError(w, []string{"user_id is required"})
		return
	}
	cubes, err := h.cubeStore.ListCubes(r.Context(), &req.UserID)
	if err != nil {
		h.logger.Error("get_user_cubes failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "list failed"})
		return
	}
	out := make([]map[string]any, 0, len(cubes))
	for _, c := range cubes {
		out = append(out, cubeToMap(c))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok", "data": map[string]any{"cubes": out},
	})
}

// NativeDeleteCube handles POST /product/delete_cube.
// Performs owner check, then soft- or hard-deletes based on hard_delete flag.
// See cubes_delete.go for implementation.
func (h *Handler) NativeDeleteCube(w http.ResponseWriter, r *http.Request) {
	if h.cubeStore == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": 503, "message": "cube store unavailable"})
		return
	}
	var req deleteCubeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeValidationError(w, []string{"invalid json: " + err.Error()})
		return
	}
	if req.CubeID == "" || req.UserID == "" {
		h.writeValidationError(w, []string{"cube_id and user_id are required"})
		return
	}
	ctx := r.Context()
	cube, err := h.cubeStore.GetCube(ctx, req.CubeID)
	if err != nil {
		if errors.Is(err, db.ErrCubeNotFound) {
			h.writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "cube not found"})
			return
		}
		h.logger.Error("delete_cube: get_cube failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "lookup failed"})
		return
	}
	if cube.OwnerID != req.UserID {
		h.writeJSON(w, http.StatusForbidden, map[string]any{"code": 403, "message": "forbidden: not cube owner"})
		return
	}
	h.deleteCubeExec(w, r, req.CubeID, cube.OwnerID, req.UserID, req.HardDelete)
}
