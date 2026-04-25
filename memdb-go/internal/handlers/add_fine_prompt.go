package handlers

// add_fine_prompt.go — date-aware extraction prompt augmentation (M9 Stream 4).
// Responsibility: gate the `[mention YYYY-MM-DD]` time-anchoring instruction
// behind MEMDB_DATE_AWARE_EXTRACT (default true) and surface it to the LLM
// extractor via the existing hints channel.
//
// Why a hint and not a system-prompt rewrite:
//   - The unified extractor in internal/llm exposes hints as the documented
//     extension point — they get wrapped in <content_hints> on the user side
//     and reliably steer the model without touching cross-package state.
//   - Keeps the change disjoint from streams editing extractor.go.
//
// Reference: Memobase's extract_profile.py (lines 26, 32, 97-98) — explicit
// "[mention YYYY/MM/DD]" tags + "never use relative dates" instruction
// reportedly drove their temporal F1 to 85.05% (public best, +5pp over runner-up).
// We stay with hyphenated ISO 8601 (YYYY-MM-DD) per spec.

import (
	"context"
	"os"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// dateAwareExtractEnvVar is the environment toggle for the date-aware extract prompt.
const dateAwareExtractEnvVar = "MEMDB_DATE_AWARE_EXTRACT"

// dateAwareExtractHint is the verbatim instruction injected into the LLM
// extraction call when MEMDB_DATE_AWARE_EXTRACT is enabled. The hint follows
// the existing `<content_hints>` wrapping in ExtractAndDedup.
//
// Two prongs (matching Memobase's recipe):
//  1. `[mention YYYY-MM-DD]` tag whenever a fact references time
//  2. Hard ban on relative dates ("today", "yesterday", "last week")
const dateAwareExtractHint = "Date convention: when extracting facts that reference time, prefix the time component with `[mention YYYY-MM-DD]` using the closest specific ISO date you can infer from the conversation timestamps or context. Never write relative dates like 'today', 'last week', 'recently' — always resolve to ISO format."

// dateAwareExtractEnabled reports whether the date-aware extract hint should be
// appended to the LLM extraction call.
//
// Default is TRUE — only the explicit strings "false" or "0" disable it. This
// mirrors the env-flag convention used elsewhere in handlers/ (see envBool in
// internal/config/config_env.go for the parser-based variant; we inline a
// minimal check here so add_fine_prompt.go has no cross-package dependency).
func dateAwareExtractEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(dateAwareExtractEnvVar)))
	switch v {
	case "false", "0":
		return false
	default:
		return true
	}
}

// dateAwareExtractHints returns the slice of hints to pass into ExtractAndDedup.
// Empty when the feature is disabled — the caller can safely splat the result
// into a variadic call regardless of state.
func dateAwareExtractHints() []string {
	if !dateAwareExtractEnabled() {
		return nil
	}
	return []string{dateAwareExtractHint}
}

// ── Observability counter ─────────────────────────────────────────────────────

// Outcome label values for memdb.add.date_aware_extract_total.
// The exact-three-strings-only contract is part of the M9 Stream 4 spec.
const (
	dateAwareExtractOutcomeEnabled  = "enabled"
	dateAwareExtractOutcomeDisabled = "disabled"
	dateAwareExtractOutcomeError    = "error"
)

var (
	dateAwareExtractMetricsOnce sync.Once
	dateAwareExtractMetrics     *dateAwareExtractInstruments
)

type dateAwareExtractInstruments struct {
	// Total counts fine-mode extraction attempts tagged by date-aware outcome.
	// outcome ∈ {enabled, disabled, error}.
	Total metric.Int64Counter
}

// dateAwareExtractMx returns the singleton date-aware extraction instruments,
// lazy-initialised. Counter memdb.add.date_aware_extract_total{outcome=...}.
func dateAwareExtractMx() *dateAwareExtractInstruments {
	dateAwareExtractMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/add")
		total, _ := meter.Int64Counter("memdb.add.date_aware_extract_total",
			metric.WithDescription("Count of fine-mode extraction calls by date-aware outcome (enabled/disabled/error)."),
		)
		dateAwareExtractMetrics = &dateAwareExtractInstruments{Total: total}
	})
	return dateAwareExtractMetrics
}

// recordDateAwareExtractOutcome bumps the counter with one of three outcomes.
// Pass an explicit `override` (e.g. "error") to force a label, or pass "" to
// have the helper derive enabled/disabled from the env flag. Exactly ONE
// counter increment per fine-mode extraction call.
func recordDateAwareExtractOutcome(ctx context.Context, override string) {
	outcome := override
	if outcome == "" {
		outcome = dateAwareExtractOutcomeDisabled
		if dateAwareExtractEnabled() {
			outcome = dateAwareExtractOutcomeEnabled
		}
	}
	dateAwareExtractMx().Total.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}
