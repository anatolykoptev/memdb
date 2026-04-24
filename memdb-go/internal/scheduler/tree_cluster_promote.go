package scheduler

// tree_cluster_promote.go — cluster-persistence half of the tree reorganizer.
//
// promoteCluster owns the "write the tier parent + hook children to it" step:
// one LLM summary, N CONSOLIDATED_INTO edges, N SetHierarchyLevel calls, and
// one audit-log event. Split out of tree_manager.go to keep each file below
// the 200-line policy ceiling and to isolate the persistence-error metric
// surface added in M5 follow-up #2.

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// promoteCluster creates a parent memory node for the cluster, links each child
// with a CONSOLIDATED_INTO edge, and promotes the children's hierarchy_level
// via parent_memory_id. Called by RunTreeReorgForCube for both tiers.
//
// Returns a zero parentInfo on empty-summary / LLM-no-op clusters — callers
// check `info.ID == ""` to skip appending. Returns the non-nil parent on
// success so the caller can feed it to the relation phase without re-embedding.
//
// Per-child errors (edge write, hierarchy update, audit insert) are logged at
// Warn and counted via the TreeReorg metric but do NOT abort the cluster —
// a partial cluster is still better than none, and each failure mode is
// independently recoverable.
func (r *Reorganizer) promoteCluster(ctx context.Context, cubeID string, cluster []hierarchyNode, targetLevel string) (parentInfo, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.createTierParent(ctx, cubeID, cluster, targetLevel, now)
	if err != nil {
		return parentInfo{}, err
	}
	if res.ParentID == "" {
		return parentInfo{}, nil // empty LLM output — nothing to write (not a failure)
	}

	childIDs := make([]string, 0, len(cluster))
	for _, n := range cluster {
		if err := r.postgres.CreateMemoryEdge(ctx, n.ID, res.ParentID, "CONSOLIDATED_INTO", now, ""); err != nil {
			r.logger.Warn("tree reorg: edge write failed",
				slog.String("from", n.ID), slog.String("to", res.ParentID),
				slog.String("tier", targetLevel),
				slog.Any("error", err))
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", targetLevel),
				attribute.String("outcome", "edge_write_error"),
			))
			continue
		}
		if err := r.postgres.SetHierarchyLevel(ctx, n.ID, childHierarchyLevelFor(targetLevel), res.ParentID, now); err != nil {
			r.logger.Warn("tree reorg: set child hierarchy failed",
				slog.String("id", n.ID),
				slog.String("tier", targetLevel),
				slog.Any("error", err))
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", targetLevel),
				attribute.String("outcome", "hierarchy_write_error"),
			))
		}
		childIDs = append(childIDs, n.ID)
	}

	// Audit trail (best-effort — schema is nullable and recoverable without it).
	eventID := newUUID()
	if err := r.postgres.InsertTreeConsolidationEvent(ctx, eventID, cubeID, res.ParentID, childIDs, targetLevel, r.llmClient.Model(), res.PromptSHA, now); err != nil {
		r.logger.Warn("tree reorg: audit log write failed",
			slog.String("parent_id", res.ParentID),
			slog.String("tier", targetLevel),
			slog.Any("error", err))
		schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", targetLevel),
			attribute.String("outcome", "audit_write_error"),
		))
	}

	return parentInfo{
		ID:        res.ParentID,
		Text:      res.Summary,
		Embedding: res.Embedding,
		Tier:      targetLevel,
	}, nil
}

// childHierarchyLevelFor returns the hierarchy_level that children of a
// given-tier parent should carry. Semantic parent children stay 'episodic'
// (they were already promoted in the first pass). Episodic parent children
// become 'raw-consolidated' via the 'raw' level; parent_memory_id ties them.
func childHierarchyLevelFor(parentLevel string) string {
	// We keep children at their current tier but stamped with parent_memory_id.
	// hierarchy_level for the child = the tier below parent.
	switch parentLevel {
	case hierarchyLevelSemantic:
		return hierarchyLevelEpisodic
	default:
		return hierarchyLevelRaw
	}
}
