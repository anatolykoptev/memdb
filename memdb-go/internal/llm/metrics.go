// Package llm — domain metrics (Prometheus via OTel).
package llm

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	llmMetricsOnce        sync.Once
	llmMetricsInstruments *llmMetricsStruct
)

type llmMetricsStruct struct {
	Requests metric.Int64Counter
	Duration metric.Float64Histogram
}

// llmMetrics returns the singleton LLM instruments, lazy-initialised.
func llmMetrics() *llmMetricsStruct {
	llmMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/llm")
		reqs, _ := meter.Int64Counter("memdb.llm.requests_total",
			metric.WithDescription("Total LLM chat requests by model and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.llm.duration_ms",
			metric.WithDescription("LLM chat request duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		llmMetricsInstruments = &llmMetricsStruct{
			Requests: reqs,
			Duration: dur,
		}
	})
	return llmMetricsInstruments
}

// --- M11 R0: per-prompt structured-call metrics. ---

var (
	llmStructuredMetricsOnce sync.Once
	llmStructuredInstruments *llmStructuredMetricsStruct
)

type llmStructuredMetricsStruct struct {
	Calls    metric.Int64Counter
	Duration metric.Float64Histogram
}

// preregisteredStructuredPromptIDs are the M11 R0 callsite prompt IDs. Each
// is pre-registered at zero so Prometheus scrapes see the series from
// container start (avoids "metric not found" gaps in dashboards / alerts
// before traffic warms the path).
var preregisteredStructuredPromptIDs = []string{
	"d5_staged",
	"d4_rewrite",
	"d11_cot",
	"d7_decompose",
	"d6_iterative",
	"d10_enhance_legacy",
	"d10_enhance",
	"search_fine_judge",
	"llm_rerank",
	"profiler",
}

// preregisteredStructuredOutcomes are the outcome label values emitted by
// ChatStructured / ChatText. Pre-registering each for the prompt-IDs above
// gives a complete grid of {prompt_id, outcome} series at scrape time.
var preregisteredStructuredOutcomes = []string{
	"success",
	"parse_error",
	"http_error",
	"timeout",
	"retried",
}

// llmStructuredMetrics returns the singleton structured-call instruments,
// lazy-initialised. Pre-registers all prompt_id × outcome combinations at
// zero on first call.
func llmStructuredMetrics() *llmStructuredMetricsStruct {
	llmStructuredMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/llm")
		calls, _ := meter.Int64Counter("memdb.llm.structured_call_total",
			metric.WithDescription("Per-prompt structured LLM call outcomes (success|parse_error|http_error|timeout|retried)"),
		)
		dur, _ := meter.Float64Histogram("memdb.llm.structured_duration_ms",
			metric.WithDescription("Per-prompt structured LLM call wall duration in milliseconds"),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(50, 100, 250, 500, 1000, 2500, 5000, 10000),
		)
		llmStructuredInstruments = &llmStructuredMetricsStruct{
			Calls:    calls,
			Duration: dur,
		}

		// Pre-register every (prompt_id, outcome) cell at zero. Same pattern
		// as internal/search/metrics.go (M1).
		ctx := context.Background()
		for _, pid := range preregisteredStructuredPromptIDs {
			for _, oc := range preregisteredStructuredOutcomes {
				calls.Add(ctx, 0, metric.WithAttributes(
					attribute.String("prompt_id", pid),
					attribute.String("outcome", oc),
				))
			}
			// Histogram has no zero-add idiom; recording 0 once would skew
			// data. Leaving duration unseeded — it appears on first real call.
		}
	})
	return llmStructuredInstruments
}
