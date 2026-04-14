package scheduler

// reorganizer_automerge.go — fast cluster merge without LLM.
//
// When all pairwise cosine similarities in a cluster exceed autoMergeThreshold (0.97),
// the texts are near-identical. We skip the LLM call and pick the longest text as the
// canonical version, soft-deleting the rest. No embedder call either — the kept node
// already has a good embedding.

import (
	"context"
	"log/slog"
)

// autoMergeCluster merges a cluster deterministically:
//   - keepNode: node with the longest text (most information-dense)
//   - removeNodes: all others — soft-deleted via removeMergedNode
//
// No LLM, no re-embedding. O(n) in cluster size.
func (r *Reorganizer) autoMergeCluster(ctx context.Context, cubeID string, cluster []memNode, now string) error {
	keepIdx := 0
	for i, n := range cluster {
		if len(n.Text) > len(cluster[keepIdx].Text) {
			keepIdx = i
		}
	}
	keepNode := cluster[keepIdx]

	r.logger.Info("reorganizer: auto-merge cluster (score≥0.97, no LLM)",
		slog.String("cube_id", cubeID),
		slog.String("keep_id", keepNode.ID),
		slog.Int("cluster_size", len(cluster)),
		slog.Float64("min_score", clusterMinScore(cluster)),
	)

	for i, n := range cluster {
		if i == keepIdx {
			continue
		}
		r.removeMergedNode(ctx, cubeID, n.ID, keepNode.ID, now)
	}
	return nil
}
