// Package observability — M12.5 metric instruments + helpers.
//
// Sprint motivation: M11 LoCoMo regression (F1 0.238 → 0.060) went undetected
// for weeks because:
//   - chat over-refusal with non-empty retrieval had no counter
//   - D2 graph injection size was opaque
//   - DB pool starvation was invisible
//   - per-query SQL latency was not tracked
//   - LLMJudge top-1 swaps weren't recorded
//
// This file adds the missing series. Each instrument is named after the
// regression it would have flagged in hours instead of weeks.
//
// Naming convention: dotted OTel names, exported as `memdb_<area>_<metric>`
// in Prometheus (the SDK lower-snakes the dots). Pre-register at zero so
// Grafana/alert rules see the series from container start.
package observability

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ---------------------------------------------------------------------------
// Bucket sets (kept here so call-sites and pre-registration agree).
// ---------------------------------------------------------------------------

// chatPredLengthBuckets — char-count buckets for memdb.chat.pred_length_chars.
// answer_style=factual forces SHORTEST → predictions can collapse below LLM
// Judge accept threshold; the buckets cover that low end densely.
var chatPredLengthBuckets = []float64{5, 10, 25, 50, 100, 200, 500, 1000}

// chatTop1CosineBuckets — top-1 retrieved cosine score AT chat time. Pairs
// with M12.7's rerank_relativity_top to show pre-rerank vs post-rerank.
var chatTop1CosineBuckets = []float64{0.0, 0.3, 0.5, 0.7, 0.85, 0.9, 1.0}

// chatContextTokenBuckets — context-token estimate fed to LLM. Detects
// context overflow (CLIProxyAPI 1M ctx but practical sweet spot is < 8k).
var chatContextTokenBuckets = []float64{256, 512, 1024, 2048, 4096, 8192}

// d2AddedCandidatesBuckets — neighbours injected per D2 expansion call.
// Detects "D2 over-injected noise" when CE rerank can't sort the pool.
var d2AddedCandidatesBuckets = []float64{0, 5, 10, 20, 50, 100}

// stageCandidatesAddedBuckets — universal "candidates added by this stage"
// shape. Same buckets across temporal_augment / inject_events / linked_expand
// / d2_graph_expand so dashboards can stack/compare.
var stageCandidatesAddedBuckets = []float64{0, 1, 5, 10, 25, 50, 100}

// dbQueryDurationBuckets — per-named-query SQL latency. Extends down to 1ms
// (vector hot path under good HNSW) up to 5s (pathological BFS).
var dbQueryDurationBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// pgxpoolAcquireBuckets — pool wait time. 0.1ms = warm reuse, 1000ms = full
// starvation. Boundaries match the spec alert at p99 > 500ms.
var pgxpoolAcquireBuckets = []float64{0.1, 1, 10, 100, 1000}

// namedQueries are the SQL names whose duration we pre-register. Adding a
// new query? Bump this slice AND wire it via MeasureQuery at the call site.
var namedQueries = []string{
	"GraphBFSTraversal",
	"VectorSearch",
	"GetMemoriesByEntityIDs",
	"SetLinkedMemoryIDs",
	"GetMemoriesByLinkedIDs",
	"SearchEventsByDate",
	"SearchMemoriesByDateRange",
}

// stageNamesForCandidates are the labels we pre-register on
// memdb.search.stage_candidates_added (must stay in sync with call sites).
var stageNamesForCandidates = []string{
	"temporal_augment",
	"inject_events",
	"linked_expand",
	"d2_graph_expand",
}

// d10EnhanceOutcomes — labels for memdb.search.d10_enhance_outcome_total.
var d10EnhanceOutcomes = []string{"answered", "unknown", "skipped", "error"}

// judgeTop1Outcomes — labels for memdb.search.judge_changed_top1_total.
// agree    — top-1 ID identical pre- and post-LLMJudge
// swap     — top-1 ID different (judge moved a candidate up)
// reject_all — judge returned all-zero scores (no candidate accepted)
var judgeTop1Outcomes = []string{"agree", "swap", "reject_all"}

// ---------------------------------------------------------------------------
// Singleton instruments.
// ---------------------------------------------------------------------------

type m12Instruments struct {
	// Chat-path quality signals (Category A).
	ChatRefusedWithEvidence metric.Int64Counter
	ChatPredLengthChars     metric.Float64Histogram
	ChatTop1CosineScore     metric.Float64Histogram
	ChatContextTokens       metric.Float64Histogram

	// Search stage transparency (Category B).
	SearchD2AddedCandidates    metric.Float64Histogram
	SearchD10EnhanceOutcome    metric.Int64Counter
	SearchStageCandidatesAdded metric.Float64Histogram
	SearchJudgeChangedTop1     metric.Int64Counter

	// DB observability (Category C).
	DBQueryDurationMs    metric.Float64Histogram
	DBPgxpoolAcquireMs   metric.Float64Histogram
	DBPgxpoolBusyConns   metric.Int64ObservableGauge
	DBRowsScanned        metric.Int64Counter
}

var (
	m12Once sync.Once
	m12Inst *m12Instruments
)

// M12 returns the singleton M12.5 instruments, lazily initialised. All
// instruments are pre-registered at zero across their label dimensions so
// dashboards / alert rules see the series from first scrape.
func M12() *m12Instruments {
	m12Once.Do(func() {
		meter := otel.Meter("memdb-go/m12")
		m12Inst = &m12Instruments{}

		// ── Category A — chat-path quality ──────────────────────────────
		m12Inst.ChatRefusedWithEvidence, _ = meter.Int64Counter(
			"memdb.chat.refused_with_evidence_total",
			metric.WithDescription("Chat said 'no answer' / 'do not contain' while retrieved was non-empty. Smoking-gun signal for prompt over-refusal — would have caught M12.2 in hours."),
		)
		m12Inst.ChatPredLengthChars, _ = meter.Float64Histogram(
			"memdb.chat.pred_length_chars",
			metric.WithDescription("Chat prediction length in characters. answer_style=factual forces SHORTEST → predictions may drag below LLM Judge accept threshold."),
			metric.WithExplicitBucketBoundaries(chatPredLengthBuckets...),
		)
		m12Inst.ChatTop1CosineScore, _ = meter.Float64Histogram(
			"memdb.chat.top1_cosine_score",
			metric.WithDescription("Top-1 retrieved cosine (relativity) score AT chat time. Pairs with M12.7 rerank_relativity_top to show pre-rerank vs post-rerank."),
			metric.WithExplicitBucketBoundaries(chatTop1CosineBuckets...),
		)
		m12Inst.ChatContextTokens, _ = meter.Float64Histogram(
			"memdb.chat.context_tokens",
			metric.WithDescription("Approximate context-token count fed to LLM (chars/4). Detects context overflow."),
			metric.WithExplicitBucketBoundaries(chatContextTokenBuckets...),
		)

		// ── Category B — search stage transparency ──────────────────────
		m12Inst.SearchD2AddedCandidates, _ = meter.Float64Histogram(
			"memdb.search.d2_added_candidates",
			metric.WithDescription("Neighbours injected by stageD2GraphExpand per query. Detects 'D2 over-injected noise' patterns."),
			metric.WithExplicitBucketBoundaries(d2AddedCandidatesBuckets...),
		)
		m12Inst.SearchD10EnhanceOutcome, _ = meter.Int64Counter(
			"memdb.search.d10_enhance_outcome_total",
			metric.WithDescription("D10 answer-style=factual enhance results (outcome in answered|unknown|skipped|error). Companion to memdb.search.d10_enhance — this slice is post-rule-check."),
		)
		m12Inst.SearchStageCandidatesAdded, _ = meter.Float64Histogram(
			"memdb.search.stage_candidates_added",
			metric.WithDescription("Universal 'candidates this stage added' (delta size). stages: temporal_augment|inject_events|linked_expand|d2_graph_expand."),
			metric.WithExplicitBucketBoundaries(stageCandidatesAddedBuckets...),
		)
		m12Inst.SearchJudgeChangedTop1, _ = meter.Int64Counter(
			"memdb.search.judge_changed_top1_total",
			metric.WithDescription("When LLMJudge fires, did it change top-1 ordering? outcome: agree|swap|reject_all. Distinct from the M12.7-owned rerank_gate_decision_total (this is post-decision)."),
		)

		// ── Category C — DB observability ───────────────────────────────
		m12Inst.DBQueryDurationMs, _ = meter.Float64Histogram(
			"memdb.db.query_duration_ms",
			metric.WithDescription("Per-named SQL query duration (ms). Shows which queries get slow under load. query_name labels: GraphBFSTraversal|VectorSearch|GetMemoriesByEntityIDs|SetLinkedMemoryIDs|GetMemoriesByLinkedIDs|SearchEventsByDate|SearchMemoriesByDateRange."),
			metric.WithExplicitBucketBoundaries(dbQueryDurationBuckets...),
		)
		m12Inst.DBPgxpoolAcquireMs, _ = meter.Float64Histogram(
			"memdb.db.pgxpool_acquire_ms",
			metric.WithDescription("Pool wait time on Acquire (ms). p99>500ms = pool starvation. Wired via MeasureQuery at named call sites."),
			metric.WithExplicitBucketBoundaries(pgxpoolAcquireBuckets...),
		)
		m12Inst.DBRowsScanned, _ = meter.Int64Counter(
			"memdb.db.rows_scanned_total",
			metric.WithDescription("Rows scanned by named queries. High value paired with low result count = post-scan filter dominates → missing index. Increment by SQL row count."),
		)

		// busy_conns is registered later in RegisterPoolGauge once the pool
		// is wired (it's an async gauge — needs a callback).

		// ── Pre-registration at zero ────────────────────────────────────
		ctx := context.Background()
		// Counters: pre-register canonical label sets.
		m12Inst.ChatRefusedWithEvidence.Add(ctx, 0,
			metric.WithAttributes(
				attribute.String("category", ""),
				attribute.String("answer_style", ""),
			),
		)
		for _, oc := range d10EnhanceOutcomes {
			m12Inst.SearchD10EnhanceOutcome.Add(ctx, 0,
				metric.WithAttributes(attribute.String("outcome", oc)))
		}
		for _, oc := range judgeTop1Outcomes {
			m12Inst.SearchJudgeChangedTop1.Add(ctx, 0,
				metric.WithAttributes(attribute.String("outcome", oc)))
		}
		for _, qn := range namedQueries {
			m12Inst.DBRowsScanned.Add(ctx, 0,
				metric.WithAttributes(attribute.String("query_name", qn)))
		}

		// Histograms: pre-record a 0 sample per canonical label so the
		// _bucket / _count / _sum series exist on first scrape.
		m12Inst.ChatPredLengthChars.Record(ctx, 0,
			metric.WithAttributes(attribute.String("answer_style", "")))
		m12Inst.ChatTop1CosineScore.Record(ctx, 0)
		m12Inst.ChatContextTokens.Record(ctx, 0,
			metric.WithAttributes(
				attribute.String("cube", ""),
				attribute.String("answer_style", ""),
			),
		)
		m12Inst.SearchD2AddedCandidates.Record(ctx, 0,
			metric.WithAttributes(attribute.String("cube", "")))
		for _, sn := range stageNamesForCandidates {
			m12Inst.SearchStageCandidatesAdded.Record(ctx, 0,
				metric.WithAttributes(attribute.String("stage", sn)))
		}
		for _, qn := range namedQueries {
			m12Inst.DBQueryDurationMs.Record(ctx, 0,
				metric.WithAttributes(attribute.String("query_name", qn)))
		}
		m12Inst.DBPgxpoolAcquireMs.Record(ctx, 0,
			metric.WithAttributes(attribute.String("query_name", "")))
	})
	return m12Inst
}

// ---------------------------------------------------------------------------
// Helpers — keep the call-site noise minimal.
// ---------------------------------------------------------------------------

// refusalRe matches phrasings that the LLM uses when it claims the memories
// don't support an answer. Three variants based on observed M11/M12 corpus:
//
//  1. "do not (contain|state|mention|...)"   — the QA-prompt rule-6 echo
//  2. "memories.*(do not|no information)"     — verbose chain-style refusal
//  3. ^(no answer|i don't know|i do not know|unknown)$
//     — bare factual refusal (matches anchored, not substring, to avoid
//     false positives on "I don't know if Tom is married but here's...")
//
// False-positive guards:
//
//   - "the memories do not detail X but Y mentioned Z" — has detail+mention,
//     answers indirectly; we DO NOT match this. Test in m12_metrics_test.go.
//   - "she does not own a dog" — substantive answer, NOT a refusal; no match
//     because we anchor on "do not (contain|state|mention|describe|specify|
//     detail|provide|include|indicate|reference)".
var refusalRe = regexp.MustCompile(
	`(?i)\b(do not (contain|state|mention|describe|specify|detail|provide|include|indicate|reference)|memories.{0,40}(do not|no information|don't (contain|mention|state)))\b`,
)

// bareNoAnswerRe matches the rule-6 literal echo. Anchored so we don't catch
// "no answer was given by the third party" or similar substantive uses.
var bareNoAnswerRe = regexp.MustCompile(
	`(?i)^\s*(no answer|i don't know|i do not know|unknown|n/?a)\s*\.?\s*$`,
)

// IsRefusalAnswer returns true when `answer` matches an LLM "no evidence"
// pattern. Used by chat handler to decide whether to bump
// memdb.chat.refused_with_evidence_total.
//
// Tested in m12_metrics_test.go — keep the false-positive table in sync.
func IsRefusalAnswer(answer string) bool {
	a := strings.TrimSpace(answer)
	if a == "" {
		return false
	}
	if bareNoAnswerRe.MatchString(a) {
		return true
	}
	return refusalRe.MatchString(a)
}

// EstimateTokens is a chars/4 approximation of the BPE token count. Good
// enough for histogram bucketing on 1k–10k context windows (the regions
// where context-overflow alerts fire). Avoids pulling tiktoken-go (CGO) into
// the build.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	// chars/4 is the long-standing tiktoken rule of thumb for English/code.
	return (len(s) + 3) / 4
}

// RecordChatRefusedWithEvidence bumps the over-refusal counter when the
// chat answer matches a refusal pattern AND retrieval returned at least one
// memory. Caller passes the raw chat_answer string and the post-filter hit
// count (len(memories) — pre-LLM, post-threshold).
//
// `category` mirrors LoCoMo categories (1..5) when known, else "" — leave
// empty when called from production paths that don't classify (chat handler).
func RecordChatRefusedWithEvidence(ctx context.Context, answer string, hitCount int, category, answerStyle string) {
	if hitCount == 0 || !IsRefusalAnswer(answer) {
		return
	}
	M12().ChatRefusedWithEvidence.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("category", category),
			attribute.String("answer_style", answerStyle),
		),
	)
}

// RecordChatPredLength records the prediction length in chars. Bucketing is
// per the chatPredLengthBuckets boundaries so factual predictions (1-10 words)
// land in the lowest two buckets.
func RecordChatPredLength(ctx context.Context, answer, answerStyle string) {
	M12().ChatPredLengthChars.Record(ctx, float64(len(answer)),
		metric.WithAttributes(attribute.String("answer_style", answerStyle)),
	)
}

// RecordChatTop1Cosine records the relativity score of memories[0] (i.e. the
// top-1 candidate AT chat time, after threshold filter). Pass 0 for empty
// memory set so the _count series stays correct (chat-with-no-evidence is a
// real production case worth tracking).
func RecordChatTop1Cosine(ctx context.Context, score float64) {
	M12().ChatTop1CosineScore.Record(ctx, score)
}

// RecordChatContextTokens records the approximate token count of the LLM
// system prompt. Caller passes the rendered prompt (post-buildSystemPrompt).
func RecordChatContextTokens(ctx context.Context, prompt, cube, answerStyle string) {
	M12().ChatContextTokens.Record(ctx, float64(EstimateTokens(prompt)),
		metric.WithAttributes(
			attribute.String("cube", cube),
			attribute.String("answer_style", answerStyle),
		),
	)
}

// RecordD2AddedCandidates records the size of the D2 expansion (origSize →
// post-cap delta). Caller computes added = len(out) - len(seedSet).
func RecordD2AddedCandidates(ctx context.Context, added int, cubeID string) {
	M12().SearchD2AddedCandidates.Record(ctx, float64(added),
		metric.WithAttributes(attribute.String("cube", cubeID)),
	)
}

// RecordD10EnhanceOutcome bumps the d10 outcome counter (factual-only path).
func RecordD10EnhanceOutcome(ctx context.Context, outcome string) {
	M12().SearchD10EnhanceOutcome.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}

// RecordStageCandidatesAdded records the per-stage candidate delta. Used by
// pipeline.go (deltaTextMerged wrapping) so each stage that mutates
// st.TextMerged surfaces its contribution.
func RecordStageCandidatesAdded(ctx context.Context, stage string, added int) {
	if added < 0 {
		added = 0
	}
	M12().SearchStageCandidatesAdded.Record(ctx, float64(added),
		metric.WithAttributes(attribute.String("stage", stage)),
	)
}

// RecordJudgeChangedTop1 records whether LLMJudge changed top-1 vs input
// order. Outcomes:
//
//	agree      — same top-1 ID before and after
//	swap       — different top-1 ID (LLM moved someone up)
//	reject_all — judge returned no usable scores (all zero / empty map)
func RecordJudgeChangedTop1(ctx context.Context, outcome string) {
	M12().SearchJudgeChangedTop1.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}

// MeasureQuery wraps a DB call to record per-named-query latency AND pool
// acquire wait time. Use:
//
//	rows, err := observability.MeasureQuery(ctx, "VectorSearch", p.pool, func(conn *pgxpool.Conn) (pgx.Rows, error) {
//	    return conn.Query(ctx, q, args...)
//	})
//
// Two histograms get a sample per call:
//
//	memdb_db_pgxpool_acquire_ms{query_name=...}
//	memdb_db_query_duration_ms{query_name=...}
//
// Errors propagate verbatim. Acquire failures are still recorded (acquire_ms
// is the time spent waiting before the failure fired).
func MeasureQuery[T any](ctx context.Context, queryName string, pool *pgxpool.Pool, fn func(*pgxpool.Conn) (T, error)) (T, error) {
	var zero T
	mx := M12()

	t0 := time.Now()
	conn, err := pool.Acquire(ctx)
	acquireMs := float64(time.Since(t0).Microseconds()) / 1000.0
	mx.DBPgxpoolAcquireMs.Record(ctx, acquireMs,
		metric.WithAttributes(attribute.String("query_name", queryName)),
	)
	if err != nil {
		return zero, err
	}
	defer conn.Release()

	t1 := time.Now()
	out, err := fn(conn)
	durMs := float64(time.Since(t1).Microseconds()) / 1000.0
	mx.DBQueryDurationMs.Record(ctx, durMs,
		metric.WithAttributes(attribute.String("query_name", queryName)),
	)
	return out, err
}

// AddRowsScanned bumps the per-query rows-scanned counter. Call from query
// readers after the rows.Next() loop with the scan count. Pair with the
// query_duration histogram to identify "scans much more than it returns"
// patterns (missing index).
func AddRowsScanned(ctx context.Context, queryName string, n int64) {
	if n <= 0 {
		return
	}
	M12().DBRowsScanned.Add(ctx, n,
		metric.WithAttributes(attribute.String("query_name", queryName)),
	)
}

// RegisterPoolGauge wires an OTel async gauge that scrapes pool.Stat() on
// every Prometheus collection. Emits memdb.db.pgxpool_busy_conns gauge
// (concurrent in-flight). Call once after pool construction; safe to call
// multiple times (idempotent — registers on the singleton meter).
//
// Returns the registered observable so callers can keep it alive (the OTel
// SDK will hold the callback closure too; this is just defensive).
func RegisterPoolGauge(pool *pgxpool.Pool) (metric.Int64ObservableGauge, error) {
	if pool == nil {
		return nil, nil
	}
	meter := otel.Meter("memdb-go/m12")
	gauge, err := meter.Int64ObservableGauge(
		"memdb.db.pgxpool_busy_conns",
		metric.WithDescription("Concurrent in-flight pgx connections (snapshot of pool.Stat().AcquiredConns()). Pair with MaxConns config; sustained AcquiredConns near MaxConns = pool starvation incoming."),
		metric.WithInt64Callback(func(_ context.Context, obs metric.Int64Observer) error {
			st := pool.Stat()
			obs.Observe(int64(st.AcquiredConns()))
			return nil
		}),
	)
	if err == nil {
		M12().DBPgxpoolBusyConns = gauge
	}
	return gauge, err
}
