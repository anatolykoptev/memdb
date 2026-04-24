package scheduler

// tree_reorganizer.go — D3 clustering primitives.
//
// Port of Python `tree_text_memory/organize/reorganizer.py`'s `_partition`.
// Uses local cosine + Union-Find (no sklearn) since the input is bounded
// (< 500 nodes per cycle) and thresholds are tier-specific rather than
// k-means-derived. LLM tier summarisation lives in tree_summariser.go to
// keep each file concern-bounded.

import (
	"math"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// hierarchyNode is the in-memory representation of a memory during tree
// clustering. Embedding is required — rows without one are filtered out
// upstream by ListMemoriesByHierarchyLevel's `embedding IS NOT NULL` predicate.
type hierarchyNode struct {
	ID        string
	Text      string
	UserID    string
	Embedding []float32
}

// clusterByCosine groups memories into clusters connected by pairwise cosine
// similarity ≥ threshold. Uses Union-Find — same structure as
// buildClusters in reorganizer_cluster.go but on in-memory embeddings.
//
// Clusters smaller than minSize are dropped (noise suppression — mirrors
// Python's min_cluster_size filter).
func clusterByCosine(mems []db.HierarchyMemory, threshold float64, minSize int) [][]hierarchyNode {
	if len(mems) < minSize {
		return nil
	}
	nodes := make([]hierarchyNode, 0, len(mems))
	for _, m := range mems {
		if len(m.Embedding) == 0 {
			continue
		}
		nodes = append(nodes, hierarchyNode{ID: m.ID, Text: m.Text, UserID: m.UserID, Embedding: m.Embedding})
	}
	if len(nodes) < minSize {
		return nil
	}

	parent := make(map[int]int, len(nodes))
	for i := range nodes {
		parent[i] = i
	}
	var find func(x int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			if cosineBetween(nodes[i].Embedding, nodes[j].Embedding) >= threshold {
				union(i, j)
			}
		}
	}

	groups := make(map[int][]hierarchyNode)
	for i := range nodes {
		root := find(i)
		groups[root] = append(groups[root], nodes[i])
	}
	out := make([][]hierarchyNode, 0, len(groups))
	for _, g := range groups {
		if len(g) >= minSize {
			out = append(out, g)
		}
	}
	return out
}

// cosineBetween returns cosine similarity in [-1, 1]. No allocation.
func cosineBetween(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
