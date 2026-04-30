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

// prependEnhancedAnswer inserts a synthetic EnhancedAnswer item at position 0
// of items. Downstream formatting treats this as the top result, but the
// ordering of all other items is preserved.
//
// The synthetic id is "enhanced-" + first 12 hex chars of sha256(query), so
// identical queries yield stable ids across requests.
func prependEnhancedAnswer(items []map[string]any, answer string, sourceIDs []string, conf float64, query string) []map[string]any {
	sum := sha256.Sum256([]byte(query))
	id := "enhanced-" + hex.EncodeToString(sum[:])[:answerEnhanceSynthIDHexLen]

	synth := map[string]any{
		"id":     id,
		"memory": answer,
		"metadata": map[string]any{
			"memory_type": "EnhancedAnswer",
			"id":          id,
			"user_name":   "",
			"confidence":  conf,
			"source_ids":  sourceIDs,
			"relativity":  1.0,
			"enhanced":    true,
		},
		"ref_id": "[enhanced]",
	}

	// Also mark downstream items with the enhanced_answer / enhanced_confidence hints.
	for _, it := range items {
		meta, ok := it["metadata"].(map[string]any)
		if !ok {
			continue
		}
		meta["enhanced_answer"] = answer
		meta["enhanced_confidence"] = conf
	}

	out := make([]map[string]any, 0, len(items)+1)
	out = append(out, synth)
	out = append(out, items...)
	return out
}

// applyAnswerEnhancement is the pipeline hook. Called from postProcessResults
// (step 6.8) iff MEMDB_SEARCH_ENHANCE=true and a reranker LLM config is
// available. On LLM failure it logs at Debug and returns items unchanged
// (graceful degrade — never fails the whole search).
//
// emb may be nil; in that case the soft-routing classifier is bypassed and
// the call is byte-identical to the post-revert single-prompt path.
func applyAnswerEnhancement(
	ctx context.Context,
	logger *slog.Logger,
	query string,
	items []map[string]any,
	cfg AnswerEnhanceConfig,
	emb classifierEmbedder,
) []map[string]any {
	d10Attrs := func(outcome string, hinted bool) metric.MeasurementOption {
		return metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("hinted", hintedLabel(hinted)),
		)
	}
	if !answerEnhanceEnabled() || len(items) == 0 || cfg.APIURL == "" {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("skipped", false))
		observability.RecordD10EnhanceOutcome(ctx, "skipped")
		return items
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
		return items
	}
	answer, sources, conf, hinted, err := EnhanceRetrievalAnswer(ctx, query, items, cfg, emb)
	if err != nil {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("error", hinted))
		observability.RecordD10EnhanceOutcome(ctx, "error")
		if logger != nil {
			logger.Debug("enhance failed, continuing without", slog.Any("error", err))
		}
		return items
	}
	if answer == "" || answer == answerEnhanceUnknownAnswer {
		searchMx().D10Enhance.Add(ctx, 1, d10Attrs("unknown", hinted))
		observability.RecordD10EnhanceOutcome(ctx, "unknown")
		return items
	}
	searchMx().D10Enhance.Add(ctx, 1, d10Attrs("answered", hinted))
	searchMx().D10Conf.Record(ctx, conf)
	observability.RecordD10EnhanceOutcome(ctx, "answered")
	return prependEnhancedAnswer(items, answer, sources, conf, query)
}

// hintedLabel maps the bool to the metric label value. Two values keep
// label cardinality bounded at 2 (yes/no) × 4 outcomes = 8 series.
func hintedLabel(hinted bool) string {
	if hinted {
		return "yes"
	}
	return "no"
}
