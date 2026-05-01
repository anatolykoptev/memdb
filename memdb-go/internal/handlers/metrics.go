// Package handlers — domain metrics for handler-level instrumentation.
// Instruments are created on first access and read by the Prometheus exporter.
package handlers

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	bufferMetricsOnce sync.Once
	bufferMetrics     *bufferMetricsInstruments
)

type bufferMetricsInstruments struct {
	// FlushErrors counts buffer flush failures by reason.
	// reason ∈ {lua, parse, db, other}. rate(...[5m]) > 10 alerts.
	FlushErrors metric.Int64Counter
}

// bufferMx returns the singleton buffer instruments, lazy-initialised.
func bufferMx() *bufferMetricsInstruments {
	bufferMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/buffer")
		flush, _ := meter.Int64Counter("memdb.buffer.flush_errors",
			metric.WithDescription("Count of buffer flush failures by reason (lua/parse/db/other). Burst alerts via increase([5m])>10."),
		)
		bufferMetrics = &bufferMetricsInstruments{FlushErrors: flush}
	})
	return bufferMetrics
}

var (
	metricsOnce     sync.Once
	feedbackMetrics *feedbackMetricsInstruments
)

type feedbackMetricsInstruments struct {
	Requests   metric.Int64Counter
	Duration   metric.Float64Histogram
	Operations metric.Int64Counter
}

// feedbackMx returns the singleton feedback instruments, lazy-initialised.
func feedbackMx() *feedbackMetricsInstruments {
	metricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/feedback")
		reqs, _ := meter.Int64Counter("memdb.feedback.requests",
			metric.WithDescription("Total feedback requests processed"),
		)
		dur, _ := meter.Float64Histogram("memdb.feedback.duration_ms",
			metric.WithDescription("Feedback pipeline duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		ops, _ := meter.Int64Counter("memdb.feedback.operations",
			metric.WithDescription("Feedback pipeline operations by type (ADD/UPDATE/NONE/KEYWORD_REPLACE/PURE_ADD)"),
		)
		feedbackMetrics = &feedbackMetricsInstruments{
			Requests:   reqs,
			Duration:   dur,
			Operations: ops,
		}
	})
	return feedbackMetrics
}

var (
	chatPromptOnce    sync.Once
	chatPromptMetrics *chatPromptMetricsInstruments
)

type chatPromptMetricsInstruments struct {
	// TemplateUsed counts chat requests by which system-prompt template was selected.
	// Label template ∈ {factual, conversational, custom}.
	//   - "custom"         — non-empty system_prompt was provided (basePrompt wins).
	//   - "factual"        — answer_style="factual" and basePrompt empty.
	//   - "conversational" — default branch (includes empty answer_style).
	TemplateUsed metric.Int64Counter
}

// chatPromptTemplateLabels lists the canonical template label values for
// memdb.chat.prompt_template_used_total. Pre-registered at zero so dashboards
// and alert rules see the full series space from container start.
// Labels: factual_high | factual_low | factual_zero | conversational | custom.
var chatPromptTemplateLabels = []string{
	"factual_high",
	"factual_low",
	"factual_zero",
	"conversational",
	"custom",
}

// chatPromptMx returns the singleton chat-prompt instruments, lazy-initialised.
// Counter memdb.chat.prompt_template_used_total{template=factual_high|factual_low|factual_zero|conversational|custom}.
func chatPromptMx() *chatPromptMetricsInstruments {
	chatPromptOnce.Do(func() {
		meter := otel.Meter("memdb-go/chat")
		used, _ := meter.Int64Counter("memdb.chat.prompt_template_used_total",
			metric.WithDescription("Count of chat requests per system-prompt template (factual_high/factual_low/factual_zero/conversational/custom)."),
		)
		// Pre-register all template label values at zero so Prometheus emits
		// the series before the first real chat request. Matches the enum in
		// promptTemplateLabel (chat_record.go).
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, tpl := range chatPromptTemplateLabels {
			used.Add(ctx, 0, metric.WithAttributes(attribute.String("template", tpl)))
		}
		chatPromptMetrics = &chatPromptMetricsInstruments{TemplateUsed: used}
	})
	return chatPromptMetrics
}

// ── Feedback events counter (reward scaffold) ─────────────────────────────────

var (
	feedbackEventsOnce    sync.Once
	feedbackEventsMetrics *feedbackEventsInstruments
)

type feedbackEventsInstruments struct {
	// EventsTotal counts persisted feedback_events rows by label.
	// label ∈ {positive, negative, neutral, correction}.
	// Pre-registered at zero so Prometheus sees the series before the first write.
	EventsTotal metric.Int64Counter
}

// feedbackEventsMx returns the singleton reward-scaffold instruments, lazy-initialised.
// Counter memdb.feedback.events_total{label=positive|negative|neutral|correction}.
func feedbackEventsMx() *feedbackEventsInstruments {
	feedbackEventsOnce.Do(func() {
		meter := otel.Meter("memdb-go/feedback")
		total, _ := meter.Int64Counter("memdb.feedback.events_total",
			metric.WithDescription("Count of feedback_events rows persisted to Postgres, labelled by label (positive/negative/neutral/correction). Powers M11 reward loop."),
		)
		// Pre-register all label values at 0 so dashboards see the series immediately.
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, label := range []string{"positive", "negative", "neutral", "correction"} {
			total.Add(ctx, 0, metric.WithAttributes(attribute.String("label", label)))
		}
		feedbackEventsMetrics = &feedbackEventsInstruments{EventsTotal: total}
	})
	return feedbackEventsMetrics
}

// ── Canary acceptance counter ─────────────────────────────────────────────────

var (
	chatAcceptanceOnce    sync.Once
	chatAcceptanceMetrics *chatAcceptanceInstruments
)

type chatAcceptanceInstruments struct {
	// Total counts answer-acceptance events with attributes style and outcome.
	// style  ∈ {factual, conversational, <any>}
	// outcome ∈ {accept, reject, <any>}  — label values are caller-defined.
	// Stream 8 PRODUCT increments this counter as part of the canary logic;
	// registering here decouples the metric lifecycle from the canary feature flag.
	Total metric.Int64Counter
}

// chatAcceptanceMx returns the singleton canary-acceptance instruments, lazy-initialised.
// Counter memdb.chat.answer_acceptance_total{style=...,outcome=...}.
func chatAcceptanceMx() *chatAcceptanceInstruments {
	chatAcceptanceOnce.Do(func() {
		meter := otel.Meter("memdb-go/chat")
		total, _ := meter.Int64Counter("memdb.chat.answer_acceptance_total",
			metric.WithDescription("Answer acceptance canary counter by style and outcome. Incremented by Stream 8 PRODUCT."),
		)
		chatAcceptanceMetrics = &chatAcceptanceInstruments{Total: total}
	})
	return chatAcceptanceMetrics
}

// ── M12.2 — refusal-reason counter + top-retrieval-score histogram ────────────

var (
	chatRefusalOnce    sync.Once
	chatRefusalMetrics *chatRefusalInstruments
)

type chatRefusalInstruments struct {
	// RefusalTotal counts factual-style chat requests labelled by the
	// prompt-routing decision. label reason ∈ {none, no_memories, low_confidence, other}.
	// label variant ∈ {none, high, low, zero}. "none/none" is the happy-path
	// (high-confidence prompt fired); "low_confidence" / "no_memories" are
	// the targets of the M12.2 ChatRefusalSpike alert.
	RefusalTotal metric.Int64Counter

	// TopRetrievalScore observes the max(metadata.relativity) of the memory
	// pool that fed buildSystemPromptWithDecision. Powers the
	// "context_signal_strength" dashboard panel and the calibration check
	// against MEMDB_FACTUAL_CONFIDENCE_THRESHOLD.
	TopRetrievalScore metric.Float64Histogram

	// ConfidenceComponents observes the per-term contributions of the
	// multi-feature confidence formula (top1, spread, density, median,
	// combined). One label `component` keeps cardinality flat (5 series)
	// regardless of formula choice, supporting A/B between top1-only and
	// multifeature gates. Range [0, 1] for every component.
	ConfidenceComponents metric.Float64Histogram
}

// chatRefusalMx returns the singleton M12.2 chat-refusal instruments,
// lazy-initialised. Pre-registers the four reason labels at zero so
// dashboards see the full series space immediately.
//
// Counter memdb.chat.refusal_total{reason=none|no_memories|low_confidence|other,variant=none|high|low|zero}.
// Histogram memdb.chat.top_retrieval_score (unitless, range [0, 1]).
func chatRefusalMx() *chatRefusalInstruments {
	chatRefusalOnce.Do(func() {
		meter := otel.Meter("memdb-go/chat")
		total, _ := meter.Int64Counter("memdb.chat.refusal_total",
			metric.WithDescription("Count of factual-style chat requests labelled by refusal reason and prompt variant. Powers ChatRefusalSpike alert."),
		)
		score, _ := meter.Float64Histogram("memdb.chat.top_retrieval_score",
			metric.WithDescription("Top-1 metadata.relativity (max cosine score) of the memory pool seen by the factual chat prompt builder."),
		)
		components, _ := meter.Float64Histogram("memdb.chat.factual_confidence_components",
			metric.WithDescription("Per-term contributions of the multi-feature retrieval-confidence formula. Label component ∈ {top1,spread,density,median,combined}; value range [0, 1]."),
		)
		// Pre-register reason labels at 0 so Prometheus emits the series
		// before the first refusal happens.
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, reason := range []string{"none", "no_memories", "low_confidence", "other"} {
			total.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", reason)))
		}
		// Pre-register every component label at 0 — operators see the full
		// series space even before the first factual chat request.
		for _, comp := range []string{"top1", "spread", "density", "median", "combined"} {
			components.Record(ctx, 0, metric.WithAttributes(attribute.String("component", comp)))
		}
		chatRefusalMetrics = &chatRefusalInstruments{
			RefusalTotal:         total,
			TopRetrievalScore:    score,
			ConfidenceComponents: components,
		}
	})
	return chatRefusalMetrics
}
