package scheduler

// reorganizer_consolidate.go — near-duplicate detection and cluster consolidation entry points.
//
// Run / RunTargeted scan for near-duplicate pairs, form clusters, and call consolidateCluster.
// Cluster primitives → reorganizer_cluster.go
// Node removal helpers → reorganizer_nodes.go
// Fuzzy UUID resolution → reorganizer_fuzzy.go

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	consolidateLogPreviewLen = 80  // chars of merged text to log as preview
	consolidateLLMMaxTokens      = 512 // max_tokens for consolidation LLM call
	consolidateLLMMaxTokensPair  = 192 // max_tokens for 2-node cluster (short JSON response)
	consolidateErrTruncLen   = 200 // max chars of error LLM output to include in error message

	// maxClusterSize caps how many memories are sent to the LLM in one
	// consolidation call. Larger clusters are split into chunks of this size
	// so the LLM does not time out or hallucinate UUIDs on long prompts.
	maxClusterSize = 6

	// fuzzyIDMaxDistance is the maximum Levenshtein distance used when
	// matching LLM-returned IDs that may contain single-character hallucinations.
	fuzzyIDMaxDistance = 2

	// dupHNSWTopK is the per-memory candidate pool for the HNSW path.
	// 20 catches near-duplicates at threshold 0.85 with minimal overhead.
	// Increase to 40 if recall regresses on large cubes.
	dupHNSWTopK = 20

	// autoMergeThreshold is the minimum cosine similarity at which a cluster
	// is merged without calling the LLM. At ≥0.97 the texts are near-identical
	// (same fact stated differently), so we keep the longest text and skip LLM.
	// Set to 1.1 to disable auto-merge entirely.
	autoMergeThreshold = 0.97
)

// findNearDuplicates routes to the HNSW or legacy query based on the feature flag.
func (r *Reorganizer) findNearDuplicates(ctx context.Context, cubeID string) ([]db.DuplicatePair, error) {
	if r.useHNSW {
		return r.postgres.FindNearDuplicatesHNSW(ctx, cubeID, dupThreshold, dupScanLimit, dupHNSWTopK)
	}
	return r.postgres.FindNearDuplicates(ctx, cubeID, dupThreshold, dupScanLimit)
}

// findNearDuplicatesByIDs routes the targeted path the same way.
func (r *Reorganizer) findNearDuplicatesByIDs(ctx context.Context, cubeID string, ids []string) ([]db.DuplicatePair, error) {
	if r.useHNSW {
		return r.postgres.FindNearDuplicatesHNSWByIDs(ctx, cubeID, ids, dupThreshold, dupScanLimit, dupHNSWTopK)
	}
	return r.postgres.FindNearDuplicatesByIDs(ctx, cubeID, ids, dupThreshold, dupScanLimit)
}

// Run performs one reorganization cycle for the given cube (user_name in DB terms).
// It is safe to call concurrently for different cubes.
func (r *Reorganizer) Run(ctx context.Context, cubeID string) {
	log := r.logger.With(slog.String("cube_id", cubeID))
	log.Debug("reorganizer: starting cycle")

	pairs, err := r.findNearDuplicates(ctx, cubeID)
	if err != nil {
		log.Error("reorganizer: FindNearDuplicates failed", slog.Any("error", err))
		return
	}
	if len(pairs) == 0 {
		log.Debug("reorganizer: no near-duplicates found")
		return
	}
	log.Info("reorganizer: found near-duplicate pairs", slog.Int("pairs", len(pairs)))

	clusters := buildClusters(pairs)
	log.Debug("reorganizer: formed clusters", slog.Int("clusters", len(clusters)))

	now := time.Now().UTC().Format(time.RFC3339)
	merged, skipped := 0, 0

	for _, cluster := range clusters {
		for _, sub := range splitLargeCluster(cluster, maxClusterSize) {
			if len(sub) < 2 {
				continue
			}
			if err := r.consolidateCluster(ctx, cubeID, sub, now); err != nil {
				log.Warn("reorganizer: cluster consolidation failed",
					slog.Any("ids", clusterIDs(sub)),
					slog.Any("error", err))
				skipped++
			} else {
				merged++
			}
		}
	}

	log.Info("reorganizer: cycle complete",
		slog.Int("merged_clusters", merged),
		slog.Int("skipped_clusters", skipped),
	)
}

// RunTargeted performs a reorganization cycle restricted to the given memory IDs.
//
// Unlike Run (which scans all memories for a user), RunTargeted only checks
// pairs where at least one node is in the provided ID set. This is used by
// the mem_feedback handler to consolidate memories the user flagged as relevant.
//
// Non-fatal: errors are logged and the method returns normally.
func (r *Reorganizer) RunTargeted(ctx context.Context, cubeID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	log := r.logger.With(
		slog.String("cube_id", cubeID),
		slog.Int("seed_ids", len(ids)),
	)
	log.Debug("reorganizer: targeted cycle starting")

	pairs, err := r.findNearDuplicatesByIDs(ctx, cubeID, ids)
	if err != nil {
		log.Error("reorganizer: FindNearDuplicatesByIDs failed", slog.Any("error", err))
		return
	}
	if len(pairs) == 0 {
		log.Debug("reorganizer: no near-duplicates found in targeted set")
		return
	}
	log.Info("reorganizer: targeted pairs found", slog.Int("pairs", len(pairs)))

	clusters := buildClusters(pairs)
	now := time.Now().UTC().Format(time.RFC3339)
	merged, skipped := 0, 0

	for _, cluster := range clusters {
		for _, sub := range splitLargeCluster(cluster, maxClusterSize) {
			if len(sub) < 2 {
				continue
			}
			if err := r.consolidateCluster(ctx, cubeID, sub, now); err != nil {
				log.Warn("reorganizer: targeted cluster consolidation failed",
					slog.Any("ids", clusterIDs(sub)),
					slog.Any("error", err))
				skipped++
			} else {
				merged++
			}
		}
	}
	log.Info("reorganizer: targeted cycle complete",
		slog.Int("merged_clusters", merged),
		slog.Int("skipped_clusters", skipped),
	)
}

// consolidateCluster merges a cluster either via fast auto-merge (score ≥ autoMergeThreshold)
// or by calling the LLM for lower-similarity clusters.
func (r *Reorganizer) consolidateCluster(ctx context.Context, cubeID string, cluster []memNode, now string) error {
	if clusterMinScore(cluster) >= autoMergeThreshold {
		return r.autoMergeCluster(ctx, cubeID, cluster, now)
	}

	result, err := r.llmConsolidate(ctx, cluster)
	if err != nil {
		return fmt.Errorf("llm consolidate: %w", err)
	}
	if result.KeepID == "" || result.MergedText == "" {
		return errors.New("llm returned empty keep_id or merged_text")
	}

	clusterSet := make(map[string]bool, len(cluster))
	ids := clusterIDs(cluster)
	for _, n := range cluster {
		clusterSet[n.ID] = true
	}

	// Resolve keep_id — attempt fuzzy match if exact match fails.
	keepID := result.KeepID
	if !clusterSet[keepID] {
		if resolved, ok := resolveFuzzyID(keepID, ids); ok {
			r.logger.Info("reorganizer: fuzzy-matched LLM id",
				slog.String("field", "keep_id"),
				slog.String("original", keepID),
				slog.String("resolved", resolved))
			keepID = resolved
		} else {
			return fmt.Errorf("llm returned keep_id %q not in cluster", keepID)
		}
	}

	embVec := ""
	if r.embedder != nil {
		embs, err := r.embedder.Embed(ctx, []string{result.MergedText})
		if err == nil && len(embs) > 0 && len(embs[0]) > 0 {
			embVec = db.FormatVector(embs[0])
		}
	}

	if err := r.postgres.UpdateMemoryNodeFull(ctx, keepID, result.MergedText, embVec, now); err != nil {
		return fmt.Errorf("update keep node %s: %w", keepID, err)
	}
	r.logger.Debug("reorganizer: updated keep node",
		slog.String("id", keepID),
		slog.String("text_preview", truncate(result.MergedText, consolidateLogPreviewLen)))

	for _, removeID := range result.RemoveIDs {
		if resolved, ok := r.resolveClusterID(removeID, clusterSet, ids, "remove_id"); ok {
			r.removeMergedNode(ctx, cubeID, resolved, keepID, now)
		}
	}

	// Contradiction path: hard-delete memories directly contradicted by the winner.
	// Unlike near-dupes (soft-merged for audit), contradicted memories must not
	// resurface — they are factually wrong and their existence would mislead the model.
	for _, contradictedID := range result.ContradictedIDs {
		if resolved, ok := r.resolveClusterID(contradictedID, clusterSet, ids, "contradicted_id"); ok {
			r.removeContradictedNode(ctx, cubeID, resolved, keepID, now)
		}
	}

	return nil
}
