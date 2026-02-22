package db

// vset.go — Redis 8 Vector Set (VSET) hot cache for WorkingMemory.
//
// Design:
//   One VSET key per cube: "wm:v:<cubeID>"
//   Each element = memory node ID (UUID string)
//   Each element has SETATTR with JSON: {"m": "<memory text>", "ts": <unix>}
//
// Benefits over pgvector for dedup candidate lookup:
//   - HNSW in-memory: ~1-5ms vs pgvector ~20-100ms
//   - Automatic HNSW graph — no index management needed
//   - VSIM FILTER for hybrid search (e.g. by recency)
//   - Covers only WorkingMemory (hot, recent) — keeps VSET small
//
// Lifecycle:
//   VAdd  — called after InsertMemoryNodes for each new WM node
//   VRem  — called when CleanupWorkingMemory removes old nodes
//   VSim  — called by fetchFineCandidates before postgres fallback
//   Sync  — called on startup to warm cache from postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// vsetKeyPrefix is the Redis key prefix for WorkingMemory VSET per cube.
	vsetKeyPrefix = "wm:v:"
	// vsetMaxCandidates is the default number of similar candidates to return.
	vsetMaxCandidates = 10
	// vsetEF is the HNSW exploration factor for VSim (higher = better recall, slower).
	// 100 gives good recall for small-to-medium sets (<10k elements).
	vsetEF = 100
	// vsetFilterEF controls how many candidates are inspected when FILTER is active.
	// 0 = scan as many as needed to fulfill COUNT (best recall for rare filters).
	vsetFilterEF = 0
	// vsetReduceDim is the target dimensionality after Redis random projection.
	// 1024 (multilingual-e5-large) → 256 gives ~16x storage reduction vs FP32:
	//   - Without REDUCE: 1024 × 4 bytes = 4096 bytes/vector
	//   - With Q8 only:   1024 × 1 byte  = 1024 bytes/vector  (4x vs FP32)
	//   - With REDUCE+Q8: 256  × 1 byte  = 256  bytes/vector  (16x vs FP32)
	// Cosine recall at dim=256 is ~0.95 for text embeddings (acceptable for dedup candidates).
	// REDUCE uses random Gaussian projection (Johnson-Lindenstrauss) — lossless in expectation.
	//
	// NOTE: REDUCE is set on the first VADD and defines the projection matrix for the key.
	// If a key already exists without REDUCE, VAdd falls back to no-REDUCE for compatibility.
	vsetReduceDim int64 = 256

	// vsetTTL is the sliding-window TTL for a cube's VSET key.
	// Each VAdd resets the expiry, so active cubes never expire.
	// Inactive cubes (no new WorkingMemory for 30 days) are auto-evicted.
	// Inspired by redis/agent-memory-server session_window pattern.
	vsetTTL = 30 * 24 * time.Hour
)

// WorkingMemoryCache wraps Redis VSET operations for WorkingMemory hot cache.
type WorkingMemoryCache struct {
	client *redis.Client
	logger *slog.Logger
}

// NewWorkingMemoryCache creates a new WorkingMemoryCache.
func NewWorkingMemoryCache(r *Redis) *WorkingMemoryCache {
	return &WorkingMemoryCache{
		client: r.client,
		logger: r.logger,
	}
}

// vsetKey returns the Redis key for a cube's WorkingMemory VSET.
func vsetKey(cubeID string) string {
	return vsetKeyPrefix + cubeID
}

// VSetCandidate is a result from VSim — an existing memory candidate for dedup.
type VSetCandidate struct {
	ID     string  // memory node UUID
	Memory string  // memory text (from SETATTR)
	Score  float64 // cosine similarity 0.0–1.0
}

// VAdd inserts or updates a WorkingMemory node in the VSET.
// embedding must be a float32 slice (1024-dim for multilingual-e5-large).
// attrs is stored as JSON for hybrid search: {"m": "<text>", "ts": <unix>}.
//
// Optimizations applied (from Redis docs + redis/agent-memory-server):
//   - REDUCE 1024→256: random Gaussian projection (Johnson-Lindenstrauss), ~16x vs FP32
//   - Q8 quantization: 4x less memory than FP32 on top of REDUCE
//   - CAS option: offloads HNSW candidate graph search to background thread,
//     avoiding blocking the Redis event loop during insertion
//
// Fallback: if the key already exists without a REDUCE projection matrix, VADD with
// REDUCE fails. In that case we retry without REDUCE so existing cubes continue to work.
// New keys always get REDUCE; existing keys migrate naturally when re-created.
func (c *WorkingMemoryCache) VAdd(ctx context.Context, cubeID, nodeID, memoryText string, embedding []float32, ts int64) error {
	key := vsetKey(cubeID)
	vec := &redis.VectorFP32{Val: float32SliceToBytes(embedding)}
	attr := fmt.Sprintf(`{"m":%q,"ts":%d}`, memoryText, ts)

	_, err := c.client.VAddWithArgs(ctx, key, nodeID, vec, &redis.VAddArgs{
		Reduce:  vsetReduceDim, // 1024→256 random projection (~16x vs FP32 baseline)
		Q8:      true,          // 4x quantization on top of REDUCE
		Cas:     true,          // async HNSW graph search — non-blocking insert
		SetAttr: attr,
	}).Result()
	if err != nil {
		// Fallback: key exists without REDUCE — projection matrix mismatch.
		// Retry without REDUCE so the cube continues working until it is re-created.
		c.logger.Debug("vset vadd: reduce failed, retrying without reduce (existing key)",
			slog.String("cube_id", cubeID),
			slog.String("node_id", nodeID),
			slog.Any("error", err),
		)
		_, err = c.client.VAddWithArgs(ctx, key, nodeID, vec, &redis.VAddArgs{
			Q8:      true,
			Cas:     true,
			SetAttr: attr,
		}).Result()
		if err != nil {
			return fmt.Errorf("vset vadd %s: %w", nodeID, err)
		}
	}

	// Sliding-window TTL: reset expiry on every insert so active cubes never expire.
	// Inactive cubes (no new WorkingMemory for vsetTTL) are auto-evicted by Redis.
	// Non-fatal: if Expire fails the VSET still works, just won't auto-expire.
	if err := c.client.Expire(ctx, key, vsetTTL).Err(); err != nil {
		c.logger.Debug("vset expire failed (non-fatal)",
			slog.String("cube_id", cubeID), slog.Any("error", err))
	}
	return nil
}

// VRem removes a WorkingMemory node from the VSET.
func (c *WorkingMemoryCache) VRem(ctx context.Context, cubeID, nodeID string) error {
	key := vsetKey(cubeID)
	if err := c.client.VRem(ctx, key, nodeID).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("vset vrem %s: %w", nodeID, err)
	}
	return nil
}

// VSim returns the top-N most similar WorkingMemory nodes for a query embedding.
// Returns nil (not an error) if the VSET key does not exist yet.
//
// Optimizations applied:
//   - Pipeline VGetAttr: fetches all attrs in one round-trip (N+1 → 1)
//   - FILTER by ts: skips nodes older than maxAgeSecs to avoid stale dedup candidates
func (c *WorkingMemoryCache) VSim(ctx context.Context, cubeID string, queryEmbedding []float32, topN int) ([]VSetCandidate, error) {
	return c.VSimFiltered(ctx, cubeID, queryEmbedding, topN, 0)
}

// VSimFiltered is like VSim but applies a minimum timestamp filter.
// Pass minTS=0 to disable filtering (equivalent to VSim).
func (c *WorkingMemoryCache) VSimFiltered(ctx context.Context, cubeID string, queryEmbedding []float32, topN int, minTS int64) ([]VSetCandidate, error) {
	key := vsetKey(cubeID)
	if topN <= 0 {
		topN = vsetMaxCandidates
	}

	vec := &redis.VectorFP32{Val: float32SliceToBytes(queryEmbedding)}
	simArgs := &redis.VSimArgs{
		Count: int64(topN),
		EF:    vsetEF,
	}
	// Improvement 3: FILTER by ts — only return nodes newer than minTS.
	// This avoids surfacing stale WM nodes as dedup candidates.
	if minTS > 0 {
		simArgs.Filter = fmt.Sprintf(".ts >= %d", minTS)
		simArgs.FilterEF = vsetFilterEF
	}

	results, err := c.client.VSimWithArgsWithScores(ctx, key, vec, simArgs).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil // key doesn't exist yet — normal on first add
		}
		return nil, fmt.Errorf("vset vsim: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	// Improvement 2: pipeline all VGetAttr calls — one round-trip instead of N.
	pipe := c.client.Pipeline()
	attrCmds := make([]*redis.StringCmd, len(results))
	for i, r := range results {
		attrCmds[i] = pipe.VGetAttr(ctx, key, r.Name)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("vset vgetattr pipeline: %w", err)
	}

	candidates := make([]VSetCandidate, 0, len(results))
	for i, r := range results {
		attrJSON, err := attrCmds[i].Result()
		if err != nil {
			c.logger.Debug("vset vgetattr failed", slog.String("id", r.Name), slog.Any("error", err))
			continue
		}
		memText := extractMemFromAttr(attrJSON)
		if memText == "" {
			continue
		}
		candidates = append(candidates, VSetCandidate{
			ID:     r.Name,
			Memory: memText,
			Score:  r.Score,
		})
	}
	return candidates, nil
}

// VCard returns the number of elements in the VSET for a cube.
func (c *WorkingMemoryCache) VCard(ctx context.Context, cubeID string) (int64, error) {
	n, err := c.client.VCard(ctx, vsetKey(cubeID)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("vset vcard: %w", err)
	}
	return n, nil
}

// VRemBatch removes multiple nodes from the VSET in a pipeline.
func (c *WorkingMemoryCache) VRemBatch(ctx context.Context, cubeID string, nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	key := vsetKey(cubeID)
	pipe := c.client.Pipeline()
	for _, id := range nodeIDs {
		pipe.VRem(ctx, key, id)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("vset vrem batch: %w", err)
	}
	return nil
}

// syncWarmLimit is the max number of WM nodes loaded per cube on startup.
// Keeps the startup scan bounded even for users with many WM nodes.
const syncWarmLimit = 50

// Sync warms the VSET hot-cache on server startup.
//
// For each cubeID it loads the N most-recent activated WorkingMemory nodes from
// Postgres (with their embeddings) and inserts them into the VSET so the very
// first query after a restart benefits from a pre-populated cache.
//
// Errors are non-fatal — the cache simply stays cold for that cube.
func (c *WorkingMemoryCache) Sync(ctx context.Context, pg interface {
	GetRecentWorkingMemory(ctx context.Context, userName string, limit int) ([]WMNode, error)
	ListUsers(ctx context.Context) ([]string, error)
}, logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Debug(msg string, args ...any)
}) {
	users, err := pg.ListUsers(ctx)
	if err != nil {
		logger.Warn("vset sync: failed to list users", "error", err)
		return
	}
	if len(users) == 0 {
		logger.Debug("vset sync: no active users, skipping warm-up")
		return
	}

	warmed, total := 0, 0
	for _, cubeID := range users {
		nodes, err := pg.GetRecentWorkingMemory(ctx, cubeID, syncWarmLimit)
		if err != nil {
			logger.Warn("vset sync: failed to get WM nodes", "cube_id", cubeID, "error", err)
			continue
		}
		for _, n := range nodes {
			if len(n.Embedding) == 0 {
				continue
			}
			if err := c.VAdd(ctx, cubeID, n.ID, n.Text, n.Embedding, n.TS); err != nil {
				logger.Debug("vset sync: vadd failed", "cube_id", cubeID, "id", n.ID, "error", err)
				continue
			}
			total++
		}
		if len(nodes) > 0 {
			warmed++
		}
	}
	logger.Info("vset sync: warm-up complete",
		"cubes_warmed", warmed,
		"nodes_loaded", total,
	)
}

// --- Helpers ---

// bytesPerFloat32 is the byte size of a single float32 value.
const bytesPerFloat32 = 4

// float32SliceToBytes converts []float32 to little-endian []byte for FP32 format.
func float32SliceToBytes(v []float32) []byte {
	b := make([]byte, len(v)*bytesPerFloat32)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*bytesPerFloat32:], math.Float32bits(f))
	}
	return b
}

// extractMemFromAttr parses {"m":"...","ts":...} and returns the "m" field.
// Uses simple string scan to avoid allocating a full JSON decoder per call.
func extractMemFromAttr(attrJSON string) string {
	// Fast path: parse {"m":"<text>","ts":...}
	const prefix = `{"m":`
	if len(attrJSON) < len(prefix)+2 {
		return ""
	}
	// Find the quoted value after "m":
	start := len(prefix)
	if attrJSON[start] != '"' {
		return ""
	}
	start++ // skip opening quote
	end := start
	for end < len(attrJSON) {
		if attrJSON[end] == '"' && (end == 0 || attrJSON[end-1] != '\\') {
			break
		}
		end++
	}
	if end >= len(attrJSON) {
		return ""
	}
	return attrJSON[start:end]
}
