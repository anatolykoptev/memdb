package scheduler

// tree_manager.go — D3 hierarchical reorganizer entry point.
//
// Port of Python `tree_text_memory/organize/manager.py` + `reorganizer.py`.
// Orchestrates raw → episodic → semantic promotion for a single cube, then
// optionally detects A→B relations across the new parent nodes. Clustering
// lives in tree_reorganizer.go; cluster persistence in tree_cluster_promote.go;
// relation phase in tree_relation_phase.go.

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
	// hierarchyLevel* — property values for Memory.hierarchy_level.
	// Cluster size + cosine thresholds live in tuning.go (env-readable).
	hierarchyLevelRaw      = "raw"
	hierarchyLevelEpisodic = "episodic"
	hierarchyLevelSemantic = "semantic"

	// Per-cube candidate caps — bound memory + LLM cost per run.
	rawCandidateLimit      = 500
	episodicCandidateLimit = 200

	// LLM call caps for tier summaries.
	tierSummaryMaxTokens = 400
	tierSummaryTimeout   = 45 * time.Second

	// memoryType* — memory_type field persisted for each tier's parent node.
	// memoryTypeEpisodic reuses the WM-compaction constant to keep filters unchanged.
	memoryTypeEpisodic = episodicMemType // "EpisodicMemory"
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

	parents := make([]parentInfo, 0, 8)

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
	epMinSize := episodicMinClusterSize()
	if len(rawMems) >= epMinSize {
		clusters := clusterByCosine(rawMems, episodicCosineThreshold(), epMinSize)
		log.Info("tree reorg: raw clusters formed", slog.Int("clusters", len(clusters)))

		for _, cluster := range clusters {
			select {
			case <-ctx.Done():
				log.Warn("tree reorg: ctx cancelled mid-cycle")
				return
			default:
			}
			if len(cluster) < epMinSize {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "episodic"),
					attribute.String("outcome", "skipped_below_threshold"),
				))
				continue
			}
			info, err := r.promoteCluster(ctx, cubeID, cluster, hierarchyLevelEpisodic)
			if err != nil {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "episodic"),
					attribute.String("outcome", "error"),
				))
				log.Warn("tree reorg: episodic promote failed",
					slog.Int("cluster_size", len(cluster)),
					slog.Any("error", err))
				continue
			}
			if info.ID == "" {
				// Empty-summary cluster — LLM returned nothing actionable.
				continue
			}
			parents = append(parents, info)
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
	semMinSize := semanticMinClusterSize()
	if len(epMems) >= semMinSize {
		clusters := clusterByCosine(epMems, semanticCosineThreshold(), semMinSize)
		log.Info("tree reorg: episodic clusters formed", slog.Int("clusters", len(clusters)))

		for _, cluster := range clusters {
			select {
			case <-ctx.Done():
				log.Warn("tree reorg: ctx cancelled mid-cycle")
				return
			default:
			}
			if len(cluster) < semMinSize {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "semantic"),
					attribute.String("outcome", "skipped_below_threshold"),
				))
				continue
			}
			info, err := r.promoteCluster(ctx, cubeID, cluster, hierarchyLevelSemantic)
			if err != nil {
				schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
					attribute.String("tier", "semantic"),
					attribute.String("outcome", "error"),
				))
				log.Warn("tree reorg: semantic promote failed",
					slog.Int("cluster_size", len(cluster)),
					slog.Any("error", err))
				continue
			}
			if info.ID == "" {
				continue
			}
			parents = append(parents, info)
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

	// ---- tier 3: cross-parent relation detection (opt-in) --------------
	r.runRelationPhase(ctx, cubeID, parents)

	log.Info("tree reorg: cycle complete",
		slog.Int("episodic_created", episodicCreated),
		slog.Int("semantic_created", semanticCreated),
		slog.Int("parents_collected", len(parents)),
	)
}
