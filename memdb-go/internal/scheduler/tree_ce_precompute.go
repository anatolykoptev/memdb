package scheduler

// tree_ce_precompute.go — D3 cross-encoder precompute pass (M10 Stream 6).
//
// Runs after the raw → episodic → semantic promotion in
// RunTreeReorgForCube. For every raw-tier memory in the cube we:
//
//  1. Find its top-K nearest neighbours by cosine over the in-memory raw
//     batch (already loaded by ListMemoriesByHierarchyLevel earlier in
//     the cycle — no extra DB round-trip for neighbour discovery).
//  2. Call the rerank.Client with the memory's text as "query" and the
//     neighbour texts as "documents", giving us BGE-rerank-v2-m3
//     pairwise scores.
//  3. Persist the top-K neighbours sorted DESC by CE score into
//     Memory.properties->>'ce_score_topk' via SetCEScoresTopK.
//
// At search time, cross_encoder_precompute.go reads these scores and
// short-circuits the live rerank HTTP call when the candidate set is a
// neighbourhood of the strongest-cosine result.
//
// Cost model:
//   - 1 rerank HTTP call per raw memory (batched K docs in a single call).
//   - 1 SQL UPDATE per raw memory (jsonb_set on ce_score_topk).
//   - O(N²) cosine in Go inside cePrecomputeNeighbours — bounded by
//     rawCandidateLimit (500), so worst-case 250k float32 dot-products
//     per cycle, well under D3's existing budget.
//
// Env gate: MEMDB_CE_PRECOMPUTE (default true). Set to "false" to skip
// the pass entirely.

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"sync"

	"github.com/anatolykoptev/go-kit/rerank"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

const (
	// cePrecomputeTopK is the number of neighbours scored per memory.
	// 10 mirrors the wmRefreshTopK budget — small enough to fit in a
	// single rerank call (well under the rerank.defaultMaxDocs cap of
	// 50) and large enough to cover the typical search-result window.
	cePrecomputeTopK = 10
)

// cePrecomputeEnabledEnv is the scheduler-side mirror of the search-side
// cePrecomputeEnabled. Both functions key on MEMDB_CE_PRECOMPUTE — single
// source of truth.
func cePrecomputeEnabledEnv() bool {
	v := os.Getenv("MEMDB_CE_PRECOMPUTE")
	if v == "" {
		return true
	}
	return v != "false"
}

// cePrecomputeMathPrefilterEnabled returns true when
// MEMDB_CE_PRECOMPUTE_MATH_PREFILTER=1. Default is false (disabled).
func cePrecomputeMathPrefilterEnabled() bool {
	return os.Getenv("MEMDB_CE_PRECOMPUTE_MATH_PREFILTER") == "1"
}

// cePrecomputeCosineThreshold returns the cosine threshold for the math
// prefilter from MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD. Default 0.3.
func cePrecomputeCosineThreshold() float64 {
	return envcfg.FloatRange("MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD", 0.3, 0, 1)
}

// ceMathPrefilterMetrics holds OTel counters for the math prefilter pass.
var (
	ceMathOnce     sync.Once
	ceMathSkipped  metric.Int64Counter
	ceMathKept     metric.Int64Counter
)

// ceMathMx lazily initialises the math-prefilter OTel counters.
func ceMathMx() (skipped, kept metric.Int64Counter) {
	ceMathOnce.Do(func() {
		meter := otel.Meter("memdb-go/scheduler")
		ceMathSkipped, _ = meter.Int64Counter(
			"memdb.scheduler.ce_precompute_math_skipped_total",
			metric.WithDescription("CE precompute pairs dropped by math prefilter (cosine < threshold)"),
		)
		ceMathKept, _ = meter.Int64Counter(
			"memdb.scheduler.ce_precompute_math_kept_total",
			metric.WithDescription("CE precompute pairs passed by math prefilter (sent to bge-reranker)"),
		)
	})
	return ceMathSkipped, ceMathKept
}

// ceFloorMetrics holds the OTel counter for the legacy neighbour-floor pass.
var (
	ceFloorOnce    sync.Once
	ceFloorDropped metric.Int64Counter
)

// ceFloorMx lazily initialises the legacy floor counter.
// Exposed via MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD (same env as B1 math
// prefilter) so both filters share one observable threshold.
func ceFloorMx() metric.Int64Counter {
	ceFloorOnce.Do(func() {
		meter := otel.Meter("memdb-go/scheduler")
		ceFloorDropped, _ = meter.Int64Counter(
			"memdb.scheduler.ce_precompute_floor_dropped_total",
			metric.WithDescription("CE precompute pairs dropped by legacy cosine floor (=B1 threshold env)"),
		)
	})
	return ceFloorDropped
}

// applyMathPrefilter filters neighbours by cosine similarity to the anchor.
// Returns the filtered slice (kept pairs) and counts skipped + kept.
//
// Bypass cases (graceful degrade):
//   - anchor.Embedding is empty → return all neighbours unchanged (test #4).
//   - a neighbour.Embedding is empty → keep that neighbour (test #5).
func applyMathPrefilter(anchor db.HierarchyMemory, neighbours []db.HierarchyMemory, threshold float64) (filtered []db.HierarchyMemory, nSkipped, nKept int) {
	if len(anchor.Embedding) == 0 {
		// Cannot compute cosine without anchor embedding — bypass entirely.
		return neighbours, 0, len(neighbours)
	}
	filtered = make([]db.HierarchyMemory, 0, len(neighbours))
	for _, n := range neighbours {
		if len(n.Embedding) == 0 {
			// Neighbour without embedding — keep (graceful degrade).
			filtered = append(filtered, n)
			nKept++
			continue
		}
		cos := cosineBetween(anchor.Embedding, n.Embedding)
		if cos >= threshold {
			filtered = append(filtered, n)
			nKept++
		} else {
			nSkipped++
		}
	}
	return filtered, nSkipped, nKept
}

// runCEPrecomputePass scores top-K neighbours per raw memory and
// persists ce_score_topk on each. Non-fatal: errors logged, the pass
// continues with the next memory. Skipped entirely when:
//
//   - MEMDB_CE_PRECOMPUTE=false
//   - Reorganizer.rerankClient is nil (not wired by main / search not
//     configured to use a CE backend)
//   - len(rawMems) < 2 (no possible pairs)
//
// Caller (RunTreeReorgForCube) passes the same rawMems slice it already
// loaded for clustering — we reuse the in-memory embeddings for cosine
// neighbour discovery.
func (r *Reorganizer) runCEPrecomputePass(
	ctx context.Context,
	cubeID string,
	rawMems []db.HierarchyMemory,
) {
	if !cePrecomputeEnabledEnv() {
		return
	}
	if r.rerankClient == nil || !r.rerankClient.Available() {
		return
	}
	if len(rawMems) < 2 {
		return
	}

	log := r.logger.With(
		slog.String("cube_id", cubeID),
		slog.String("component", "ce_precompute"),
	)
	log.Debug("ce precompute: starting pass", slog.Int("candidates", len(rawMems)))

	mathPrefilter := cePrecomputeMathPrefilterEnabled()
	mathThreshold := cePrecomputeCosineThreshold()

	scored := 0
	for i := range rawMems {
		select {
		case <-ctx.Done():
			log.Warn("ce precompute: ctx cancelled mid-pass",
				slog.Int("scored", scored), slog.Int("total", len(rawMems)))
			return
		default:
		}
		mem := rawMems[i]
		if mem.ID == "" || mem.Text == "" || len(mem.Embedding) == 0 {
			continue
		}
		neighbours := cePrecomputeNeighbours(ctx, mem, rawMems, cePrecomputeTopK)
		if len(neighbours) == 0 {
			continue
		}

		if mathPrefilter {
			var nSkipped, nKept int
			neighbours, nSkipped, nKept = applyMathPrefilter(mem, neighbours, mathThreshold)
			skippedCtr, keptCtr := ceMathMx()
			if nSkipped > 0 {
				skippedCtr.Add(ctx, int64(nSkipped))
			}
			if nKept > 0 {
				keptCtr.Add(ctx, int64(nKept))
			}
			if len(neighbours) == 0 {
				continue
			}
		}

		entries := cePrecomputeScoreNeighbours(ctx, r.rerankClient, mem.Text, neighbours)
		if len(entries) == 0 {
			continue
		}
		if err := r.cePrecomputeStore(ctx, mem.ID, cubeID, entries); err != nil {
			log.Debug("ce precompute: store failed",
				slog.String("memory_id", mem.ID), slog.Any("error", err))
			continue
		}
		scored++
	}

	log.Info("ce precompute: pass complete",
		slog.Int("scored", scored),
		slog.Int("candidates", len(rawMems)))
}

// cePrecomputeNeighbours returns the top-K nearest cosine neighbours of
// `target` from the `pool`, excluding the target itself. Filters out pairs
// whose cosine similarity is below cePrecomputeCosineThreshold()
// (MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD, default 0.3) — they're dead weight
// at search time. Dropped pairs are counted via the
// memdb.scheduler.ce_precompute_floor_dropped_total metric.
func cePrecomputeNeighbours(ctx context.Context, target db.HierarchyMemory, pool []db.HierarchyMemory, topK int) []db.HierarchyMemory {
	type scored struct {
		idx int
		cos float64
	}
	threshold := cePrecomputeCosineThreshold()
	floorCtr := ceFloorMx()
	candidates := make([]scored, 0, len(pool)-1)
	for i := range pool {
		if pool[i].ID == target.ID || pool[i].ID == "" || len(pool[i].Embedding) == 0 || pool[i].Text == "" {
			continue
		}
		c := cosineBetween(target.Embedding, pool[i].Embedding)
		if c < threshold {
			floorCtr.Add(ctx, 1)
			continue
		}
		candidates = append(candidates, scored{idx: i, cos: c})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].cos > candidates[b].cos
	})
	if topK > 0 && len(candidates) > topK {
		candidates = candidates[:topK]
	}
	out := make([]db.HierarchyMemory, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, pool[c.idx])
	}
	return out
}

// cePrecomputeScoreNeighbours fires one rerank HTTP call (memory.text as
// query, neighbour texts as docs) and returns entries sorted DESC by CE
// score. Returns nil on rerank error (best-effort — pass continues).
func cePrecomputeScoreNeighbours(
	ctx context.Context,
	client *rerank.Client,
	queryText string,
	neighbours []db.HierarchyMemory,
) []db.CEScoreEntry {
	docs := make([]rerank.Doc, 0, len(neighbours))
	for _, n := range neighbours {
		docs = append(docs, rerank.Doc{ID: n.ID, Text: n.Text})
	}
	if len(docs) == 0 {
		return nil
	}
	scored := client.Rerank(ctx, queryText, docs)
	entries := make([]db.CEScoreEntry, 0, len(scored))
	for _, s := range scored {
		if s.ID == "" {
			continue
		}
		entries = append(entries, db.CEScoreEntry{NeighborID: s.ID, Score: s.Score})
	}
	// rerank.Client already sorts head DESC, but defensive: enforce DESC
	// on the caller side so persistence is order-stable regardless of
	// future client changes.
	sort.SliceStable(entries, func(a, b int) bool {
		return entries[a].Score > entries[b].Score
	})
	return entries
}

// cePrecomputeWriter is the optional persistence interface that
// reorgPostgres implementations may satisfy. Keeping it optional lets
// existing test spies skip the method without compile errors.
type cePrecomputeWriter interface {
	SetCEScoresTopK(ctx context.Context, memoryID, cubeID string, entries []db.CEScoreEntry) error
}

func (r *Reorganizer) cePrecomputeStore(ctx context.Context, memID, cubeID string, entries []db.CEScoreEntry) error {
	w, ok := r.postgres.(cePrecomputeWriter)
	if !ok {
		return nil
	}
	return w.SetCEScoresTopK(ctx, memID, cubeID, entries)
}
