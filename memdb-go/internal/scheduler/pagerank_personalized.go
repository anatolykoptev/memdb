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
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

// pprSingleflight deduplicates concurrent ComputePersonalizedPR calls for the
// same (cubeID, sorted seedIDs) tuple. Without it, N parallel queries on a cold
// cache each pay the full Postgres FetchEdgesForPageRank + power-iteration cost
// (~10–100 ms × graph size). With it, the first caller computes and the rest
// receive the same map by reference. Map values are read-only after the
// recompute path returns; callers MUST NOT mutate them. The single SearchService
// caller (blendPPRIntoMetadata) only reads.
var pprSingleflight singleflight.Group

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
	pprW = envcfg.Float("MEMDB_PPR_BLEND_PPR", defaultPPRBlendPPR)
	globalW = envcfg.Float("MEMDB_PPR_BLEND_GLOBAL", defaultPPRBlendGlobal)
	return
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
	start := time.Now()

	// recordDuration emits personalized_pr_duration_ms{outcome=...} on every
	// return path. Bound to a pointer so we can update outcome late and still
	// record once via defer.
	outcome := "success"
	defer func() {
		mx.PPRDurationMs.Record(ctx, float64(time.Since(start).Milliseconds()),
			metric.WithAttributes(attribute.String("outcome", outcome)))
	}()

	// empty_seed fast-path — no seeds, return empty map (not error).
	if len(seedEntityIDs) == 0 {
		outcome = "empty_seed"
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome(outcome))
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
				outcome = "cache_hit"
				mx.PPRRuns.Add(ctx, 1, labelPPROutcome(outcome))
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
		outcome = "fallback_global"
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome(outcome))
		return map[string]float64{}, nil
	}

	// Singleflight: dedupe concurrent recomputes for the same cache key.
	// Sub-result.Shared is informational only — semantically every caller in a
	// flight gets the same map by reference, callers must not mutate.
	v, sfErr, _ := pprSingleflight.Do(cacheKey, func() (any, error) {
		return w.recomputePersonalizedPR(ctx, cubeID, cacheKey, seedEntityIDs)
	})
	if sfErr != nil {
		outcome = "fallback_global"
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome(outcome))
		return nil, sfErr
	}
	res, ok := v.(pprComputeResult)
	if !ok {
		// Defensive: should never happen — recomputePersonalizedPR always returns pprComputeResult.
		outcome = "fallback_global"
		mx.PPRRuns.Add(ctx, 1, labelPPROutcome(outcome))
		return map[string]float64{}, nil
	}
	outcome = res.outcome
	mx.PPRRuns.Add(ctx, 1, labelPPROutcome(outcome))
	return res.scores, nil
}

// pprComputeResult bundles the (scores, outcome) tuple returned by
// recomputePersonalizedPR through singleflight.
type pprComputeResult struct {
	scores  map[string]float64
	outcome string
}

// recomputePersonalizedPR performs the cold-path PPR computation: fetches
// edges from Postgres, runs the power-method, and writes the result into Redis
// (best-effort). Wrapped by singleflight in the caller so concurrent requests
// for the same cache key share a single Postgres + compute round.
func (w *Worker) recomputePersonalizedPR(
	ctx context.Context,
	cubeID, cacheKey string,
	seedEntityIDs []string,
) (pprComputeResult, error) {
	mx := schedMx()

	edges, err := w.pg.FetchEdgesForPageRank(ctx, cubeID)
	if err != nil {
		return pprComputeResult{outcome: "fallback_global"},
			fmt.Errorf("ppr: fetch edges cube=%s: %w", cubeID, err)
	}
	if len(edges) == 0 {
		return pprComputeResult{scores: map[string]float64{}, outcome: "empty_seed"}, nil
	}

	seedSet := make(map[string]struct{}, len(seedEntityIDs))
	for _, id := range seedEntityIDs {
		seedSet[id] = struct{}{}
	}

	iterCount, scores := computePersonalizedPageRank(edges, seedSet)
	mx.PPRIterCount.Record(ctx, int64(iterCount))

	if len(scores) == 0 {
		return pprComputeResult{scores: map[string]float64{}, outcome: "fallback_global"}, nil
	}

	if w.redis != nil {
		if b, jerr := json.Marshal(scores); jerr == nil {
			_ = w.redis.Set(ctx, cacheKey, b, pprCacheTTL).Err()
		}
	}
	return pprComputeResult{scores: scores, outcome: "success"}, nil
}

// (computePersonalizedPageRank lives in pagerank_personalized_algo.go.)
