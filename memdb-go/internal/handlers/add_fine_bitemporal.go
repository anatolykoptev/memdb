package handlers

// add_fine_bitemporal.go — M11 F11 fire-and-forget bi-temporal edge
// invalidation hook. Wired from triggerBackgroundExtractors so it inherits
// the standard fan-out pattern: bounded concurrency, env gate, no influence
// on the sync write path.
//
// Flow per /add:
//  1. Load entity edges this cube wrote with created_at = `now` (the just-
//     inserted batch). Capped at edgeJudgeBatchCap to bound LLM cost.
//  2. Group by (from_entity_id, predicate) — these are the (subject,predicate)
//     keys where supersession can happen.
//  3. For each group with ≥ 2 distinct objects (or 1 new + ≥ 1 prior peer),
//     fetch the active peer set, ask llm.DecideInvalidation, and on
//     confidence ≥ EdgeInvalidationConfidenceThreshold flip invalid_at on
//     the losing peers (the new edge is always treated as authoritative).
//
// Env gate: MEMDB_F11_EDGE_JUDGE (default "true"; "false"/"0" disable).
// Concurrency cap: edgeJudgeSemaphoreSize (4) — half of the profile-extract
// budget because each /add can fan out to multiple judges.
//
// Cost shape (default-on, p95 estimate):
//   - 1 LLM judge call per (subject,predicate) group with ≥1 prior peer.
//   - Realistic /add fan-out: 0–2 groups per request (most /adds add 0–1
//     fresh entity edges that have a prior peer).
//   - At ~$0.0001 per cached-prompt judge call (gemini-2.5-flash-lite via
//     CLIProxyAPI), incremental cost per /add stays under $0.0002 p95.
//   - Wallclock cost is OFF the request path (fire-and-forget goroutine).
//
// Group fan-out vs per-pair fan-out: the judge ALREADY batches up to
// edgeJudgePeerCap (8) peers into a single LLM call per (subject,predicate)
// group via DecideInvalidation's `refs` parameter. We do not batch ACROSS
// groups in one call because the prompt is keyed on a single (newFact,
// subject) pair — combining groups would require a second prompt template
// and lose the prompt-cache hits the per-group call enjoys.
//
// Metrics:
//   - memdb.add.edges_invalidated_total{table=entity}
//   - memdb.add.edge_invalidation_confidence histogram
//   - memdb.add.edge_judge_total{outcome=success|skip|llm_error|db_error|busy|disabled}
//
// On any error the goroutine logs at Debug. The sync write path is
// untouched: a failed judge just leaves the prior edges valid (status quo).

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/semaphore"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	edgeJudgeEnvVar         = "MEMDB_F11_EDGE_JUDGE"
	edgeJudgeSemaphoreSize  = 4
	edgeJudgeOverallTimeout = 90 * time.Second
	// edgeJudgeBatchCap caps how many fresh edges a single /add can fan out to.
	// Bigger /adds get truncated — the validator loop will pick up the rest.
	edgeJudgeBatchCap = 16
	// edgeJudgePeerCap caps how many peer rows we feed the LLM per group.
	edgeJudgePeerCap = 8
)

func edgeJudgeEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(edgeJudgeEnvVar)))
	switch v {
	case "false", "0":
		return false
	default:
		return true
	}
}

// --- bounded concurrency ---

var (
	edgeJudgeSemOnce sync.Once
	edgeJudgeSem     *semaphore.Weighted
)

func edgeJudgeSemaphore() *semaphore.Weighted {
	edgeJudgeSemOnce.Do(func() {
		edgeJudgeSem = semaphore.NewWeighted(edgeJudgeSemaphoreSize)
	})
	return edgeJudgeSem
}

// --- metrics ---

const (
	edgeJudgeOutcomeSuccess  = "success"
	edgeJudgeOutcomeSkip     = "skip" // judge ran but confidence below threshold
	edgeJudgeOutcomeLLMError = "llm_error"
	edgeJudgeOutcomeDBError  = "db_error" // Postgres fetch/update failure
	edgeJudgeOutcomeBusy     = "busy"
	edgeJudgeOutcomeDisabled = "disabled"
	edgeJudgeOutcomeNoPeers  = "no_peers" // nothing to judge against — common
)

var (
	edgeJudgeMxOnce sync.Once
	edgeJudgeMx     struct {
		Invalidated metric.Int64Counter   // table label: memory|entity
		Confidence  metric.Float64Histogram
		JudgeTotal  metric.Int64Counter   // outcome label
	}
)

// preregisteredJudgeOutcomes / preregisteredJudgeTables drive the zero-add
// pre-registration so Prometheus sees every (outcome) and (table) series at
// container start — same pattern as llm/metrics.go and search/metrics.go.
var (
	preregisteredJudgeOutcomes = []string{
		edgeJudgeOutcomeSuccess,
		edgeJudgeOutcomeSkip,
		edgeJudgeOutcomeLLMError,
		edgeJudgeOutcomeDBError,
		edgeJudgeOutcomeBusy,
		edgeJudgeOutcomeDisabled,
		edgeJudgeOutcomeNoPeers,
	}
	preregisteredJudgeTables = []string{"entity"}
)

func edgeJudgeMetrics() {
	edgeJudgeMxOnce.Do(func() {
		meter := otel.Meter("memdb-go/handlers")
		edgeJudgeMx.Invalidated, _ = meter.Int64Counter(
			"memdb.add.edges_invalidated_total",
			metric.WithDescription("F11 bi-temporal edges invalidated by the LLM judge, by table"),
		)
		edgeJudgeMx.Confidence, _ = meter.Float64Histogram(
			"memdb.add.edge_invalidation_confidence",
			metric.WithDescription("F11 LLM judge confidence distribution per decision"),
			metric.WithExplicitBucketBoundaries(0.1, 0.3, 0.5, 0.7, 0.85, 0.95, 1.0),
		)
		edgeJudgeMx.JudgeTotal, _ = meter.Int64Counter(
			"memdb.add.edge_judge_total",
			metric.WithDescription("F11 LLM judge invocations by outcome (success|skip|llm_error|db_error|busy|disabled|no_peers)"),
		)

		// Pre-register at zero for both label sets.
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, oc := range preregisteredJudgeOutcomes {
			edgeJudgeMx.JudgeTotal.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		for _, tbl := range preregisteredJudgeTables {
			edgeJudgeMx.Invalidated.Add(ctx, 0, metric.WithAttributes(attribute.String("table", tbl)))
		}
	})
}

func recordJudgeOutcome(ctx context.Context, outcome string) {
	edgeJudgeMetrics()
	if edgeJudgeMx.JudgeTotal != nil {
		edgeJudgeMx.JudgeTotal.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
}

func recordEdgesInvalidated(ctx context.Context, table string, n int64) {
	if n <= 0 {
		return
	}
	edgeJudgeMetrics()
	if edgeJudgeMx.Invalidated != nil {
		edgeJudgeMx.Invalidated.Add(ctx, n, metric.WithAttributes(
			attribute.String("table", table),
		))
	}
}

func recordJudgeConfidence(ctx context.Context, conf float64) {
	edgeJudgeMetrics()
	if edgeJudgeMx.Confidence != nil {
		edgeJudgeMx.Confidence.Record(ctx, conf)
	}
}

// --- DB interface (allows test injection without a real pgx pool) ---

// edgeJudgePG is the minimal Postgres surface needed by the F11 judge.
// *db.Postgres satisfies it; tests can provide a stub.
type edgeJudgePG interface {
	FetchFreshEntityEdgesForCube(ctx context.Context, userName, now string, limit int) ([]db.BiTemporalEdgeRef, error)
	FetchActiveEntityEdgesBySubject(ctx context.Context, userName, subjectID, predicate string, limit int) ([]db.BiTemporalEdgeRef, error)
	InvalidateEntityEdgesByTriples(ctx context.Context, userName string, triples []db.BiTemporalEdgeRef, invalidAt string) (int64, error)
}

// --- entry point ---

// triggerEdgeInvalidationJudge launches a fire-and-forget bi-temporal
// invalidation pass for entity edges this cube just wrote. Returns true
// when a goroutine was scheduled.
//
// Required deps: postgres + llmChat. Env gate: MEMDB_F11_EDGE_JUDGE.
// Admission control matches triggerProfileExtract: TryAcquire BEFORE spawn.
//
// `now` is the per-/add timestamp passed through extractorTriggerInput. We
// use it to filter entity_edges to the just-inserted batch (created_at = now).
func (h *Handler) triggerEdgeInvalidationJudge(reqCtx context.Context, cubeID, now string) bool {
	if h == nil || h.postgres == nil || h.llmChat == nil {
		return false
	}
	if !edgeJudgeEnabled() {
		recordJudgeOutcome(reqCtx, edgeJudgeOutcomeDisabled)
		return false
	}
	if cubeID == "" || now == "" {
		return false
	}
	sem := edgeJudgeSemaphore()
	if !sem.TryAcquire(1) {
		recordJudgeOutcome(reqCtx, edgeJudgeOutcomeBusy)
		h.logger.Debug("edge judge: semaphore saturated, dropping",
			slog.String("cube_id", cubeID))
		return false
	}
	bgCtx := context.WithoutCancel(reqCtx)
	go func() {
		defer sem.Release(1)
		h.runEdgeInvalidationJudge(bgCtx, h.postgres, cubeID, now)
	}()
	return true
}

// runEdgeInvalidationJudge is the goroutine body. Loads fresh entity edges
// for the cube, groups by (subject,predicate), and dispatches one LLM judge
// per group with active peers.
// bgCtx must be a context.WithoutCancel-wrapped request context so the OTel
// trace propagates into pgxotel spans and slogh log lines.
func (h *Handler) runEdgeInvalidationJudge(bgCtx context.Context, pg edgeJudgePG, cubeID, now string) {
	ctx, cancel := context.WithTimeout(bgCtx, edgeJudgeOverallTimeout)
	defer cancel()

	fresh, err := pg.FetchFreshEntityEdgesForCube(ctx, cubeID, now, edgeJudgeBatchCap)
	if err != nil {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeDBError)
		h.logger.Debug("edge judge: fetch fresh edges failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
		return
	}
	if len(fresh) == 0 {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeNoPeers)
		return
	}

	// Group fresh edges by (subject, predicate) — that's the supersession key.
	type groupKey struct{ subject, predicate string }
	groups := make(map[groupKey][]db.BiTemporalEdgeRef, len(fresh))
	for _, f := range fresh {
		k := groupKey{subject: f.FromID, predicate: f.Predicate}
		groups[k] = append(groups[k], f)
	}

	for k, freshGroup := range groups {
		if ctx.Err() != nil {
			return
		}
		h.judgeOneEntityGroup(ctx, pg, cubeID, now, k.subject, k.predicate, freshGroup)
	}
}

// judgeOneEntityGroup runs the LLM judge for a single (subject,predicate)
// group on entity_edges. Multiple fresh edges in the same group are treated
// as competing candidates; we use the first as the "new fact" representative
// and feed the rest as additional peers. (Realistic per-/add fan-out is 1
// fresh edge per group anyway.)
func (h *Handler) judgeOneEntityGroup(
	ctx context.Context,
	pg edgeJudgePG,
	cubeID, now, subject, predicate string,
	freshGroup []db.BiTemporalEdgeRef,
) {
	if len(freshGroup) == 0 {
		return
	}
	// Pull the wider set of currently-valid peers for the same key.
	peers, err := pg.FetchActiveEntityEdgesBySubject(ctx, cubeID, subject, predicate, edgeJudgePeerCap)
	if err != nil {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeDBError)
		h.logger.Debug("edge judge: fetch peers failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
		return
	}

	// Exclude the freshly-inserted rows from the candidate set — we judge
	// against PRIOR facts, never against the new one (which is authoritative).
	freshIdx := make(map[string]struct{}, len(freshGroup))
	for _, f := range freshGroup {
		freshIdx[edgeKey(f)] = struct{}{}
	}
	candidates := peers[:0]
	for _, p := range peers {
		if _, isFresh := freshIdx[edgeKey(p)]; isFresh {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeNoPeers)
		return
	}

	// Build LLM-side FactRef inputs. Each ID is an opaque key derived from
	// the triple — we map back to the same triple set on the way out.
	refs := make([]llm.FactRef, len(candidates))
	for i, c := range candidates {
		refs[i] = llm.FactRef{ID: edgeKey(c), Text: predicate + " " + c.ToID}
	}
	newFact := predicate + " " + freshGroup[0].ToID

	decision, err := llm.DecideInvalidation(ctx, h.llmChat, newFact, subject, refs)
	if err != nil {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeLLMError)
		h.logger.Debug("edge judge: LLM call failed",
			slog.String("cube_id", cubeID),
			slog.String("subject", subject),
			slog.String("predicate", predicate),
			slog.Any("error", err))
		return
	}
	recordJudgeConfidence(ctx, decision.Confidence)

	if decision.Confidence < llm.EdgeInvalidationConfidenceThreshold || len(decision.Invalidate) == 0 {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeSkip)
		return
	}

	// Map back: build the triple set the UPDATE will hit.
	keyToTriple := make(map[string]db.BiTemporalEdgeRef, len(candidates))
	for _, c := range candidates {
		keyToTriple[edgeKey(c)] = c
	}
	doomed := make([]db.BiTemporalEdgeRef, 0, len(decision.Invalidate))
	for _, id := range decision.Invalidate {
		if t, ok := keyToTriple[id]; ok {
			doomed = append(doomed, t)
		}
	}
	if len(doomed) == 0 {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeSkip)
		return
	}

	n, err := pg.InvalidateEntityEdgesByTriples(ctx, cubeID, doomed, now)
	if err != nil {
		recordJudgeOutcome(ctx, edgeJudgeOutcomeDBError)
		h.logger.Debug("edge judge: UPDATE failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
		return
	}
	recordEdgesInvalidated(ctx, "entity", n)
	recordJudgeOutcome(ctx, edgeJudgeOutcomeSuccess)
	h.logger.Debug("edge judge: invalidated entity edges",
		slog.String("cube_id", cubeID),
		slog.String("subject", subject),
		slog.String("predicate", predicate),
		slog.Int64("rows", n),
		slog.Float64("confidence", decision.Confidence))
}

// edgeKey forms a compact deterministic key for an edge triple. Used both
// to deduplicate the fresh set against the peer set and as the opaque ID
// fed to the LLM.
func edgeKey(e db.BiTemporalEdgeRef) string {
	return e.FromID + "|" + e.Predicate + "|" + e.ToID
}
