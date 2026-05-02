package search

// answer_enhance_pipeline.go — D10 pipeline integration.
//
// Concern split: answer_enhance.go owns the prompt + LLM call (what the
// extractor does). This file owns where it fires from the search pipeline,
// the synthetic-item insertion, and the metric-label plumbing — all of
// which are postProcessResults concerns, not extractor concerns.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// answerEnhanceMinPrependConfidence is the LLM self-reported confidence
// floor below which the synthetic answer is NOT prepended. Forensic
// 2026-05-02 fix #3: low-confidence answers were polluting the top of
// the result list, displacing real top-K memories that had higher
// retrieval relevance. Conservative threshold — pre-existing behaviour
// can be re-enabled by setting MEMDB_ENHANCE_PREPEND_MIN_CONFIDENCE=0.
const answerEnhanceMinPrependConfidence = 0.5

// prependEnhancedAnswer inserts a synthetic EnhancedAnswer item at position 0
// of items. Downstream formatting treats this as the top result, but the
// ordering of all other items is preserved.
//
// The synthetic id is "enhanced-" + first 12 hex chars of sha256(query), so
// identical queries yield stable ids across requests.
//
// Forensic 2026-05-02 fix #3 (refactor):
//   - Synthetic relativity is now top1_score + epsilon (small) instead of
//     a hardcoded 1.0. This keeps natural ranking continuity downstream
//     (sorting code can't reorder it below real items, but the score is
//     still comparable for percentile / threshold calculations) and
//     avoids artificially boosting the synth ahead of legitimately
//     stronger memories.
//   - The global mutation loop that stamped enhanced_answer /
//     enhanced_confidence onto every other item is GONE — it was a
//     side-effect that made every map in the pipeline carry duplicate
//     answer text and broke metric label cardinality assumptions.
//     Consumers that need the answer should read it from the synthetic
//     item's metadata only.
//   - Caller is responsible for confidence + source-id gating. See
//     computeAnswerEnhancement / applyAnswerEnhancementAfterTrim for the policy.
func prependEnhancedAnswer(items []map[string]any, answer string, sourceIDs []string, conf float64, query string) []map[string]any {
	sum := sha256.Sum256([]byte(query))
	id := "enhanced-" + hex.EncodeToString(sum[:])[:answerEnhanceSynthIDHexLen]

	// Top-1 anchored relativity: epsilon above the strongest existing item
	// (or 1.0 when items is empty / has no relativity field). epsilon is
	// 0.001 — large enough to dominate equal-score ties via the
	// downstream sortByRelativity DESC, small enough not to perturb
	// percentile-based metrics.
	var top1 float64
	for _, it := range items {
		meta, ok := it["metadata"].(map[string]any)
		if !ok {
			continue
		}
		if r, ok := meta["relativity"].(float64); ok && r > top1 {
			top1 = r
		}
	}
	synthRel := top1 + 0.001
	if synthRel <= 0 {
		synthRel = 1.0
	}

	synth := map[string]any{
		"id":     id,
		"memory": answer,
		"metadata": map[string]any{
			"memory_type": "EnhancedAnswer",
			"id":          id,
			"user_name":   "",
			"confidence":  conf,
			"source_ids":  sourceIDs,
			"relativity":  synthRel,
			"enhanced":    true,
		},
		"ref_id": "[enhanced]",
	}

	out := make([]map[string]any, 0, len(items)+1)
	out = append(out, synth)
	out = append(out, items...)
	return out
}

// enhancementResult captures a synthesised answer + the inputs needed to
// build the synthetic item. Returned by computeAnswerEnhancement so the
// caller can defer the actual prepend until AFTER TrimSlice — synth is
// supposed to add ABOVE the top-K cap, not displace a real top-K result.
//
// `ok=false` means no synth should be added (LLM error, threshold below,
// low confidence, missing source_ids, etc.). The caller's items slice
// must be returned unchanged in that case.
type enhancementResult struct {
	answer    string
	sourceIDs []string
	conf      float64
	ok        bool
}

// computeAnswerEnhancement runs the LLM call and returns the result
// without mutating items. The actual prepend happens in
// applyAnswerEnhancementAfterTrim, called by postProcessResults AFTER
// TrimSlice. Forensic 2026-05-02 fix #3.
//
// emb may be nil; in that case the soft-routing classifier is bypassed
// and the call is byte-identical to the post-revert single-prompt path.
func computeAnswerEnhancement(
	ctx context.Context,
	logger *slog.Logger,
	query string,
	items []map[string]any,
	cfg AnswerEnhanceConfig,
	emb classifierEmbedder,
) enhancementResult {
	d10Attrs := func(outcome string, hinted bool) metric.MeasurementOption {
		return metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("hinted", hintedLabel(hinted)),
		)
	}
	// Forensic 2026-05-01: D10 entry trace. memdb_search_d10_enhance_total=0
	// in prod despite MEMDB_D10_CLASSIFIER_ENABLED=true — the actual gate
	// is MEMDB_SEARCH_ENHANCE (answerEnhanceEnabled()), defaulted off in
	// docker-compose. Logging at Debug confirms whether D10 is being
	// reached at all without forcing a metric label change.
	if logger != nil {
		logger.Debug("D10 enhance: entered",
			slog.String("query", query),
			slog.Int("item_count", len(items)),
			slog.Bool("enhance_enabled", answerEnhanceEnabled()),
			slog.Bool("classifier_enabled", d10ClassifierEnabled()),
		)
	}
	if !answerEnhanceEnabled() || len(items) == 0 || cfg.APIURL == "" {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("skipped", false))
		observability.RecordD10EnhanceOutcome(ctx, "skipped")
		return enhancementResult{}
	}
	// Pre-check relativity floor to distinguish "threshold below" (skipped)
	// from a genuine LLM UNKNOWN response.
	minRel := answerEnhanceMinRelativity()
	anyRelevant := false
	for _, it := range items {
		if getRelativity(it) >= minRel {
			anyRelevant = true
			break
		}
	}
	if !anyRelevant {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("skipped", false))
		observability.RecordD10EnhanceOutcome(ctx, "skipped")
		return enhancementResult{}
	}
	answer, sources, conf, hinted, trace, err := EnhanceRetrievalAnswer(ctx, query, items, cfg, emb)

	// Routing observability — fired regardless of LLM outcome so the route
	// distribution is attributable per request, not only on the answered
	// path. recordD10Routing is a no-op for the no-signal branch (HasSignal
	// gates the histogram emits internally).
	recordD10Routing(ctx, trace)

	// Sample log every D10 call (Debug level — captured by tracing pipeline,
	// off in production unless slog level is bumped). Full distribution +
	// chosen mode + outcome is what we need to reconstruct routing decisions
	// from a replay log without re-running the classifier.
	if logger != nil && logger.Enabled(ctx, slog.LevelDebug) {
		logger.Debug("d10 routing decision",
			slog.String("mode", string(trace.Mode)),
			slog.String("top1_cat", trace.top1Label()),
			slog.Float64("top1_conf", trace.Top1Conf),
			slog.Float64("top2_conf", trace.Top2Conf),
			slog.Float64("entropy", trace.Entropy),
			slog.Bool("has_signal", trace.HasSignal),
			slog.String("query", query),
		)
	}

	if err != nil {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("error", hinted))
		observability.RecordD10EnhanceOutcome(ctx, "error")
		recordD10OutcomeByCat(ctx, trace, "error")
		if logger != nil {
			logger.Debug("enhance failed, continuing without", slog.Any("error", err))
		}
		return enhancementResult{}
	}
	if answer == "" || answer == answerEnhanceUnknownAnswer {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("unknown", hinted))
		observability.RecordD10EnhanceOutcome(ctx, "unknown")
		recordD10OutcomeByCat(ctx, trace, "unknown")
		return enhancementResult{}
	}
	searchMx().D10Enhance.Add(ctx, 1, d10Attrs("answered", hinted))
	searchMx().D10Conf.Record(ctx, conf)
	observability.RecordD10EnhanceOutcome(ctx, "answered")
	recordD10OutcomeByCat(ctx, trace, "answered")

	// Forensic 2026-05-02 fix #3: gate the prepend on minimum confidence
	// + non-empty source_ids. A low-confidence or unsourced answer
	// poisons the top of the result list — we surface it as "answered"
	// for the metric (the LLM did succeed) but withhold the prepend so
	// real top-K memories keep their slots.
	if conf < answerEnhanceMinPrependConfidence || len(sources) == 0 {
		if logger != nil {
			logger.Debug("D10 enhance: synth withheld (low conf / no sources)",
				slog.Float64("confidence", conf),
				slog.Int("source_ids", len(sources)),
			)
		}
		return enhancementResult{}
	}

	return enhancementResult{
		answer:    answer,
		sourceIDs: sources,
		conf:      conf,
		ok:        true,
	}
}

// applyAnswerEnhancementAfterTrim is the post-trim hook called by
// postProcessResults. Runs computeAnswerEnhancement on the pre-trim items
// (so the LLM sees the strongest evidence), then prepends the synthetic
// answer onto the post-trim list (so the synth adds ABOVE the top-K cap
// rather than displacing a real top-K result).
//
// Forensic 2026-05-02 fix #3 — the previous code path called the LLM and
// the prepend BEFORE TrimSlice, which meant a synthetic always evicted
// the bottom-ranked real memory at the trim boundary even when the synth
// confidence was high enough to claim slot 0 anyway.
func applyAnswerEnhancementAfterTrim(
	ctx context.Context,
	logger *slog.Logger,
	query string,
	preTrim []map[string]any,
	postTrim []map[string]any,
	cfg AnswerEnhanceConfig,
	emb classifierEmbedder,
) []map[string]any {
	res := computeAnswerEnhancement(ctx, logger, query, preTrim, cfg, emb)
	if !res.ok {
		return postTrim
	}
	return prependEnhancedAnswer(postTrim, res.answer, res.sourceIDs, res.conf, query)
}

// hintedLabel maps the bool to the metric label value. Two values keep
// label cardinality bounded at 2 (yes/no) × 4 outcomes = 8 series.
func hintedLabel(hinted bool) string {
	if hinted {
		return "yes"
	}
	return "no"
}
