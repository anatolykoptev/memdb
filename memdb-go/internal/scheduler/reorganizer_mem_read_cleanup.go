package scheduler

// reorganizer_mem_read_cleanup.go — WorkingMemory staging-node cleanup:
// Postgres delete + VSET hot-cache eviction after fine/legacy pipelines complete.

import (
	"context"
	"log/slog"
)

// deleteWMNodes deletes multiple WorkingMemory nodes from Postgres and evicts from VSET.
func (r *Reorganizer) deleteWMNodes(ctx context.Context, cubeID string, wmIDs []string, log *slog.Logger) {
	for _, wmID := range wmIDs {
		r.deleteWMNode(ctx, cubeID, wmID, log)
	}
}

// deleteWMNode deletes a WorkingMemory node from Postgres and evicts from VSET.
func (r *Reorganizer) deleteWMNode(ctx context.Context, cubeID, wmID string, log *slog.Logger) {
	if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{wmID}, cubeID); err != nil {
		log.Debug("mem_read: delete WM node failed (non-fatal)", slog.String("wm_id", wmID), slog.Any("error", err))
	}
	if r.wmCache != nil {
		if err := r.wmCache.VRem(ctx, cubeID, wmID); err != nil {
			log.Debug("mem_read: vset evict failed (non-fatal)", slog.String("wm_id", wmID), slog.Any("error", err))
		}
	}
}
