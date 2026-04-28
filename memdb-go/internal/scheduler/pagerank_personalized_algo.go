package scheduler

// pagerank_personalized_algo.go — pure power-method core for Personalized PageRank.
//
// Split out of pagerank_personalized.go to keep the orchestration file (cache
// lookup, singleflight, metrics, Redis I/O) under the 300-LOC hard cap. This
// file is dependency-light: only db.PageRankEdge + the pprDamping/pprIterations
// constants from pagerank_personalized.go.

import (
	"sort"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// computePersonalizedPageRank runs the power method with a seeded restart vector.
//
// seedSet: set of node IDs that form the personalization seed.
// Nodes in seedSet each receive 1/|seedSet| restart probability; others get 0.
// The algorithm uses continuation probability (1-α) = 0.85 with α=pprDamping=0.15.
//
// Returns (iterations_run, score_map). score_map sums to ~1.0.
// Returns (0, nil) when the graph is degenerate (no valid nodes after filtering).
func computePersonalizedPageRank(edges []db.PageRankEdge, seedSet map[string]struct{}) (int, map[string]float64) {
	// Build adjacency — mirrors computePageRank exactly.
	type weightedDst struct {
		dst    string
		weight float64
	}
	outEdges := make(map[string][]weightedDst)
	nodeSet := make(map[string]struct{})

	for _, e := range edges {
		if e.FromID == "" || e.ToID == "" || e.FromID == e.ToID {
			continue
		}
		nodeSet[e.FromID] = struct{}{}
		nodeSet[e.ToID] = struct{}{}
		w := e.Weight
		if w <= 0 {
			w = 1.0
		}
		outEdges[e.FromID] = append(outEdges[e.FromID], weightedDst{e.ToID, w})
	}

	if len(nodeSet) == 0 {
		return 0, nil
	}

	// Index nodes.
	nodes := make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes) // deterministic order for tests
	idx := make(map[string]int, len(nodes))
	for i, n := range nodes {
		idx[n] = i
	}
	n := len(nodes)

	// Personalized restart vector: seeds get 1/|seeds|; others get 0.
	// If none of the seed IDs appear in the graph, fall back to uniform prior
	// so PPR degrades gracefully to global PR without error.
	restart := make([]float64, n)
	seedCount := 0
	for _, nd := range nodes {
		if _, ok := seedSet[nd]; ok {
			seedCount++
		}
	}
	if seedCount == 0 {
		// Seeds not in graph — uniform prior (degrades to global PR).
		for i := range restart {
			restart[i] = 1.0 / float64(n)
		}
	} else {
		seedProb := 1.0 / float64(seedCount)
		for i, nd := range nodes {
			if _, ok := seedSet[nd]; ok {
				restart[i] = seedProb
			}
		}
	}

	// Normalize out-weights.
	type edge struct {
		dst, src int
		w        float64
	}
	var edgeList []edge
	for u, dsts := range outEdges {
		uid := idx[u]
		var total float64
		for _, d := range dsts {
			total += d.weight
		}
		for _, d := range dsts {
			vid := idx[d.dst]
			edgeList = append(edgeList, edge{vid, uid, d.weight / total})
		}
	}

	// Dangling nodes: sinks (no outgoing edges).
	hasSink := make([]bool, n)
	for i, nd := range nodes {
		if len(outEdges[nd]) == 0 {
			hasSink[i] = true
		}
	}

	// Power iteration with early stopping.
	rank := make([]float64, n)
	newRank := make([]float64, n)
	copy(rank, restart)

	alpha := pprDamping      // teleportation probability
	cont := 1.0 - pprDamping // continuation probability = 0.85

	iterCount := 0
	for iter := 0; iter < pprIterations; iter++ {
		iterCount = iter + 1

		// Dangling mass redistributed via personalized vector (not uniform).
		var danglingSum float64
		for i, sink := range hasSink {
			if sink {
				danglingSum += rank[i]
			}
		}

		for i := range newRank {
			// Teleport to personalized restart + dangling mass via restart vector.
			newRank[i] = alpha*restart[i] + cont*danglingSum*restart[i]
		}
		for _, e := range edgeList {
			newRank[e.dst] += cont * rank[e.src] * e.w
		}

		// Check convergence (L1 delta).
		var delta float64
		for i := range rank {
			d := newRank[i] - rank[i]
			if d < 0 {
				d = -d
			}
			delta += d
		}
		copy(rank, newRank)
		if delta < pprConvergenceThreshold {
			break
		}
	}

	result := make(map[string]float64, n)
	for i, nd := range nodes {
		result[nd] = rank[i]
	}
	return iterCount, result
}
