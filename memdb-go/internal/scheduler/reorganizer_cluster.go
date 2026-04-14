package scheduler

// reorganizer_cluster.go — Union-Find clustering and cluster splitting for the reorganizer.
//
// memNode, buildClusters, splitLargeCluster, and clusterIDs are shared by
// Run, RunTargeted, and their tests. Kept separate to stay within the 200-line limit.

import (
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

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

// splitLargeCluster slices a cluster into sub-clusters of at most maxSize nodes.
// Called after Union-Find clustering to keep LLM prompt length bounded. Order is
// preserved. Returns a single-element slice if len(cluster) <= maxSize.
func splitLargeCluster(cluster []memNode, maxSize int) [][]memNode {
	if maxSize <= 0 || len(cluster) <= maxSize {
		return [][]memNode{cluster}
	}
	out := make([][]memNode, 0, (len(cluster)+maxSize-1)/maxSize)
	for i := 0; i < len(cluster); i += maxSize {
		end := i + maxSize
		if end > len(cluster) {
			end = len(cluster)
		}
		out = append(out, cluster[i:end])
	}
	return out
}

// clusterIDs extracts IDs from a cluster for logging.
func clusterIDs(cluster []memNode) []string {
	ids := make([]string, len(cluster))
	for i, n := range cluster {
		ids[i] = n.ID
	}
	return ids
}
