package scheduler

// reorganizer_consolidate.go — near-duplicate detection and cluster consolidation.
//
// Implements Run (full scan) and RunTargeted (seed-ID scan) plus the shared
// buildClusters / consolidateCluster helpers used by both entry points.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

const (
	consolidateLogPreviewLen = 80  // chars of merged text to log as preview
	consolidateLLMMaxTokens  = 512 // max_tokens for consolidation LLM call
	consolidateErrTruncLen   = 200 // max chars of error LLM output to include in error message
)

// Run performs one reorganization cycle for the given cube (user_name in DB terms).
// It is safe to call concurrently for different cubes.
func (r *Reorganizer) Run(ctx context.Context, cubeID string) {
	log := r.logger.With(slog.String("cube_id", cubeID))
	log.Debug("reorganizer: starting cycle")

	pairs, err := r.postgres.FindNearDuplicates(ctx, cubeID, dupThreshold, dupScanLimit)
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
		if len(cluster) < 2 {
			continue
		}
		if err := r.consolidateCluster(ctx, cubeID, cluster, now); err != nil {
			log.Warn("reorganizer: cluster consolidation failed",
				slog.Any("ids", clusterIDs(cluster)),
				slog.Any("error", err))
			skipped++
		} else {
			merged++
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

	pairs, err := r.postgres.FindNearDuplicatesByIDs(ctx, cubeID, ids, dupThreshold, dupScanLimit)
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
		if len(cluster) < 2 {
			continue
		}
		if err := r.consolidateCluster(ctx, cubeID, cluster, now); err != nil {
			log.Warn("reorganizer: targeted cluster consolidation failed",
				slog.Any("ids", clusterIDs(cluster)),
				slog.Any("error", err))
			skipped++
		} else {
			merged++
		}
	}
	log.Info("reorganizer: targeted cycle complete",
		slog.Int("merged_clusters", merged),
		slog.Int("skipped_clusters", skipped),
	)
}

// memNode is a minimal memory representation used during clustering.
type memNode struct {
	ID   string
	Text string
}

// buildClusters groups near-duplicate pairs into connected clusters using Union-Find.
// Each cluster is a slice of memNodes that should be consolidated together.
func buildClusters(pairs []db.DuplicatePair) [][]memNode {
	idToText := make(map[string]string, len(pairs)*2)
	for _, p := range pairs {
		if idToText[p.IDa] == "" {
			idToText[p.IDa] = p.MemA
		}
		if idToText[p.IDb] == "" {
			idToText[p.IDb] = p.MemB
		}
	}

	parent := make(map[string]string, len(idToText))
	for id := range idToText {
		parent[id] = id
	}

	var find func(x string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}

	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for _, p := range pairs {
		union(p.IDa, p.IDb)
	}

	groups := make(map[string][]memNode)
	for id, text := range idToText {
		root := find(id)
		groups[root] = append(groups[root], memNode{ID: id, Text: text})
	}

	clusters := make([][]memNode, 0, len(groups))
	for _, members := range groups {
		if len(members) >= 2 {
			clusters = append(clusters, members)
		}
	}
	return clusters
}

// consolidateCluster calls the LLM to merge a cluster, then persists the result.
func (r *Reorganizer) consolidateCluster(ctx context.Context, cubeID string, cluster []memNode, now string) error {
	result, err := r.llmConsolidate(ctx, cluster)
	if err != nil {
		return fmt.Errorf("llm consolidate: %w", err)
	}
	if result.KeepID == "" || result.MergedText == "" {
		return errors.New("llm returned empty keep_id or merged_text")
	}

	clusterSet := make(map[string]bool, len(cluster))
	for _, n := range cluster {
		clusterSet[n.ID] = true
	}
	if !clusterSet[result.KeepID] {
		return fmt.Errorf("llm returned keep_id %q not in cluster", result.KeepID)
	}

	embVec := ""
	if r.embedder != nil {
		embs, err := r.embedder.Embed(ctx, []string{result.MergedText})
		if err == nil && len(embs) > 0 && len(embs[0]) > 0 {
			embVec = db.FormatVector(embs[0])
		}
	}

	if err := r.postgres.UpdateMemoryNodeFull(ctx, result.KeepID, result.MergedText, embVec, now); err != nil {
		return fmt.Errorf("update keep node %s: %w", result.KeepID, err)
	}
	r.logger.Debug("reorganizer: updated keep node",
		slog.String("id", result.KeepID),
		slog.String("text_preview", truncate(result.MergedText, consolidateLogPreviewLen)))

	for _, removeID := range result.RemoveIDs {
		if clusterSet[removeID] {
			r.removeMergedNode(ctx, cubeID, removeID, result.KeepID, now)
		} else {
			r.logger.Warn("reorganizer: LLM returned remove_id not in cluster, skipping",
				slog.String("remove_id", removeID))
		}
	}

	// Contradiction path: hard-delete memories directly contradicted by the winner.
	// Unlike near-dupes (soft-merged for audit), contradicted memories must not
	// resurface — they are factually wrong and their existence would mislead the model.
	for _, contradictedID := range result.ContradictedIDs {
		if clusterSet[contradictedID] {
			r.removeContradictedNode(ctx, cubeID, contradictedID, result.KeepID, now)
		} else {
			r.logger.Warn("reorganizer: LLM returned contradicted_id not in cluster, skipping",
				slog.String("contradicted_id", contradictedID))
		}
	}

	return nil
}

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

// consolidationResult is the JSON structure returned by the LLM.
// ContradictedIDs contains memories directly contradicted by the winner — they are
// hard-deleted (not soft-merged) so they never re-surface in search results.
type consolidationResult struct {
	KeepID         string   `json:"keep_id"`
	RemoveIDs      []string `json:"remove_ids"`
	ContradictedIDs []string `json:"contradicted_ids,omitempty"`
	MergedText     string   `json:"merged_text"`
}

// llmConsolidate calls the LLM with the cluster members and returns a parsed result.
func (r *Reorganizer) llmConsolidate(ctx context.Context, cluster []memNode) (consolidationResult, error) {
	type inputItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	items := make([]inputItem, len(cluster))
	for i, n := range cluster {
		items[i] = inputItem(n)
	}
	memoriesJSON, _ := json.Marshal(items)

	msgs := []map[string]string{
		{"role": "system", "content": consolidationSystemPrompt},
		{"role": "user", "content": fmt.Sprintf("Memory cluster to consolidate:\n%s", memoriesJSON)},
	}

	callCtx, cancel := context.WithTimeout(ctx, reorganizerLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, msgs, consolidateLLMMaxTokens)
	if err != nil {
		return consolidationResult{}, err
	}

	raw = stripFences(raw)
	var result consolidationResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return consolidationResult{}, fmt.Errorf("parse llm json (%s): %w", truncate(raw, consolidateErrTruncLen), err)
	}
	return result, nil
}

// clusterIDs extracts IDs from a cluster for logging.
func clusterIDs(cluster []memNode) []string {
	ids := make([]string, len(cluster))
	for i, n := range cluster {
		ids[i] = n.ID
	}
	return ids
}
