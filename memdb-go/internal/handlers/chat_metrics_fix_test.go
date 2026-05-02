package handlers

// chat_metrics_fix_test.go — unit tests for the three dead-metric emit-site
// fixes (Task #106 / PR feat/chat-metrics-fix):
//
//  1. TestChatPromptTemplateMetric_Factual — promptTemplateLabel returns
//     granular factual_high/factual_low/factual_zero labels (not the
//     coarse "factual" that was missing from /metrics).
//
//  2. TestChatRefusalMetric_DetectsRefusalPhrases — IsRefusalAnswer matches the
//     common LLM refusal phrases observed in M12 LoCoMo runs, and returns false
//     for substantive answers. Confirms the detection predicate used by
//     RecordChatRefusedWithEvidence is correct.
//
//  3. TestChatTop1CosineMetric_RecordsActualScore — chatTopRelativity extracts
//     the correct score from the memory map and RecordChatTop1Cosine is wired
//     to a non-zero argument (not the pre-registration zero).
//
// These are pure unit tests — no Postgres, no LLM, no OTel provider needed.
// The metric recording helpers are verified via input/output, not by scraping
// Prometheus output (that is covered by observability.TestM12MetricsExportWireNames).

import (
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// ── Fix 1: chat_prompt_template_used_total granular labels ────────────────────

// TestChatPromptTemplateMetric_Factual verifies that promptTemplateLabel emits
// the granular factual_{high,low,zero} labels when answerStyle=factual.
// Previously the function returned the coarse "factual" label regardless of
// which variant fired — the granular series were never created in Prometheus.
func TestChatPromptTemplateMetric_Factual(t *testing.T) {
	t.Parallel()

	type tc struct {
		name       string
		basePrompt string
		style      string
		decision   factualPromptDecision
		wantLabel  string
	}

	cases := []tc{
		{
			name:      "factual_high_confidence",
			style:     answerStyleFactual,
			decision:  factualPromptDecision{Variant: factualVariantHigh, Reason: refusalReasonNone, TopScore: 0.85},
			wantLabel: "factual_high",
		},
		{
			name:      "factual_low_confidence",
			style:     answerStyleFactual,
			decision:  factualPromptDecision{Variant: factualVariantLow, Reason: refusalReasonLowConfidence, TopScore: 0.30},
			wantLabel: "factual_low",
		},
		{
			name:      "factual_zero_memories",
			style:     answerStyleFactual,
			decision:  factualPromptDecision{Variant: factualVariantZero, Reason: refusalReasonNoMemories, TopScore: 0},
			wantLabel: "factual_zero",
		},
		{
			name:      "conversational_default",
			style:     "",
			decision:  factualPromptDecision{Variant: factualVariantNone},
			wantLabel: "conversational",
		},
		{
			name:      "conversational_explicit",
			style:     answerStyleConversational,
			decision:  factualPromptDecision{Variant: factualVariantNone},
			wantLabel: "conversational",
		},
		{
			name:       "custom_base_prompt_wins",
			basePrompt: "You are a helpful assistant.",
			style:      answerStyleFactual,
			decision:   factualPromptDecision{Variant: factualVariantHigh},
			wantLabel:  "custom",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := promptTemplateLabel(tt.basePrompt, tt.style, tt.decision)
			if got != tt.wantLabel {
				t.Errorf("promptTemplateLabel(%q, %q, variant=%q) = %q, want %q",
					tt.basePrompt, tt.style, tt.decision.Variant, got, tt.wantLabel)
			}
		})
	}
}

// TestChatPromptStyleLabel verifies the orthogonal `style` attribute
// (added 2026-05-01) preserves answer_style signal even on the custom
// branch where template collapses to "custom" by basePrompt-wins precedence.
// Without this attribute the LoCoMo dual-speaker harness path (basePrompt!="",
// answerStyle="factual") was emitting only template=custom — factual signal
// was invisible in /metrics.
func TestChatPromptStyleLabel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		style     string
		wantLabel string
	}{
		{"factual", answerStyleFactual, "factual"},
		{"conversational", answerStyleConversational, "conversational"},
		{"empty_style_maps_to_none", "", "none"},
		{"unknown_style_maps_to_none", "weird", "none"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := promptStyleLabel(tt.style)
			if got != tt.wantLabel {
				t.Errorf("promptStyleLabel(%q) = %q, want %q", tt.style, got, tt.wantLabel)
			}
		})
	}
}

// TestChatPromptTemplateMetric_AllTemplatesPreRegistered confirms that the
// chatPromptTemplateLabels slice contains all labels that promptTemplateLabel
// can return — these must stay in sync for the pre-registration loop
// (chatPromptMx) to cover every series dashboards/alerts reference.
func TestChatPromptTemplateMetric_AllTemplatesPreRegistered(t *testing.T) {
	t.Parallel()
	// The set of labels promptTemplateLabel can return.
	possible := map[string]bool{
		"factual_high":   true,
		"factual_low":    true,
		"factual_zero":   true,
		"conversational": true,
		"custom":         true,
		// Defensive fallback for unknown variant on factual path.
		answerStyleFactual: true,
	}
	for _, label := range chatPromptTemplateLabels {
		if !possible[label] {
			t.Errorf("chatPromptTemplateLabels contains %q which is not a valid label from promptTemplateLabel", label)
		}
	}
	// Canonical labels (all except the defensive fallback) must be pre-registered.
	canonical := []string{"factual_high", "factual_low", "factual_zero", "conversational", "custom"}
	registered := make(map[string]bool, len(chatPromptTemplateLabels))
	for _, l := range chatPromptTemplateLabels {
		registered[l] = true
	}
	for _, want := range canonical {
		if !registered[want] {
			t.Errorf("chatPromptTemplateLabels missing canonical label %q — Prometheus will not see the series from startup", want)
		}
	}
}

// ── Fix 2: chat_refused_with_evidence_total detection ─────────────────────────

// TestChatRefusalMetric_DetectsRefusalPhrases verifies IsRefusalAnswer against
// phrases that appear in real M12 LoCoMo "memories do not contain" failures.
// If detection returns false, RecordChatRefusedWithEvidence skips the counter
// even when hitCount > 0 — the counter stays stuck at 0.
func TestChatRefusalMetric_DetectsRefusalPhrases(t *testing.T) {
	t.Parallel()

	type tc struct {
		name    string
		answer  string
		refusal bool
	}
	cases := []tc{
		// Phrases from the task description — "memories do not contain" pattern.
		{"do not contain", "The memories do not contain that information.", true},
		{"do not contain lowercase", "the memories do not contain any relevant data.", true},
		{"do not state", "The memories do not state when she left.", true},
		{"do not mention", "The provided memories do not mention his name.", true},
		{"do not specify", "Memories do not specify the exact date.", true},
		{"do not provide", "The memories do not provide an answer.", true},
		{"no information", "The memories provided have no information on that topic.", true},
		// Bare refusals.
		{"no answer bare", "no answer", true},
		{"no answer period", "no answer.", true},
		{"idk", "I don't know", true},
		{"idk formal", "I do not know", true},
		{"unknown", "unknown", true},
		{"na", "n/a", true},
		// Substantive answers — must NOT trigger the counter.
		{"short factual", "Emma", false},
		{"date answer", "May 2023", false},
		{"yes", "yes", false},
		{"no (negation answer)", "no", false},
		{"negation in answer", "She does not own a dog.", false},
		{"complex with but", "Marcus moved in 2019, but the exact month isn't stated.", false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := observability.IsRefusalAnswer(tt.answer)
			if got != tt.refusal {
				t.Errorf("IsRefusalAnswer(%q) = %v, want %v", tt.answer, got, tt.refusal)
			}
		})
	}
}

// TestChatRefusalMetric_FiresWhenMemoriesNonEmpty verifies the guard logic
// inside RecordChatRefusedWithEvidence: the counter increments only when
// hitCount > 0 (evidence was present) AND the answer is a refusal.
// This test uses a no-op OTel provider (the global default); it checks that the
// function does not panic and returns without error in all branches.
func TestChatRefusalMetric_FiresWhenMemoriesNonEmpty(t *testing.T) {
	t.Parallel()

	// We can't easily assert the counter value without wiring a full OTel
	// provider — that is the job of m12_smoke_test.go. Here we verify:
	// (a) no panic when hitCount=0 (skip path), and
	// (b) no panic when hitCount>0 + refusal phrase (increment path).

	ctx := t.Context()

	// hitCount=0 → should be a no-op (no panic).
	observability.RecordChatRefusedWithEvidence(ctx, "The memories do not contain that.", 0, "", "factual")

	// hitCount>0 + non-refusal → no-op.
	observability.RecordChatRefusedWithEvidence(ctx, "Emma", 5, "", "factual")

	// hitCount>0 + refusal phrase → increment (no panic; actual counter value
	// verified in observability.TestM12MetricsExportWireNames).
	observability.RecordChatRefusedWithEvidence(ctx, "The memories do not contain that.", 5, "", "factual")
	observability.RecordChatRefusedWithEvidence(ctx, "The memories do not contain that.", 3, "1", "conversational")
}

// ── Fix 3: chat_top1_cosine_score records actual score ────────────────────────

// TestChatTop1CosineMetric_RecordsActualScore verifies that chatTopRelativity
// extracts a non-zero score from a memory map with populated metadata.relativity
// — confirming that RecordChatTop1Cosine is called with a real score, not the
// hardcoded 0 from the pre-registration.
func TestChatTop1CosineMetric_RecordsActualScore(t *testing.T) {
	t.Parallel()

	// Memories with populated metadata.relativity (the shape returned by
	// search/helpers.go::FormatMemoryItem).
	memories := []map[string]any{
		memWithScore("User started Go in May 2023", 0.92),
		memWithScore("User prefers short answers", 0.75),
	}
	score := chatTopRelativity(memories)
	if score != 0.92 {
		t.Errorf("chatTopRelativity = %v, want 0.92 (top-1 score)", score)
	}

	// Empty memories → 0 (the pre-registration value; still a valid record).
	emptyScore := chatTopRelativity(nil)
	if emptyScore != 0 {
		t.Errorf("chatTopRelativity(nil) = %v, want 0", emptyScore)
	}

	// Memories without metadata field → 0 (graceful fallback, no panic).
	noMeta := []map[string]any{{"memory": "some text", "id": "abc"}}
	noMetaScore := chatTopRelativity(noMeta)
	if noMetaScore != 0 {
		t.Errorf("chatTopRelativity(no-meta) = %v, want 0", noMetaScore)
	}

	// Verify RecordChatTop1Cosine doesn't panic with a real score.
	// (OTel global is the no-op default here; actual histogram verified in smoke test.)
	observability.RecordChatTop1Cosine(t.Context(), score)
	observability.RecordChatTop1Cosine(t.Context(), 0)
}
