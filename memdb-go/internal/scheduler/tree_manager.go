package scheduler

// tree_manager.go — D3 hierarchical reorganizer entry point.
//
// Port of Python `tree_text_memory/organize/manager.py` + `reorganizer.py`.
// Orchestrates raw → episodic → semantic promotion for a single cube:
//
//  1. Load raw memories.
//  2. Cluster by cosine similarity (cos ≥ episodicCosineThreshold, size ≥ episodicMinClusterSize).
//  3. Per cluster: LLM summarise → new EpisodicMemory node → CONSOLIDATED_INTO edges + SetHierarchyLevel.
//  4. Load episodic memories.
//  5. Cluster by cosine (size ≥ semanticMinClusterSize).
//  6. Per cluster: LLM summarise → new SemanticMemory node → CONSOLIDATED_INTO edges + SetHierarchyLevel.
//  7. Optional: relation detector on cross-cluster pairs.
//
// Keeps files ≤200 lines by delegating clustering and consolidation to
// tree_reorganizer.go and LLM-call / edge-write helpers below.

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// TreeHierarchyEnabled reports whether the D3 tree reorganizer is active.
// Read on every call (not at init) so tests can flip with t.Setenv and so
// deploys can toggle without restart. Default FALSE — keeps the pipeline
// identical to pre-D3 until explicitly enabled.
func TreeHierarchyEnabled() bool {
	return os.Getenv("MEMDB_REORG_HIERARCHY") == "true"
}

const (
	// hierarchyLevelRaw / Episodic / Semantic — property values for Memory.hierarchy_level.
	hierarchyLevelRaw      = "raw"
	hierarchyLevelEpisodic = "episodic"
	hierarchyLevelSemantic = "semantic"

	// Cluster size bounds — match Python tree_text_memory/organize defaults.
	episodicMinClusterSize = 3
	semanticMinClusterSize = 2

	// Cosine thresholds for in-cluster connectivity.
	episodicCosineThreshold = 0.70
	semanticCosineThreshold = 0.60

	// Per-cube candidate caps. Bound memory + LLM cost per run. Deliberately
	// modest — a busy conversational cube rarely exceeds 500 raw memories
	// between 6h reorg cycles, and the episodic tier compacts further.
	rawCandidateLimit      = 500
	episodicCandidateLimit = 200

	// LLM call caps for tier summaries.
	tierSummaryMaxTokens = 400
	tierSummaryTimeout   = 45 * time.Second

	// memoryTypeEpisodic is the memory_type persisted for episodic nodes (reused
	// from the existing WM-compaction path — keeps search filters unchanged).
	memoryTypeEpisodic = episodicMemType // "EpisodicMemory"

	// memoryTypeSemantic is the D3-new type for semantic theme nodes.
	memoryTypeSemantic = "SemanticMemory"
)

// RunTreeReorgForCube runs one full tree reorganization pass for a single cube.
//
// Non-fatal: errors at any stage are logged and the pass continues (or aborts
// gracefully). Safe to call from periodicReorgLoop.
//
// Callers MUST check TreeHierarchyEnabled() before invoking — this function
// unconditionally executes the pipeline when called.
func (r *Reorganizer) RunTreeReorgForCube(ctx context.Context, cubeID string) {
	log := r.logger.With(
		slog.String("cube_id", cubeID),
		slog.String("component", "tree_reorg"),
	)
	log.Debug("tree reorg: starting cycle")

	// ---- tier 1: raw → episodic -----------------------------------------
	rawMems, err := r.postgres.ListMemoriesByHierarchyLevel(ctx, cubeID, hierarchyLevelRaw, rawCandidateLimit)
	if err != nil {
		schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", "all"),
			attribute.String("outcome", "error"),
		))
		log.Error("tree reorg: list raw failed", slog.Any("error", err))
		return
	}
	log.Debug("tree reorg: loaded raw memories", slog.Int("count", len(rawMems)))

	episodicCreated := 0
	if len(rawMems) >= episodicMinClusterSize {
		clusters := clusterByCosine(rawMems, episodicCosineThreshold, episodicMinClusterSize)
		log.Info("tree reorg: raw clusters formed", slog.Int("clusters", len(clusters)))

		for _, cluster := range clusters {
			select {
			case <-ctx.Done():
				log.Warn("tree reorg: ctx cancelled mid-cycle")
				return
			default:
			}
			if len(cluster) < episodicMinClusterSize {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "episodic"),
					attribute.String("outcome", "skipped_below_threshold"),
				))
				continue
			}
			if err := r.promoteCluster(ctx, cubeID, cluster, hierarchyLevelEpisodic); err != nil {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "episodic"),
					attribute.String("outcome", "error"),
				))
				log.Warn("tree reorg: episodic promote failed",
					slog.Int("cluster_size", len(cluster)),
					slog.Any("error", err))
				continue
			}
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", "episodic"),
				attribute.String("outcome", "created"),
			))
			episodicCreated++
		}
	} else {
		// Whole raw tier is below the threshold for any clustering.
		schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", "episodic"),
			attribute.String("outcome", "skipped_below_threshold"),
		))
	}

	// ---- tier 2: episodic → semantic ------------------------------------
	epMems, err := r.postgres.ListMemoriesByHierarchyLevel(ctx, cubeID, hierarchyLevelEpisodic, episodicCandidateLimit)
	if err != nil {
		schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", "all"),
			attribute.String("outcome", "error"),
		))
		log.Error("tree reorg: list episodic failed", slog.Any("error", err))
		return
	}
	log.Debug("tree reorg: loaded episodic memories", slog.Int("count", len(epMems)))

	semanticCreated := 0
	if len(epMems) >= semanticMinClusterSize {
		clusters := clusterByCosine(epMems, semanticCosineThreshold, semanticMinClusterSize)
		log.Info("tree reorg: episodic clusters formed", slog.Int("clusters", len(clusters)))

		for _, cluster := range clusters {
			select {
			case <-ctx.Done():
				log.Warn("tree reorg: ctx cancelled mid-cycle")
				return
			default:
			}
			if len(cluster) < semanticMinClusterSize {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "semantic"),
					attribute.String("outcome", "skipped_below_threshold"),
				))
				continue
			}
			if err := r.promoteCluster(ctx, cubeID, cluster, hierarchyLevelSemantic); err != nil {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "semantic"),
					attribute.String("outcome", "error"),
				))
				log.Warn("tree reorg: semantic promote failed",
					slog.Int("cluster_size", len(cluster)),
					slog.Any("error", err))
				continue
			}
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", "semantic"),
				attribute.String("outcome", "created"),
			))
			semanticCreated++
		}
	} else {
		schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", "semantic"),
			attribute.String("outcome", "skipped_below_threshold"),
		))
	}

	log.Info("tree reorg: cycle complete",
		slog.Int("episodic_created", episodicCreated),
		slog.Int("semantic_created", semanticCreated),
	)
}

// promoteCluster creates a parent memory node for the cluster, links each child
// with a CONSOLIDATED_INTO edge, and promotes the children's hierarchy_level
// via parent_memory_id. Called by RunTreeReorgForCube for both tiers.
func (r *Reorganizer) promoteCluster(ctx context.Context, cubeID string, cluster []hierarchyNode, targetLevel string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	parentID, promptSHA, err := r.createTierParent(ctx, cubeID, cluster, targetLevel, now)
	if err != nil {
		return err
	}
	if parentID == "" {
		return nil // empty LLM output — nothing to write (not a failure)
	}

	childIDs := make([]string, 0, len(cluster))
	for _, n := range cluster {
		if err := r.postgres.CreateMemoryEdge(ctx, n.ID, parentID, "CONSOLIDATED_INTO", now, ""); err != nil {
			r.logger.Debug("tree reorg: edge write failed",
				slog.String("from", n.ID), slog.String("to", parentID),
				slog.Any("error", err))
			continue
		}
		if err := r.postgres.SetHierarchyLevel(ctx, n.ID, childHierarchyLevelFor(targetLevel), parentID, now); err != nil {
			r.logger.Debug("tree reorg: set child hierarchy failed",
				slog.String("id", n.ID), slog.Any("error", err))
		}
		childIDs = append(childIDs, n.ID)
	}

	// Audit trail (best-effort — schema is nullable and recoverable without it).
	eventID := newUUID()
	if err := r.postgres.InsertTreeConsolidationEvent(ctx, eventID, cubeID, parentID, childIDs, targetLevel, r.llmClient.Model(), promptSHA, now); err != nil {
		r.logger.Debug("tree reorg: audit log write failed", slog.Any("error", err))
	}
	return nil
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
