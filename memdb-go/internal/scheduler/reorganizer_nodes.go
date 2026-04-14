package scheduler

// reorganizer_nodes.go — node mutation helpers: soft-delete merged, hard-delete contradicted, VSET eviction.

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// removeMergedNode soft-deletes a near-duplicate node and records the MERGED_INTO graph edge.
func (r *Reorganizer) removeMergedNode(ctx context.Context, cubeID, removeID, keepID, now string) {
	if err := r.postgres.SoftDeleteMerged(ctx, removeID, keepID, now); err != nil {
		r.logger.Warn("reorganizer: soft-delete merged failed",
			slog.String("remove_id", removeID), slog.Any("error", err))
	} else {
		r.logger.Debug("reorganizer: merged memory",
			slog.String("remove_id", removeID), slog.String("into", keepID))
		if err := r.postgres.CreateMemoryEdge(ctx, removeID, keepID, db.EdgeMergedInto, now, ""); err != nil {
			r.logger.Debug("reorganizer: create merged edge failed (non-fatal)",
				slog.String("from", removeID), slog.String("to", keepID), slog.Any("error", err))
		}
	}
	r.evictVSet(ctx, cubeID, removeID, "reorganizer: vset evict failed (non-fatal)")
	r.invalidateCaches(ctx, cubeID, removeID)
}

// removeContradictedNode hard-deletes a contradicted memory and invalidates its graph edges.
func (r *Reorganizer) removeContradictedNode(ctx context.Context, cubeID, contradictedID, keepID, now string) {
	// Record CONTRADICTS edge before hard-delete so the relationship is auditable.
	if err := r.postgres.CreateMemoryEdge(ctx, keepID, contradictedID, db.EdgeContradicts, now, ""); err != nil {
		r.logger.Debug("reorganizer: create contradicts edge failed (non-fatal)",
			slog.String("from", keepID), slog.String("to", contradictedID), slog.Any("error", err))
	}
	// Bi-temporal invalidation: stamp invalid_at on all edges from the contradicted memory.
	if err := r.postgres.InvalidateEdgesByMemoryID(ctx, contradictedID, now); err != nil {
		r.logger.Debug("reorganizer: invalidate memory edges failed (non-fatal)",
			slog.String("id", contradictedID), slog.Any("error", err))
	}
	if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, contradictedID, now); err != nil {
		r.logger.Debug("reorganizer: invalidate entity edges failed (non-fatal)",
			slog.String("id", contradictedID), slog.Any("error", err))
	}
	if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{contradictedID}, cubeID); err != nil {
		r.logger.Warn("reorganizer: hard-delete contradicted failed",
			slog.String("contradicted_id", contradictedID), slog.Any("error", err))
	} else {
		r.logger.Info("reorganizer: hard-deleted contradicted memory",
			slog.String("contradicted_id", contradictedID), slog.String("by", keepID))
	}
	r.evictVSet(ctx, cubeID, contradictedID, "reorganizer: vset evict contradicted failed (non-fatal)")
	r.invalidateCaches(ctx, cubeID, contradictedID)
}

// invalidateCaches purges the Redis search-result and per-memory caches for the given ID.
// Prevents clients from receiving stale IDs in subsequent search responses after a
// soft-delete (merged) or hard-delete (contradicted). Non-fatal: no-op if cacheInvalidator is nil.
func (r *Reorganizer) invalidateCaches(ctx context.Context, cubeID, memoryID string) {
	if r.cacheInvalidator == nil {
		return
	}
	r.cacheInvalidator.Invalidate(ctx,
		"memdb:db:search:*:"+cubeID+":*", // vector/fulltext search results for this cube
		"memdb:db:memory:"+memoryID,       // per-ID direct get cache
	)
}

// evictVSet removes an ID from the VSET hot cache (non-fatal).
func (r *Reorganizer) evictVSet(ctx context.Context, cubeID, id, logMsg string) {
	if r.wmCache == nil {
		return
	}
	if err := r.wmCache.VRem(ctx, cubeID, id); err != nil {
		r.logger.Debug(logMsg, slog.String("id", id), slog.Any("error", err))
	}
}
