package handlers

// admin_reorg.go — POST /product/admin/reorg
// Triggers the Go Memory Reorganizer on demand for a cube, bypassing Redis streams.
// Useful for ops verification and forced consolidation without waiting for the 6-hour cycle.

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
)

// defaultTreeHierarchyEnabled delegates to the scheduler package's env check.
// Extracted so tests can stub the flag without touching os.Setenv.
func defaultTreeHierarchyEnabled() bool { return scheduler.TreeHierarchyEnabled() }

// reorgRunner is the minimal interface for the Memory Reorganizer, defined here
// so tests can inject a mock without depending on the concrete scheduler type.
//
// RunTreeReorgForCube is the D3 tree reorganizer entry point (raw → episodic →
// semantic promotion, emits CONSOLIDATED_INTO edges). It is a no-op unless
// MEMDB_REORG_HIERARCHY=true, but AdminReorg calls it unconditionally so the
// gate decision lives in one place (scheduler.TreeHierarchyEnabled) rather
// than duplicated across callers.
type reorgRunner interface {
	Run(ctx context.Context, cubeID string)
	RunTargeted(ctx context.Context, cubeID string, ids []string)
	RunTreeReorgForCube(ctx context.Context, cubeID string)
}

// treeHierarchyEnabledFn is scheduler.TreeHierarchyEnabled; extracted behind
// an atomic.Pointer so tests can swap it without touching process env and
// without racing the background goroutine that reads it post-dispatch.
var treeHierarchyEnabledFn = func() bool {
	fn := treeHierarchyEnabledPtr.Load()
	if fn == nil {
		return defaultTreeHierarchyEnabled()
	}
	return (*fn)()
}

var treeHierarchyEnabledPtr atomic.Pointer[func() bool]

// SetTreeHierarchyEnabledForTest is test-only: installs a flag stub and
// returns a restore func. Safe to call from parallel tests because the
// underlying pointer is atomic. Exporting is acceptable since the function
// is called solely from tests in the same package plus gives a single
// well-named seam for anything external.
func SetTreeHierarchyEnabledForTest(fn func() bool) (restore func()) {
	prev := treeHierarchyEnabledPtr.Load()
	treeHierarchyEnabledPtr.Store(&fn)
	return func() { treeHierarchyEnabledPtr.Store(prev) }
}

// reorgRequest is the JSON body for POST /product/admin/reorg.
type reorgRequest struct {
	CubeID string   `json:"cube_id"`
	IDs    []string `json:"ids"`
}

// reorgResponse is the JSON response for POST /product/admin/reorg.
type reorgResponse struct {
	Status      string `json:"status"`
	CubeID      string `json:"cube_id"`
	Mode        string `json:"mode"`
	TriggeredAt string `json:"triggered_at"`
}

// AdminReorg triggers the Memory Reorganizer for a cube, returning 202 Accepted immediately.
// The reorganizer runs in a background goroutine with a 10-minute timeout.
func (h *Handler) AdminReorg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"code": 405, "message": "method not allowed",
		})
		return
	}

	if h.reorg == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "reorganizer not configured",
		})
		return
	}

	cubeID, ids, ok := h.parseReorgRequest(w, r)
	if !ok {
		return
	}

	mode := "full"
	if len(ids) > 0 {
		mode = "targeted"
	}

	triggeredAt := time.Now().UTC().Format(time.RFC3339)
	h.logger.Info("admin reorg: dispatching",
		slog.String("cube_id", cubeID),
		slog.String("mode", mode),
		slog.Int("ids_count", len(ids)),
	)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				h.logger.Error("admin reorg: panic in background goroutine",
					slog.Any("panic", rec),
					slog.String("cube_id", cubeID),
				)
			}
		}()

		if mode == "targeted" {
			h.reorg.RunTargeted(ctx, cubeID, ids)
		} else {
			h.reorg.Run(ctx, cubeID)
		}

		// D3 tree reorganizer — raw → episodic → semantic promotion. Emits
		// CONSOLIDATED_INTO edges that D2's recursive CTE traverses for
		// multi-hop recall. Mirrors the sequencing in
		// scheduler.runPeriodicReorg (Run → RunTreeReorgForCube). Targeted
		// mode still invokes it so operators can force a cube-wide tree
		// promotion from the admin endpoint when iterating on D3 tuning.
		treeRan := false
		if treeHierarchyEnabledFn() {
			h.reorg.RunTreeReorgForCube(ctx, cubeID)
			treeRan = true
		}

		h.logger.Info("admin reorg: background goroutine finished",
			slog.String("cube_id", cubeID),
			slog.String("mode", mode),
			slog.Bool("tree_reorg_ran", treeRan),
		)
	}()

	h.writeJSON(w, http.StatusAccepted, reorgResponse{
		Status:      "accepted",
		CubeID:      cubeID,
		Mode:        mode,
		TriggeredAt: triggeredAt,
	})
}

// parseReorgRequest validates and returns cube_id and ids from the request.
func (h *Handler) parseReorgRequest(w http.ResponseWriter, r *http.Request) (cubeID string, ids []string, ok bool) {
	var req reorgRequest

	// Try JSON body first
	if err := parseJSONBody(r, &req); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "message": "invalid JSON: " + err.Error(),
		})
		return "", nil, false
	}

	// Fall back to query param if body had no cube_id
	if req.CubeID == "" {
		req.CubeID = r.URL.Query().Get("cube_id")
	}

	if req.CubeID == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "message": "cube_id is required",
		})
		return "", nil, false
	}

	return req.CubeID, req.IDs, true
}
