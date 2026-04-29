package handlers

// chat_prompt_softening_test.go — M12.2: tests for the conditional refusal
// logic in the factual answer-style branch. Verifies that:
//
//  1. With high top retrieval score the high-confidence prompt fires (commit
//     bias, no "reply exactly: no answer" rule).
//  2. With low top score the low-confidence prompt fires (classic refusal).
//  3. With zero memories the low-confidence prompt fires + reason=no_memories.
//  4. The threshold is env-overridable via MEMDB_FACTUAL_CONFIDENCE_THRESHOLD.
//  5. The decision struct returned by buildSystemPromptWithDecision matches
//     reality (variant, reason, top score) — this is what feeds the metric +
//     debug header in chat.go::recordFactualPromptDecision.
//
// Pure unit tests — no Postgres, no LLM. Reference the same `memWithScore`
// helper defined in chat_prompt_factual_test.go.

import (
	"context"
	"strings"
	"testing"
)

// ── Decision-matrix tests ─────────────────────────────────────────────────────

func TestDecideFactualPrompt_NoMemories(t *testing.T) {
	d := decideFactualPrompt(nil)
	if d.Variant != factualVariantZero {
		t.Errorf("variant = %q, want zero", d.Variant)
	}
	if d.Reason != refusalReasonNoMemories {
		t.Errorf("reason = %q, want no_memories", d.Reason)
	}
	if d.TopScore != 0 {
		t.Errorf("top score = %v, want 0", d.TopScore)
	}
	// Empty slice must behave identically.
	d2 := decideFactualPrompt([]map[string]any{})
	if d2.Variant != factualVariantZero || d2.Reason != refusalReasonNoMemories {
		t.Errorf("empty-slice path: variant=%q reason=%q, want zero/no_memories", d2.Variant, d2.Reason)
	}
}

func TestDecideFactualPrompt_LowConfidence(t *testing.T) {
	memories := []map[string]any{
		memWithScore("weak hit", 0.30),
		memWithScore("weaker hit", 0.10),
	}
	d := decideFactualPrompt(memories)
	if d.Variant != factualVariantLow {
		t.Errorf("variant = %q, want low", d.Variant)
	}
	if d.Reason != refusalReasonLowConfidence {
		t.Errorf("reason = %q, want low_confidence", d.Reason)
	}
	if d.TopScore != 0.30 {
		t.Errorf("top score = %v, want 0.30", d.TopScore)
	}
}

func TestDecideFactualPrompt_HighConfidence(t *testing.T) {
	memories := []map[string]any{
		memWithScore("noisy", 0.30),
		memWithScore("strong hit", 0.85),
		memWithScore("medium", 0.45),
	}
	d := decideFactualPrompt(memories)
	if d.Variant != factualVariantHigh {
		t.Errorf("variant = %q, want high", d.Variant)
	}
	if d.Reason != refusalReasonNone {
		t.Errorf("reason = %q, want none", d.Reason)
	}
	if d.TopScore != 0.85 {
		t.Errorf("top score = %v, want 0.85 (max across memories)", d.TopScore)
	}
}

func TestDecideFactualPrompt_AtThresholdIsHigh(t *testing.T) {
	// Boundary: top == threshold MUST fire the high variant (>= comparison).
	// This protects benchmark calibration: a threshold tuned to a specific
	// percentile must include that percentile, not exclude it.
	memories := []map[string]any{
		memWithScore("right at default threshold", defaultFactualConfidenceThreshold),
	}
	d := decideFactualPrompt(memories)
	if d.Variant != factualVariantHigh {
		t.Errorf("variant = %q, want high (top==threshold must satisfy >=)", d.Variant)
	}
}

// ── Threshold-override tests ──────────────────────────────────────────────────

func TestFactualConfidenceThreshold_Default(t *testing.T) {
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", "")
	if got := factualConfidenceThreshold(); got != defaultFactualConfidenceThreshold {
		t.Errorf("unset env: threshold = %v, want %v", got, defaultFactualConfidenceThreshold)
	}
}

func TestFactualConfidenceThreshold_EnvOverride(t *testing.T) {
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", "0.7")
	if got := factualConfidenceThreshold(); got != 0.7 {
		t.Errorf("env override: threshold = %v, want 0.7", got)
	}
}

func TestFactualConfidenceThreshold_OutOfRange(t *testing.T) {
	for _, raw := range []string{"-0.1", "1.5", "abc", "NaN"} {
		t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", raw)
		if got := factualConfidenceThreshold(); got != defaultFactualConfidenceThreshold {
			t.Errorf("invalid env %q: threshold = %v, want default %v", raw, got, defaultFactualConfidenceThreshold)
		}
	}
}

func TestDecideFactualPrompt_RespectsEnvThreshold(t *testing.T) {
	// With threshold=0.9, top=0.85 should now be classified low-confidence.
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", "0.9")
	memories := []map[string]any{
		memWithScore("strong-but-not-strong-enough", 0.85),
	}
	d := decideFactualPrompt(memories)
	if d.Variant != factualVariantLow {
		t.Errorf("variant = %q, want low (top=0.85 < threshold=0.9)", d.Variant)
	}
	if d.Threshold != 0.9 {
		t.Errorf("decision.Threshold = %v, want 0.9", d.Threshold)
	}
}

// ── Prompt-routing tests (end-to-end through buildSystemPromptWithDecision) ──

func TestBuildSystemPrompt_FactualHighConfidenceVariant(t *testing.T) {
	memories := []map[string]any{
		memWithScore("User started Go in May 2023", 0.92),
	}
	prompt, decision := buildSystemPromptWithDecision(
		context.Background(),
		"When did the user start Go?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Errorf("high-confidence prompt missing commit marker %q", factualHighConfidenceMarker)
	}
	// Low-confidence-only marker MUST NOT appear in the high prompt.
	if strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Error("high-confidence prompt leaked the low-confidence hedging line")
	}
	if decision.Variant != factualVariantHigh {
		t.Errorf("decision.Variant = %q, want high", decision.Variant)
	}
	if decision.Reason != refusalReasonNone {
		t.Errorf("decision.Reason = %q, want none", decision.Reason)
	}
}

func TestBuildSystemPrompt_FactualLowConfidenceVariant(t *testing.T) {
	memories := []map[string]any{
		memWithScore("noisy near-match", 0.30),
	}
	prompt, decision := buildSystemPromptWithDecision(
		context.Background(),
		"What is the user's favourite colour?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Errorf("low-confidence prompt missing hedging marker %q", factualLowConfidenceMarker)
	}
	if strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Error("low-confidence prompt leaked the high-confidence commit line")
	}
	// Classic refusal contract preserved.
	if !strings.Contains(prompt, "no answer") {
		t.Error("low-confidence prompt must keep the 'no answer' refusal contract")
	}
	if decision.Variant != factualVariantLow {
		t.Errorf("decision.Variant = %q, want low", decision.Variant)
	}
	if decision.Reason != refusalReasonLowConfidence {
		t.Errorf("decision.Reason = %q, want low_confidence", decision.Reason)
	}
}

func TestBuildSystemPrompt_FactualZeroMemoriesVariant(t *testing.T) {
	prompt, decision := buildSystemPromptWithDecision(
		context.Background(),
		"What did the user say last week?",
		nil,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, "no answer") {
		t.Error("zero-memories prompt must carry the 'no answer' refusal rule")
	}
	if decision.Variant != factualVariantZero {
		t.Errorf("decision.Variant = %q, want zero", decision.Variant)
	}
	if decision.Reason != refusalReasonNoMemories {
		t.Errorf("decision.Reason = %q, want no_memories", decision.Reason)
	}
}

func TestBuildSystemPrompt_FactualConversationalNoDecision(t *testing.T) {
	// Conversational branch must produce variant=none, reason=none — the
	// X-Memdb-Refusal-Reason header should be skipped by chat handlers.
	prompt, decision := buildSystemPromptWithDecision(
		context.Background(),
		"hello",
		[]map[string]any{memWithScore("anything", 0.99)},
		"", "", "conversational", "",
	)
	if strings.Contains(prompt, factualSharedSignature) {
		t.Error("conversational branch must not render the factual template")
	}
	if decision.Variant != factualVariantNone {
		t.Errorf("conversational decision.Variant = %q, want none", decision.Variant)
	}
	if decision.Reason != refusalReasonNone {
		t.Errorf("conversational decision.Reason = %q, want none", decision.Reason)
	}
}

func TestBuildSystemPrompt_FactualCustomBasePreservesPrompt(t *testing.T) {
	// M12.4: when a caller supplies a custom system_prompt + factual answer
	// style (LoCoMo dual-speaker harness), the custom prompt MUST be preserved
	// verbatim AND the variant-marked anti-refusal rules block injected after
	// it. The decision struct is now populated so factual_<variant> metric
	// labels and X-Memdb-Refusal-Reason fire for custom-prompt callers too —
	// pre-fix this branch silently dropped both the decision and the rules,
	// causing 48% chat_refused_with_evidence on full-corpus eval.
	memories := []map[string]any{memWithScore("strong hit", 0.95)}
	prompt, decision := buildSystemPromptWithDecision(
		context.Background(),
		"anything",
		memories,
		"", "Custom system prompt.", "factual", "",
	)
	if !strings.Contains(prompt, "Custom system prompt.") {
		t.Errorf("prompt missing custom base: %q", prompt)
	}
	if decision.Variant != factualVariantHigh {
		t.Errorf("custom-base + factual + high-cosine: decision.Variant = %q, want high", decision.Variant)
	}
	if !strings.Contains(prompt, "## Answer Rules") {
		t.Error("custom-base + factual: missing injected '## Answer Rules' block")
	}
	if !strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Errorf("custom-base + factual + high: missing %q", factualHighConfidenceMarker)
	}
}

// TestBuildSystemPrompt_FactualCustomBaseRulesPerVariant verifies that the
// injected rule block reflects the chosen variant for custom-prompt callers.
// Mirrors the dual-speaker harness path: each (variant, body) row covers the
// invariants the M12.2 rules expose to the LLM.
func TestBuildSystemPrompt_FactualCustomBaseRulesPerVariant(t *testing.T) {
	cases := []struct {
		name        string
		memories    []map[string]any
		wantVariant factualPromptVariant
		mustContain []string
		mustAbsent  []string
	}{
		{
			name:        "high",
			memories:    []map[string]any{memWithScore("Caroline supports LGBTQ rights", 0.92)},
			wantVariant: factualVariantHigh,
			mustContain: []string{
				factualHighConfidenceMarker,           // "Commit to an answer based on the retrieved evidence"
				"actively search for confirming",      // rule 5
				"count ALL distinct mentions",         // rule 8
				"Synthesize from evidence",            // rule 9
				"Cross-character shared events",       // rule 10
				"Reply \"no answer\" only if every memory is unambiguously off-topic",
			},
			mustAbsent: []string{factualLowConfidenceMarker},
		},
		{
			name:        "low",
			memories:    []map[string]any{memWithScore("noisy near-match", 0.30)},
			wantVariant: factualVariantLow,
			mustContain: []string{
				"lower confidence in these memories", // factualLowConfidenceMarker substring
				"Use any relevant context to answer",
				"only when every memory is entirely unrelated",
				"Synthesize from evidence",
				"Cross-character shared events",
			},
			mustAbsent: []string{factualHighConfidenceMarker},
		},
		{
			name:        "zero",
			memories:    nil,
			wantVariant: factualVariantZero,
			mustContain: []string{
				"lower confidence in these memories", // zero falls back to low-EN body
				"no answer",                          // refusal contract preserved
			},
			mustAbsent: []string{factualHighConfidenceMarker},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prompt, decision := buildSystemPromptWithDecision(
				context.Background(),
				"unused query",
				c.memories,
				"", "DUAL-SPEAKER PROMPT BLOCK", "factual", "",
			)
			if decision.Variant != c.wantVariant {
				t.Errorf("variant = %q, want %q", decision.Variant, c.wantVariant)
			}
			if !strings.Contains(prompt, "DUAL-SPEAKER PROMPT BLOCK") {
				t.Error("custom prompt missing from rendered output")
			}
			if !strings.Contains(prompt, "## Answer Rules") {
				t.Error("rendered prompt missing '## Answer Rules' header")
			}
			for _, s := range c.mustContain {
				if !strings.Contains(prompt, s) {
					t.Errorf("rendered prompt missing required fragment %q", s)
				}
			}
			for _, s := range c.mustAbsent {
				if strings.Contains(prompt, s) {
					t.Errorf("rendered prompt leaked forbidden fragment %q", s)
				}
			}
		})
	}
}

// TestBuildSystemPrompt_CustomBaseConversationalNoRules verifies that the
// rules block is NOT injected when answerStyle is empty/conversational —
// preserves legacy behaviour for non-factual custom-prompt callers.
func TestBuildSystemPrompt_CustomBaseConversationalNoRules(t *testing.T) {
	memories := []map[string]any{memWithScore("anything", 0.99)}
	prompt, decision := buildSystemPromptWithDecision(
		context.Background(),
		"hello",
		memories,
		"", "Custom system prompt.", "conversational", "",
	)
	if strings.Contains(prompt, "## Answer Rules") {
		t.Error("conversational custom-base must NOT inject the factual rules block")
	}
	if decision.Variant != factualVariantNone {
		t.Errorf("decision.Variant = %q, want none for conversational branch", decision.Variant)
	}
}

// ── Top-retrieval-score helper ────────────────────────────────────────────────

func TestTopRetrievalScore(t *testing.T) {
	cases := []struct {
		name string
		mems []map[string]any
		want float64
	}{
		{"empty", nil, 0},
		{"single", []map[string]any{memWithScore("a", 0.42)}, 0.42},
		{"unsorted", []map[string]any{
			memWithScore("a", 0.30),
			memWithScore("b", 0.85),
			memWithScore("c", 0.50),
		}, 0.85},
		{"missing-metadata", []map[string]any{{"memory": "x"}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := topRetrievalScore(c.mems); got != c.want {
				t.Errorf("topRetrievalScore = %v, want %v", got, c.want)
			}
		})
	}
}
