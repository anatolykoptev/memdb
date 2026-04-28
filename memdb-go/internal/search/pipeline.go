// Package search — pipeline.go: stage-based orchestrator that replaces
// the legacy 14-step hardcoded Search method.
//
// Goals:
//   - One mutable *pipelineState carries everything between stages — no more
//     ad-hoc local variables threaded through 157 LOC of orchestration.
//   - Each stage gets per-stage timing + a memdb.search.stage_* metric without
//     the orchestrator having to remember to record it.
//   - Adding a new stage (F1 VEC_COT, F2 reflection-loop, F6) is a one-line
//     append to the slice — no more editing the master Search method.
//
// Soft-error contract: a single stage's error does NOT abort the pipeline.
// It is logged, recorded as outcome=error on the stage_total counter, and
// appended to state.Errors. The pipeline always returns nil — Search()
// translates an empty result into the existing error path explicitly when
// needed (only embed_query is fatal — the rest gracefully degrade).
package search

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// stage is one step of the search pipeline.
//
// Implementations must:
//   - Be re-entrant (called once per Search()).
//   - Mutate *pipelineState in place — no return value besides error.
//   - Return a soft-fail error to skip this stage's effect; the pipeline
//     records outcome=error and continues. Returning nil = success.
//   - Use state.skip("name") to mark themselves as skipped (outcome=skipped).
type stage interface {
	Name() string
	Run(ctx context.Context, s *pipelineState) error
}

// funcStage adapts a free function (or method value) into a stage. Used
// for one-off stages whose body is a verbatim lift from the legacy Search.
type funcStage struct {
	name string
	run  func(ctx context.Context, s *pipelineState) error
}

func (f funcStage) Name() string { return f.name }
func (f funcStage) Run(ctx context.Context, s *pipelineState) error {
	return f.run(ctx, s)
}

// pipelineState carries everything between stages. Fields are populated as
// stages execute — earlier stages may leave fields nil/empty if they
// soft-failed; later stages must tolerate that (mirrors current Search
// graceful-degrade behavior).
type pipelineState struct {
	// Input — set by Search() before runPipeline.
	Params SearchParams
	// EmbedQuery is the (possibly D4-rewritten) query string passed to the
	// embedder. p.Query stays untouched and is used by BM25 / CE / LLM
	// rerank / D10 enhance for user-intent fidelity.
	EmbedQuery string
	// Subqueries is the D7 CoT split. Always [original, ...subqueries].
	// len==1 ⇒ atomic, no augmentation.
	Subqueries []string
	// QueryVec is the embedding of EmbedQuery; nil only when embed_query
	// failed (which is fatal — Search returns the embed error).
	QueryVec []float32
	Tokens   []string
	TSQuery  string
	// CutoffISO and HasCutoff come from temporal cutoff detection.
	CutoffISO string
	HasCutoff bool
	Budget    searchBudget

	// Mid-pipeline data.
	PSR             *parallelSearchResults
	BFSResults      []db.GraphRecallResult
	InternetMerged  []MergedResult
	D11Subqueries   []string
	TextMerged      []MergedResult
	SkillMerged     []MergedResult
	ToolMerged      []MergedResult
	TextFormatted   []map[string]any
	SkillFormatted  []map[string]any
	ToolFormatted   []map[string]any
	PrefFormatted   []map[string]any
	TextEmbByID     map[string][]float32
	SkillEmbByID    map[string][]float32
	ToolEmbByID     map[string][]float32
	ActMemFormatted []map[string]any

	// Output assembled by build_response stage.
	Result *SearchResult

	// Bookkeeping.
	Timings  map[string]time.Duration
	Errors   []error
	skipped  map[string]struct{}
	embedErr error // fatal — Search() unwraps this after runPipeline
}

// skip marks a stage as skipped so the pipeline records outcome=skipped
// instead of success. Stages call this when they detect their preconditions
// are not met (e.g. CoT decomposer disabled).
func (s *pipelineState) skip(name string) {
	if s.skipped == nil {
		s.skipped = map[string]struct{}{}
	}
	s.skipped[name] = struct{}{}
}

// runPipeline executes stages serially, recording per-stage timing and
// emitting the memdb.search.stage_duration_ms histogram + stage_total
// counter. Soft-fail per stage (see package doc).
func runPipeline(ctx context.Context, logger *slog.Logger, stages []stage, s *pipelineState) {
	mx := pipelineMx()
	if s.Timings == nil {
		s.Timings = make(map[string]time.Duration, len(stages))
	}
	for _, st := range stages {
		name := st.Name()
		t0 := time.Now()
		err := st.Run(ctx, s)
		dur := time.Since(t0)
		s.Timings[name] = dur

		outcome := "success"
		switch {
		case err != nil:
			outcome = "error"
			s.Errors = append(s.Errors, fmt.Errorf("%s: %w", name, err))
			if logger != nil {
				logger.Warn("search pipeline stage error",
					slog.String("stage", name),
					slog.Any("error", err),
				)
			}
		default:
			if _, ok := s.skipped[name]; ok {
				outcome = "skipped"
			}
		}

		stageAttrs := metric.WithAttributes(attribute.String("stage", name))
		mx.Duration.Record(ctx, dur.Milliseconds(), stageAttrs)
		mx.Total.Add(ctx, 1, metric.WithAttributes(
			attribute.String("stage", name),
			attribute.String("outcome", outcome),
		))
	}
}

// pipelineMetrics holds the per-stage telemetry instruments. Lazily
// initialised + pre-registered at zero for every stage in stageNames so
// dashboards see all series from container start.
type pipelineMetrics struct {
	Duration metric.Int64Histogram
	Total    metric.Int64Counter
}

var (
	pipelineMxOnce sync.Once
	pipelineMxInst *pipelineMetrics
)

// stageNames is the canonical list of pipeline stage labels — used for
// pre-registration. Must stay in sync with defaultStages() in
// pipeline_stages.go.
var stageNames = []string{
	"d7_cot_decompose",
	"d4_query_rewrite",
	"embed_query",
	"tokenize_temporal_cutoff",
	"parallel_db_fanout",
	"d7_cot_augment",
	"d11_cot_decompose",
	"bfs_expand",
	"internet_embed",
	"merge_candidates",
	"temporal_augment",
	"inject_events",
	"linked_expand",
	"d2_graph_expand",
	"contradicts_penalty",
	"format_items",
	"post_process",
	"reflect",
	"working_mem_format",
	"build_response",
	"profile_inject",
	"retrieval_count_async",
}

func pipelineMx() *pipelineMetrics {
	pipelineMxOnce.Do(func() {
		m := otel.Meter("memdb-go/search")
		dur, _ := m.Int64Histogram("memdb.search.stage_duration_ms",
			metric.WithDescription("Per-stage duration of the search pipeline (label: stage)"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000))
		tot, _ := m.Int64Counter("memdb.search.stage_total",
			metric.WithDescription("Per-stage execution count of the search pipeline (labels: stage, outcome=success|error|skipped)"))
		pipelineMxInst = &pipelineMetrics{Duration: dur, Total: tot}

		// Pre-register every stage at zero for all three outcomes.
		ctx := context.Background()
		for _, n := range stageNames {
			dur.Record(ctx, 0, metric.WithAttributes(attribute.String("stage", n)))
			for _, oc := range []string{"success", "error", "skipped"} {
				tot.Add(ctx, 0, metric.WithAttributes(
					attribute.String("stage", n),
					attribute.String("outcome", oc),
				))
			}
		}
	})
	return pipelineMxInst
}
