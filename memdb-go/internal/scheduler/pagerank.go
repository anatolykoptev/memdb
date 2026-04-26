package scheduler

// pagerank.go — M10 Stream 7: background PageRank computation.
//
// Every MEMDB_PAGERANK_INTERVAL (default 6h) this goroutine reads all active
// memory_edges for each cube, computes a weighted PageRank, and persists the
// score as Memory.properties->>'pagerank' via a single bulk UPDATE per cube.
//
// Algorithm: iterative power method, 30 iterations, damping d=0.85, uniform
// prior (no external gonum dependency). Converges for any strongly-connected
// component. For disconnected graphs each weakly-connected component settles
// independently — scores remain comparable because they are normalized inside
// each cube.
//
// Env gates:
//   MEMDB_PAGERANK_ENABLED  — default "true"; set "false" to disable entirely.
//   MEMDB_PAGERANK_INTERVAL — Go duration string, default 6h.
//   MEMDB_PAGERANK_BOOST_WEIGHT — float in [0, 1], default 0.1 (D1 multiplier weight).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// errComputePageRank is returned by runPageRankForCube when computePageRank
// produces no scores despite a non-empty edge list (e.g. all edges had
// invalid node IDs or a degenerate graph). Callers use errors.Is to
// distinguish this from a database error.
var errComputePageRank = errors.New("pagerank compute produced no scores")

const (
	defaultPageRankInterval    = 6 * time.Hour
	defaultPageRankBoostWeight = 0.1
	pageRankDamping            = 0.85
	pageRankIterations         = 30
)

// pageRankEnabled reports whether the PageRank background task is active.
// Read on every call so tests can flip the env var without rebuilding.
func pageRankEnabled() bool {
	v := os.Getenv("MEMDB_PAGERANK_ENABLED")
	if v == "" || v == "true" {
		return true
	}
	return false
}

// pageRankInterval returns the tick period for the PageRank goroutine.
// Env: MEMDB_PAGERANK_INTERVAL (Go duration string). Falls back to 6h on parse error.
func pageRankInterval() time.Duration {
	raw := os.Getenv("MEMDB_PAGERANK_INTERVAL")
	if raw == "" {
		return defaultPageRankInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultPageRankInterval
	}
	return d
}

// PageRankBoostWeight returns the multiplicative boost weight applied in D1
// rerank: final_score *= (1 + pagerank * weight).
// Env: MEMDB_PAGERANK_BOOST_WEIGHT in [0, 1]. Default 0.1.
func PageRankBoostWeight() float64 {
	raw := os.Getenv("MEMDB_PAGERANK_BOOST_WEIGHT")
	if raw == "" {
		return defaultPageRankBoostWeight
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		return defaultPageRankBoostWeight
	}
	return v
}

// runPageRankLoop is started as a background goroutine by Worker.Run when
// pageRankEnabled() is true and a Postgres client is available.
// It staggered by half the interval on startup (same pattern as periodicReorgLoop).
func (w *Worker) runPageRankLoop(ctx context.Context, pg *db.Postgres) {
	if pg == nil {
		return
	}
	interval := pageRankInterval()
	// Stagger first run so it doesn't overlap with periodicReorgLoop startup.
	select {
	case <-ctx.Done():
		return
	case <-time.After(interval / 3):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		w.runPageRankForAllCubes(ctx, pg)

		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
		}
	}
}

// runPageRankForAllCubes discovers active cubes (reusing the Worker's existing
// scanVSetCubeIDs / scanStreamCubeIDs methods) and runs PageRank for each.
//
// HA gate: in multi-replica deployments only one replica executes the PageRank
// pass per cycle. The session advisory lock is acquired non-blocking; replicas
// that do not win skip with outcome=skipped_other_leader. The lock is released
// at function exit (or auto-released on session close if the pod crashes).
func (w *Worker) runPageRankForAllCubes(ctx context.Context, pg *db.Postgres) {
	mx := schedMx()
	start := time.Now()

	locked, err := tryAcquirePagerankAdvisoryLock(ctx, pg)
	if err != nil {
		mx.PageRankRuns.Add(ctx, 1, labelPageRankOutcome("db_error"))
		w.logger.Debug("pagerank: advisory lock acquire failed", "err", err)
		return
	}
	if !locked {
		mx.PageRankRuns.Add(ctx, 1, labelPageRankOutcome("skipped_other_leader"))
		w.logger.Debug("pagerank: another replica holds the lock, skipping")
		return
	}
	defer func() { _ = releasePagerankAdvisoryLock(ctx, pg) }()

	cubes := w.getActiveCubes(ctx)
	if len(cubes) == 0 {
		w.logger.Debug("pagerank: no active cubes found")
		mx.PageRankRuns.Add(ctx, 1, labelPageRankOutcome("empty"))
		mx.PageRankLastRun.Record(ctx, time.Since(start).Seconds())
		return
	}

	success := 0
	for _, cubeID := range cubes {
		if ctx.Err() != nil {
			return
		}
		if err := w.runPageRankForCube(ctx, pg, cubeID); err != nil {
			w.logger.Warn("pagerank: cube failed",
				slog.String("cube_id", cubeID),
				slog.Any("error", err),
			)
			outcome := "db_error"
			if errors.Is(err, errComputePageRank) {
				outcome = "compute_error"
			}
			mx.PageRankRuns.Add(ctx, 1, labelPageRankOutcome(outcome))
		} else {
			success++
		}
	}
	mx.PageRankRuns.Add(ctx, int64(success), labelPageRankOutcome("success"))
	mx.PageRankLastRun.Record(ctx, time.Since(start).Seconds())

	w.logger.Info("pagerank: cycle complete",
		slog.Int("cubes_total", len(cubes)),
		slog.Int("cubes_ok", success),
		slog.Duration("elapsed", time.Since(start)),
	)
}

// runPageRankForCube fetches all valid edges for a cube, computes PageRank,
// and persists the scores as Memory.properties->>'pagerank'.
func (w *Worker) runPageRankForCube(ctx context.Context, pg *db.Postgres, cubeID string) error {
	edges, err := pg.FetchEdgesForPageRank(ctx, cubeID)
	if err != nil {
		return fmt.Errorf("fetch edges: %w", err)
	}
	if len(edges) == 0 {
		return nil // no edges → nothing to rank
	}

	scores := computePageRank(edges)
	if len(scores) == 0 {
		// Non-empty edge list produced no scores — degenerate graph (e.g. all
		// edges had empty node IDs). This is a math/input failure, not a DB
		// failure; emit compute_error so PromQL alerts fire.
		return fmt.Errorf("%w: cube=%s edges=%d", errComputePageRank, cubeID, len(edges))
	}

	if err := pg.BulkSetPageRank(ctx, cubeID, scores); err != nil {
		return fmt.Errorf("bulk set pagerank: %w", err)
	}
	return nil
}

// computePageRank runs the iterative power-method PageRank on the given edges.
// edges is a slice of (fromID, toID, weight) triples. Weights are used as
// outgoing-edge weights; if all weights are zero, uniform distribution is used.
//
// Returns a map from node ID → PageRank score (sum ≈ 1.0 for all nodes).
func computePageRank(edges []db.PageRankEdge) map[string]float64 {
	// Build adjacency: outEdges[u] → [(v, weight)]
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
		return nil
	}

	// Index nodes for O(1) lookup.
	nodes := make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	idx := make(map[string]int, len(nodes))
	for i, n := range nodes {
		idx[n] = i
	}
	n := len(nodes)

	// Compute normalized out-weights per node.
	// outWeight[u][v] = w(u→v) / sum_w(u)
	type edge struct{ dst, src int; w float64 }
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

	// dangling: nodes with no outgoing edges (sinks).
	hasSink := make([]bool, n)
	for i, nd := range nodes {
		if len(outEdges[nd]) == 0 {
			hasSink[i] = true
		}
	}

	// Power iteration.
	rank := make([]float64, n)
	newRank := make([]float64, n)
	for i := range rank {
		rank[i] = 1.0 / float64(n)
	}

	d := pageRankDamping
	teleport := (1.0 - d) / float64(n)

	for iter := 0; iter < pageRankIterations; iter++ {
		// Collect dangling sum.
		var danglingSum float64
		for i, sink := range hasSink {
			if sink {
				danglingSum += rank[i]
			}
		}
		danglingContrib := d * danglingSum / float64(n)

		for i := range newRank {
			newRank[i] = teleport + danglingContrib
		}
		for _, e := range edgeList {
			newRank[e.dst] += d * rank[e.src] * e.w
		}
		copy(rank, newRank)
	}

	result := make(map[string]float64, n)
	for i, nd := range nodes {
		result[nd] = rank[i]
	}
	return result
}
