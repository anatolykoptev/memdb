package scheduler

// pagerank_personalized.go — F14: Personalized PageRank (HippoRAG 2, arxiv 2502.14802).
//
// ComputePersonalizedPR seeds the restart vector with query-matched entity nodes
// instead of the uniform prior used by global PageRank. This gives query-relative
// authority scores that lift associative-memory recall (cat-2) by ~+3pp per the
// HippoRAG 2 ablation study.
//
// Algorithm: same iterative power method as computePageRank (pagerank.go) but
// with a non-uniform restart vector: seed entities each receive 1/|seeds| restart
// probability; all other nodes get 0. Damping α = 0.15 (HippoRAG 2 default, which
// flips the usual convention: α here is the teleportation weight, i.e. d = 1-α = 0.85).
//
// Cache: Redis key memdb:ppr:{cube_id}:{sha1(sorted_seed_ids)}, TTL 300s.
// On cache hit the serialized map[string]float64 is returned directly.
//
// Env gates:
//   MEMDB_PPR_BLEND_PPR    — weight of PPR in final blend (default 0.7).
//   MEMDB_PPR_BLEND_GLOBAL — weight of global PR in final blend (default 0.3).

import (
	"context"
	"crypto/sha1" //nolint:gosec // SHA1 used for non-cryptographic cache key only.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/redis/go-redis/v9"
)

const (
	// pprDamping is the teleportation probability α in HippoRAG 2 (0.15).
	// The walk continuation probability is (1 - pprDamping) = 0.85.
	pprDamping = 0.15

	// pprIterations is the maximum number of power iterations before forced stop.
	pprIterations = 50

	// pprConvergenceThreshold is the delta L1-norm below which iteration stops early.
	pprConvergenceThreshold = 1e-6

	// pprCacheTTL is the Redis TTL for personalized PR cache entries.
	pprCacheTTL = 5 * time.Minute

	// pprCacheKeyPrefix is the Redis key namespace for personalized PR scores.
	pprCacheKeyPrefix = "memdb:ppr:"

	// defaultPPRBlendPPR is the default weight for PPR scores in the blend.
	defaultPPRBlendPPR = 0.7

	// defaultPPRBlendGlobal is the default weight for global PR in the blend.
	defaultPPRBlendGlobal = 0.3
)

// PPRBlendWeights returns the (pprWeight, globalWeight) for the final score blend.
// Env: MEMDB_PPR_BLEND_PPR / MEMDB_PPR_BLEND_GLOBAL. Defaults 0.7 / 0.3.
func PPRBlendWeights() (pprW, globalW float64) {
	pprW = envFloatScheduler("MEMDB_PPR_BLEND_PPR", defaultPPRBlendPPR)
	globalW = envFloatScheduler("MEMDB_PPR_BLEND_GLOBAL", defaultPPRBlendGlobal)
	return
}

// envFloatScheduler reads an env key as float64; returns def on missing/parse error.
func envFloatScheduler(key string, def float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

// pprCacheKey returns the Redis cache key for a (cubeID, seedEntityIDs) pair.
// Seeds are sorted before hashing to ensure stable keys regardless of input order.
func pprCacheKey(cubeID string, seedEntityIDs []string) string {
	sorted := make([]string, len(seedEntityIDs))
	copy(sorted, seedEntityIDs)
	sort.Strings(sorted)

	h := sha1.New() //nolint:gosec // non-cryptographic
	for _, id := range sorted {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0}) // null separator
	}
	return pprCacheKeyPrefix + cubeID + ":" + hex.EncodeToString(h.Sum(nil))
}

// ComputePersonalizedPR computes a query-seeded Personalized PageRank for the
// given cube and seed entity IDs.
//
// Returns a map from memory-node ID → PPR score (sum ≈ 1.0 within ε).
// Returns an empty map (not nil) when seedEntityIDs is empty — callers should
// check len(result) == 0 and fall back to global PR. Returns nil only on a hard
// error (Redis unavailable AND Postgres edge fetch failed).
//
// Cache behaviour: results are serialized to Redis with TTL=300s. On cache hit,
// the deserialized map is returned immediately. Metrics: ppr_compute_total.
func (w *Worker) ComputePersonalizedPR(ctx context.Context, cubeID string, seedEntityIDs []string) (map[string]float64, error) {
	mx := schedMx()

	// empty_seed fast-path — no seeds, return empty map (not error).
	if len(seedEntityIDs) == 0 {
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome("empty_seed"))
		return map[string]float64{}, nil
	}

	cacheKey := pprCacheKey(cubeID, seedEntityIDs)

	// Cache lookup — done before postgres guard so a warm cache works even
	// when pg is nil (e.g. test doubles that only stub Redis).
	if w.redis != nil {
		cached, redisErr := w.redis.Get(ctx, cacheKey).Bytes()
		if redisErr == nil {
			var cachedScores map[string]float64
			if jerr := json.Unmarshal(cached, &cachedScores); jerr == nil {
				mx.PPRRuns.Add(ctx, 1, labelPPROutcome("cache_hit"))
				mx.PPRCacheHits.Add(ctx, 1)
				mx.PPRCacheTotal.Add(ctx, 1)
				return cachedScores, nil
			}
		} else if redisErr != redis.Nil {
			w.logger.Debug("ppr: redis get failed, recomputing", "error", redisErr)
		}
		mx.PPRCacheTotal.Add(ctx, 1)
	}

	// Postgres required for recomputation.
	if w.pg == nil {
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome("fallback_global"))
		return map[string]float64{}, nil
	}

	// Fetch edges.
	edges, err := w.pg.FetchEdgesForPageRank(ctx, cubeID)
	if err != nil {
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome("fallback_global"))
		return nil, fmt.Errorf("ppr: fetch edges cube=%s: %w", cubeID, err)
	}
	if len(edges) == 0 {
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome("empty_seed"))
		return map[string]float64{}, nil
	}

	// Build seed set for O(1) lookup.
	seedSet := make(map[string]struct{}, len(seedEntityIDs))
	for _, id := range seedEntityIDs {
		seedSet[id] = struct{}{}
	}

	// Compute PPR.
	iterCount, scores := computePersonalizedPageRank(edges, seedSet)
	mx.PPRIterCount.Record(ctx, int64(iterCount))

	if len(scores) == 0 {
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome("fallback_global"))
		return map[string]float64{}, nil
	}

	// Store in cache (best-effort — don't fail the caller on cache write errors).
	if w.redis != nil {
		if b, jerr := json.Marshal(scores); jerr == nil {
			_ = w.redis.Set(ctx, cacheKey, b, pprCacheTTL).Err()
		}
	}

	mx.PPRRuns.Add(ctx, 1, labelPPROutcome("success"))
	return scores, nil
}

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
