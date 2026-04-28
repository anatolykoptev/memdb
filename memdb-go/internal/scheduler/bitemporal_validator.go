package scheduler

// bitemporal_validator.go — M11 F11 background re-validation loop.
//
// The synchronous /add path runs the LLM judge over the just-inserted edges
// (handlers/add_fine_bitemporal.go). That covers contradictions visible at
// write-time. This loop covers the orthogonal case: an edge written months
// ago whose contradiction only becomes obvious after a later, unrelated /add.
//
// On each tick we sample stale-but-valid entity edges across active cubes
// and re-run the same judge against the cube's currently-valid peers. Edges
// whose subject+predicate has been touched recently (within the staleness
// window) are skipped — the sync path already covered them.
//
// Wiring: Start*Loop is invoked from Worker.Run when w.pg, w.reorg, and a
// shared LLM client are all available. Disabled by default; enabled by
// MEMDB_F11_VALIDATOR_ENABLED=true.
//
// Cost shape: per tick we judge at most validatorBatchPerCube edges per cube,
// one LLM call per edge group. The interval (default 30 min) and budget cap
// keep total LLM cost predictable.
//
// Metrics:
//   - memdb.scheduler.loop_iterations_total{name=bitemporal_validator,outcome=...}
//     (auto-emitted by periodicLoop).
//   - memdb.scheduler.bitemporal_validator_edges_total{outcome=invalidated|skip|llm_error|no_peers}

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	bitemporalValidatorLoopName = "bitemporal_validator"
	bitemporalValidatorEnvGate  = "MEMDB_F11_VALIDATOR_ENABLED"

	// defaultValidatorInterval is how often the loop wakes. 30 min keeps
	// total LLM cost low while still catching long-tail contradictions.
	defaultValidatorInterval = 30 * time.Minute

	// defaultValidatorStaleness is the minimum age an edge must reach
	// before the validator will re-judge it. ~6 months — older edges are
	// the prime suspects for stale state ("still lives in Paris" after
	// the user moved to Berlin).
	defaultValidatorStaleness = 6 * 30 * 24 * time.Hour

	// validatorBatchPerCube caps how many stale edges a single tick judges
	// per active cube. Real /add traffic hits the sync judge first; this
	// loop is a long-tail mop-up so a small per-tick budget is enough.
	validatorBatchPerCube = 16

	// validatorPerEdgeTimeout caps wall time per LLM judge call.
	validatorPerEdgeTimeout = 25 * time.Second
)

// validatorEnabled reports whether the F11 validator loop should start.
// Default OFF — opt-in. Production rollout flips this only after the sync
// path's invalidation rate stabilises.
func validatorEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(bitemporalValidatorEnvGate)))
	switch v {
	case "true", "1":
		return true
	default:
		return false
	}
}

// validatorInterval reads MEMDB_F11_VALIDATOR_INTERVAL_MIN (minutes) with
// a sane default. Bounded at >= 5 min to prevent runaway LLM cost from a
// fat-fingered env override.
func validatorInterval() time.Duration {
	if raw := os.Getenv("MEMDB_F11_VALIDATOR_INTERVAL_MIN"); raw != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 5 {
			return time.Duration(n) * time.Minute
		}
	}
	return defaultValidatorInterval
}

// validatorStaleness reads MEMDB_F11_VALIDATOR_STALENESS_DAYS with a sane
// default. Bounded at >= 1 day to keep the loop from re-judging fresh edges
// the sync path just covered (which would waste tokens).
func validatorStaleness() time.Duration {
	if raw := os.Getenv("MEMDB_F11_VALIDATOR_STALENESS_DAYS"); raw != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 1 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	return defaultValidatorStaleness
}

// --- metrics ---

const (
	validatorOutcomeInvalidated = "invalidated"
	validatorOutcomeSkip        = "skip"
	validatorOutcomeLLMError    = "llm_error"
	validatorOutcomeDBError     = "db_error" // Postgres fetch/update failure
	validatorOutcomeNoPeers     = "no_peers"
)

var (
	validatorMxOnce sync.Once
	validatorMx     struct {
		Edges metric.Int64Counter
	}
)

func validatorMetrics() {
	validatorMxOnce.Do(func() {
		meter := otel.Meter("memdb-go/scheduler")
		validatorMx.Edges, _ = meter.Int64Counter(
			"memdb.scheduler.bitemporal_validator_edges_total",
			metric.WithDescription("F11 background validator per-edge outcomes (invalidated|skip|llm_error|db_error|no_peers)"),
		)
		// Pre-register at zero so the series exist before the first tick.
		ctx := context.Background()
		for _, oc := range []string{
			validatorOutcomeInvalidated,
			validatorOutcomeSkip,
			validatorOutcomeLLMError,
			validatorOutcomeDBError,
			validatorOutcomeNoPeers,
		} {
			validatorMx.Edges.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
	})
}

func recordValidatorEdgeOutcome(ctx context.Context, outcome string, n int64) {
	if n <= 0 {
		return
	}
	validatorMetrics()
	if validatorMx.Edges != nil {
		validatorMx.Edges.Add(ctx, n, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
}

// --- loop wiring ---

// startBitemporalValidatorLoop wires the F11 validator into periodicLoop.
// Caller is responsible for the env gate check + dependency presence.
func (w *Worker) startBitemporalValidatorLoop(ctx context.Context) {
	interval := validatorInterval()
	(&periodicLoop{
		name:     bitemporalValidatorLoopName,
		interval: interval,
		// Stagger after pagerank/periodic_reorg so the three loops don't
		// punch the LLM proxy in lockstep on cold start.
		stagger: 60 * time.Second,
		runOnce: func(ctx context.Context) error {
			return w.runBitemporalValidatorOnce(ctx)
		},
	}).Start(ctx, w.stopCh)
}

// runBitemporalValidatorOnce is the per-tick body. Iterates active cubes,
// fetches stale-but-valid edges per cube, and re-judges each one.
func (w *Worker) runBitemporalValidatorOnce(ctx context.Context) error {
	if w.pg == nil || w.llmJudge == nil {
		// Defensive: Worker.Run already gates this loop on these deps,
		// but a SetPostgres(nil) at runtime would land us here.
		return nil
	}
	cubes := w.getActiveCubes(ctx)
	if len(cubes) == 0 {
		return nil
	}

	cutoff := time.Now().Add(-validatorStaleness()).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)

	for _, cubeID := range cubes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.judgeStaleEdgesForCube(ctx, cubeID, cutoff, now); err != nil {
			w.logger.Warn("bitemporal_validator: cube failed",
				slog.String("cube_id", cubeID), slog.Any("error", err))
			// Continue — one cube's failure shouldn't abort the cycle.
		}
	}
	return nil
}

// judgeStaleEdgesForCube fetches stale entity edges for one cube and runs
// the LLM judge on each. Bounded by validatorBatchPerCube.
func (w *Worker) judgeStaleEdgesForCube(ctx context.Context, cubeID, cutoff, now string) error {
	stale, err := w.pg.FetchStaleEntityEdges(ctx, cutoff, cubeID, validatorBatchPerCube)
	if err != nil {
		return fmt.Errorf("fetch stale edges: %w", err)
	}
	if len(stale) == 0 {
		return nil
	}
	for _, e := range stale {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.judgeOneStaleEdge(ctx, cubeID, now, e)
	}
	return nil
}

// judgeOneStaleEdge re-runs the F11 judge against a single old edge,
// using the cube's currently-valid peers as the candidate set.
func (w *Worker) judgeOneStaleEdge(ctx context.Context, cubeID, now string, edge db.BiTemporalEdgeRef) {
	callCtx, cancel := context.WithTimeout(ctx, validatorPerEdgeTimeout)
	defer cancel()

	peers, err := w.pg.FetchActiveEntityEdgesBySubject(callCtx, cubeID, edge.FromID, edge.Predicate, 8)
	if err != nil {
		recordValidatorEdgeOutcome(ctx, validatorOutcomeDBError, 1)
		return
	}
	if len(peers) <= 1 {
		// Only the edge itself is alive — nothing to invalidate.
		recordValidatorEdgeOutcome(ctx, validatorOutcomeNoPeers, 1)
		return
	}

	// The "new fact" representative is the most-recent peer (peers are
	// returned DESC by created_at). The candidate set excludes it.
	newPeer := peers[0]
	candidates := peers[1:]

	refs := make([]llm.FactRef, len(candidates))
	for i, c := range candidates {
		refs[i] = llm.FactRef{
			ID:   edge.Predicate + "|" + c.ToID,
			Text: c.Predicate + " " + c.ToID,
		}
	}
	newFact := newPeer.Predicate + " " + newPeer.ToID

	decision, err := llm.DecideInvalidation(callCtx, w.llmJudge, newFact, edge.FromID, refs)
	if err != nil {
		recordValidatorEdgeOutcome(ctx, validatorOutcomeLLMError, 1)
		return
	}
	if decision.Confidence < llm.EdgeInvalidationConfidenceThreshold || len(decision.Invalidate) == 0 {
		recordValidatorEdgeOutcome(ctx, validatorOutcomeSkip, 1)
		return
	}

	// Rebuild the doomed triples from the decision IDs.
	keyToTriple := make(map[string]db.BiTemporalEdgeRef, len(candidates))
	for _, c := range candidates {
		keyToTriple[edge.Predicate+"|"+c.ToID] = c
	}
	doomed := make([]db.BiTemporalEdgeRef, 0, len(decision.Invalidate))
	for _, id := range decision.Invalidate {
		if t, ok := keyToTriple[id]; ok {
			doomed = append(doomed, t)
		}
	}
	if len(doomed) == 0 {
		recordValidatorEdgeOutcome(ctx, validatorOutcomeSkip, 1)
		return
	}
	n, err := w.pg.InvalidateEntityEdgesByTriples(ctx, cubeID, doomed, now)
	if err != nil {
		recordValidatorEdgeOutcome(ctx, validatorOutcomeDBError, 1)
		w.logger.Warn("bitemporal_validator: invalidate failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
		return
	}
	recordValidatorEdgeOutcome(ctx, validatorOutcomeInvalidated, n)
}
