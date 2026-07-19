package db

// vset_eviction.go — per-cube VSET eviction policy.
//
// When a cube's WorkingMemory VSET exceeds MEMDB_VSET_PER_CUBE_CAP the
// VSetEvictionPolicy evicts the oldest entries (by SETATTR ts) so dedup
// candidates stay fresh. A per-cube cap is required because Redis
// allkeys-lru is global — without it one hot cube starves cold cubes and
// stale dedup candidates survive, leading to wrong LLM consolidation merges
// and data corruption in Postgres.
//
// Eviction is hooked into VAdd (non-fatal on failure) and is oldest-first:
//   1. VCard  → current element count
//   2. VSim   → all candidates with their ts (COUNT = count, minTS = 0)
//   3. sort   → ascending ts, take the oldest (count - cap + evictBatch)
//   4. VRemBatch → remove them in one pipeline
//
// The evictBatch overshoot amortises eviction across consecutive VAdd calls
// so we don't evict on every single insert once the cap is reached.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// vsetEvictBatch is the number of extra entries evicted beyond (count - cap)
// to amortise eviction across consecutive VAdd calls.
const vsetEvictBatch = 10

// vsetEvictOp is the op label recorded with the VsetEvictTotal metric.
const vsetEvictOp = "per_cube_cap"

// vsetEvictionTarget is the subset of WorkingMemoryCache methods the
// eviction policy depends on. *WorkingMemoryCache satisfies this interface
// implicitly. It exists so tests can mock the Redis layer without a live
// Redis 8 instance (miniredis does not support VSET commands).
type vsetEvictionTarget interface {
	VCard(ctx context.Context, cubeID string) (int64, error)
	VSimFiltered(ctx context.Context, cubeID string, queryEmbedding []float32, topN int, minTS int64) ([]VSetCandidate, error)
	VRemBatch(ctx context.Context, cubeID string, nodeIDs []string) error
}

// VSetEvictionPolicy enforces a per-cube cap on the VSET hot cache by
// evicting oldest-first entries when VCard exceeds the configured cap.
type VSetEvictionPolicy struct {
	cap      int
	embedDim int // dimension of the zero-vector query for VSim (original embedder dim)
	cache    vsetEvictionTarget
	logger   *slog.Logger
}

// NewVSetEvictionPolicy creates a policy with the given cap. embedDim is the
// original embedding dimension (before REDUCE) used to build a zero-vector
// query for VSim; Redis applies the key's REDUCE projection automatically.
func NewVSetEvictionPolicy(cap int, embedDim int, cache vsetEvictionTarget, logger *slog.Logger) *VSetEvictionPolicy {
	if cap < 0 {
		cap = 0
	}
	if embedDim <= 0 {
		embedDim = 1024 // multilingual-e5-large default
	}
	return &VSetEvictionPolicy{
		cap:      cap,
		embedDim: embedDim,
		cache:    cache,
		logger:   logger,
	}
}

// CheckAndEvict checks the VSET cardinality for cubeID and, if it exceeds the
// cap, evicts the oldest entries (by ts) to bring it back under cap. The
// return value is non-fatal to VAdd — callers log but do not fail the insert.
//
// Metric outcomes:
//   - miss    — count <= cap, no eviction needed
//   - success — VRemBatch evicted oldest entries
//   - error   — VCard, VSim, or VRemBatch returned an error
func (p *VSetEvictionPolicy) CheckAndEvict(ctx context.Context, cubeID string) error {
	count, err := p.cache.VCard(ctx, cubeID)
	if err != nil {
		observability.RecordVsetEvict(ctx, vsetEvictOp, "error")
		return fmt.Errorf("vset eviction vcard %s: %w", cubeID, err)
	}

	// Under or at cap — no eviction needed.
	if count <= int64(p.cap) {
		observability.RecordVsetEvict(ctx, vsetEvictOp, "miss")
		return nil
	}

	// Number of entries to evict: the overflow plus a batch overshoot so we
	// don't evict on every single subsequent VAdd.
	evictCount := int(count) - p.cap + vsetEvictBatch

	// Fetch ALL elements (COUNT = count) so we can sort by ts and pick the
	// truly oldest. VSimFiltered with a zero vector and minTS=0 returns
	// every element with its ts attribute; the similarity score is
	// irrelevant for eviction — only the ts ordering matters.
	zeroVec := make([]float32, p.embedDim)
	candidates, err := p.cache.VSimFiltered(ctx, cubeID, zeroVec, int(count), 0)
	if err != nil {
		observability.RecordVsetEvict(ctx, vsetEvictOp, "error")
		return fmt.Errorf("vset eviction vsim %s: %w", cubeID, err)
	}
	if len(candidates) == 0 {
		// VCard said > cap but VSim returned nothing — the key may have been
		// dropped concurrently. Treat as a miss, not an error.
		observability.RecordVsetEvict(ctx, vsetEvictOp, "miss")
		return nil
	}

	// Sort oldest-first by ts so we evict stale dedup candidates, not fresh
	// ones. This is the correctness-critical step: evicting the wrong entries
	// leaves stale candidates that cause wrong LLM consolidation merges.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].TS < candidates[j].TS
	})

	if evictCount > len(candidates) {
		evictCount = len(candidates)
	}

	nodeIDs := make([]string, 0, evictCount)
	for i := 0; i < evictCount; i++ {
		nodeIDs = append(nodeIDs, candidates[i].ID)
	}

	if err := p.cache.VRemBatch(ctx, cubeID, nodeIDs); err != nil {
		observability.RecordVsetEvict(ctx, vsetEvictOp, "error")
		return fmt.Errorf("vset eviction vrembatch %s: %w", cubeID, err)
	}

	p.logger.DebugContext(ctx, "vset eviction: evicted oldest entries",
		slog.String("cube_id", cubeID),
		slog.Int64("count", count),
		slog.Int("cap", p.cap),
		slog.Int("evicted", evictCount),
	)

	observability.RecordVsetEvict(ctx, vsetEvictOp, "success")
	return nil
}
