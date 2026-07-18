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
	"math"
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
	// Legacy top1 formula: TopScore == max(metadata.relativity).
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA", "top1")
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
	// Legacy top1 formula: TopScore == max(metadata.relativity).
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA", "top1")
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
	// Boundary: score == threshold MUST fire the high variant (>= comparison).
	// This protects benchmark calibration: a threshold tuned to a specific
	// percentile must include that percentile, not exclude it. Pinned to the
	// legacy top1 formula so the boundary value (top1 == threshold) is the
	// thing being compared — under multi-feature the combined score depends
	// on density/median/spread and a single-memory pool would fall short.
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA", "top1")
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
				factualHighConfidenceMarker,      // "Commit to an answer based on the retrieved evidence"
				"actively search for confirming", // rule 5
				"count ALL distinct mentions",    // rule 8
				"Synthesize from evidence",       // rule 9
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

// ── Multi-feature confidence-formula tests ────────────────────────────────────

// TestComputeRetrievalConfidence_TopOnlyDistribution — single strong leader at
// 0.6 with tail at 0.05. The top1-only path returns 0.6; the multi-feature
// path should also score reasonably (top1 dominates the formula by weight)
// but density and median pull it slightly down vs a uniformly-strong pool.
// The bound check guards the [0, 1] invariant, the lower bound asserts the
// formula does not throw away the top1 signal.
func TestComputeRetrievalConfidence_TopOnlyDistribution(t *testing.T) {
	mems := []map[string]any{
		memWithScore("leader", 0.60),
		memWithScore("noise", 0.05),
		memWithScore("noise", 0.05),
		memWithScore("noise", 0.05),
	}
	got := computeRetrievalConfidence(mems)
	if got < 0 || got > 1 {
		t.Fatalf("confidence = %v, want in [0, 1]", got)
	}
	// Lower bound — the formula must reflect top1 even when density is weak.
	// w_top1 * 0.60 = 0.33; w_spread * spread (>0) > 0; so combined > 0.30.
	if got < 0.30 {
		t.Errorf("confidence = %v, want > 0.30 (single strong leader is still informative)", got)
	}
}

// TestComputeRetrievalConfidence_TightCluster — five memories at 0.50–0.46
// (typical "many corroborators" signal). The multi-feature formula must
// score this pool HIGHER than a single-memory pool with the same top1=0.50:
// density and median both reward corroboration where the top1-only gate is
// blind. We deliberately compare against a SINGLE-leader pool (not a
// huge-spread pool), because the default weights (top1=0.55, spread=0.20)
// can let an uncontested leader dominate via the spread term — that is by
// design (a clear winner is also a strong signal). The headline win for
// multi-feature is "richer than naive top1, not richer than every alternative".
func TestComputeRetrievalConfidence_TightCluster(t *testing.T) {
	tight := []map[string]any{
		memWithScore("a", 0.50),
		memWithScore("b", 0.49),
		memWithScore("c", 0.48),
		memWithScore("d", 0.47),
		memWithScore("e", 0.46),
	}
	// Single memory at top1=0.50 — same top1, no corroborators, no spread
	// signal (top2=0). This isolates the contribution of density+median.
	soloSameTop1 := []map[string]any{
		memWithScore("solo", 0.50),
	}
	gotTight := computeRetrievalConfidence(tight)
	gotSolo := computeRetrievalConfidence(soloSameTop1)
	if gotTight < 0 || gotTight > 1 {
		t.Fatalf("tight-cluster confidence = %v, want in [0, 1]", gotTight)
	}
	if gotSolo < 0 || gotSolo > 1 {
		t.Fatalf("solo confidence = %v, want in [0, 1]", gotSolo)
	}
	// At the same top1=0.50, the corroborated pool MUST score higher than
	// the single-memory pool: density (5 above floor vs 1) and median
	// (0.48 vs 0.50 — close, but density dominates the gap) reward the
	// cluster. Spread is similar between the two (1 memory → top2=0; cluster
	// → small gap), so the asymmetry comes from density + median.
	if gotTight <= gotSolo {
		t.Errorf("tight-cluster confidence (%v) must exceed single-memory confidence at same top1 (%v) — density should reward corroboration", gotTight, gotSolo)
	}
}

// TestComputeRetrievalConfidence_LowSpread — two memories at 0.51 and 0.50
// (gap = 0.01). The spread term should be approximately (0.01 / 0.2) = 0.05,
// reflecting a contested leader. We assert the components individually so the
// regression is loud if the normaliser changes.
func TestComputeRetrievalConfidence_LowSpread(t *testing.T) {
	mems := []map[string]any{
		memWithScore("a", 0.51),
		memWithScore("b", 0.50),
	}
	c := computeRetrievalConfidenceWithComponents(mems)
	if c.Top1 != 0.51 {
		t.Errorf("top1 = %v, want 0.51", c.Top1)
	}
	wantSpread := 0.05
	if math.Abs(c.Spread-wantSpread) > 1e-9 {
		t.Errorf("spread = %v, want ~%v (gap=0.01, normaliser=%v)", c.Spread, wantSpread, spreadNormaliser)
	}
	if c.Combined < 0 || c.Combined > 1 {
		t.Errorf("combined = %v, out of [0, 1]", c.Combined)
	}
}

// TestComputeRetrievalConfidence_AllBelowFloor — every score below density
// floor. count_above_min=0 → density=sigmoid(-2)≈0.119 (residual floor; we
// keep some signal so a completely-below-floor pool can still tip High when
// top1 + spread + median compensate). The point of this test is to LOCK the
// "all below floor" behaviour: density is small but non-zero, and the term
// stays in [0, 1].
func TestComputeRetrievalConfidence_AllBelowFloor(t *testing.T) {
	t.Setenv("MEMDB_FACTUAL_DENSITY_FLOOR", "0.30")
	mems := []map[string]any{
		memWithScore("a", 0.20),
		memWithScore("b", 0.15),
		memWithScore("c", 0.05),
	}
	c := computeRetrievalConfidenceWithComponents(mems)
	// Density = sigmoid(0-2) ≈ 0.119 — small residual floor by design.
	if c.Density >= 0.5 {
		t.Errorf("density = %v, want < 0.5 when no memory is above floor", c.Density)
	}
	if c.Density < 0 || c.Density > 1 {
		t.Errorf("density = %v, out of [0, 1]", c.Density)
	}
	if c.Combined > 0.5 {
		t.Errorf("combined = %v, want low (top1=0.20 with no density support)", c.Combined)
	}
}

// TestComputeRetrievalConfidence_EmptyMemories — guard the trivial path. The
// empty-pool case is expected to return 0 across the board so decideFactualPrompt
// never accidentally fires the high variant on a missing pool.
func TestComputeRetrievalConfidence_EmptyMemories(t *testing.T) {
	if got := computeRetrievalConfidence(nil); got != 0 {
		t.Errorf("nil memories: confidence = %v, want 0", got)
	}
	if got := computeRetrievalConfidence([]map[string]any{}); got != 0 {
		t.Errorf("empty memories: confidence = %v, want 0", got)
	}
	c := computeRetrievalConfidenceWithComponents(nil)
	if c.Top1 != 0 || c.Spread != 0 || c.Density != 0 || c.Median != 0 || c.Combined != 0 {
		t.Errorf("empty pool components = %+v, want all zero", c)
	}
}

// TestDecideFactualPrompt_LegacyFormula verifies that
// MEMDB_FACTUAL_CONFIDENCE_FORMULA=top1 produces byte-identical pre-PR
// behaviour (TopScore == top-1 cosine, comparison against the threshold).
// This is the safe-rollback path: operators can flip back to legacy without
// a code change if multi-feature regresses.
func TestDecideFactualPrompt_LegacyFormula(t *testing.T) {
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA", "top1")
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", "0.50")
	mems := []map[string]any{
		memWithScore("a", 0.50),
		memWithScore("b", 0.49),
		memWithScore("c", 0.48),
		memWithScore("d", 0.47),
		memWithScore("e", 0.46),
	}
	d := decideFactualPrompt(mems)
	if d.TopScore != 0.50 {
		t.Errorf("legacy formula: TopScore = %v, want 0.50 (top-1 cosine)", d.TopScore)
	}
	if d.Variant != factualVariantHigh {
		t.Errorf("legacy formula: variant = %q, want high (top1=0.50 == threshold)", d.Variant)
	}
}

// TestDecideFactualPrompt_MultiFeatureRoute asserts that the SAME memory pool
// gets routed Low under the legacy top1 formula (threshold barely exceeds
// the leader cosine) but High under multi-feature (density+spread+median
// push the combined score across). This is the headline reason the new
// formula exists.
//
// Numbers chosen so the maths is easy to follow (default weights 0.55 / 0.20
// / 0.15 / 0.10, density floor 0.30, spread normaliser 0.2):
//
//	memories  : top1=0.45, five tail at 0.40
//	threshold : 0.46
//	top1 path : 0.45 < 0.46  → Low
//	mf path   : 0.55*0.45 + 0.20*((0.45-0.40)/0.2) + 0.15*σ(6-2) + 0.10*0.40
//	          = 0.2475    + 0.0500                + 0.1473      + 0.0400
//	          ≈ 0.4848    >= 0.46                → High
func TestDecideFactualPrompt_MultiFeatureRoute(t *testing.T) {
	mems := []map[string]any{
		memWithScore("a", 0.45),
		memWithScore("b", 0.40),
		memWithScore("c", 0.40),
		memWithScore("d", 0.40),
		memWithScore("e", 0.40),
		memWithScore("f", 0.40),
	}
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", "0.46")

	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA", "top1")
	if d := decideFactualPrompt(mems); d.Variant != factualVariantLow {
		t.Errorf("top1 formula: variant = %q, want low (top1=0.45 < 0.46)", d.Variant)
	}

	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA", "multifeature")
	d := decideFactualPrompt(mems)
	if d.Variant != factualVariantHigh {
		t.Errorf("multifeature formula: variant = %q, want high (combined ≈ 0.48 > 0.46)", d.Variant)
	}
	if d.TopScore < 0.46 {
		t.Errorf("multifeature formula: TopScore = %v, want >= 0.46 (combined exceeds threshold)", d.TopScore)
	}
	if d.Components == nil {
		t.Fatal("decision.Components should be populated under multifeature formula")
	}
	if d.Components["combined"] != d.TopScore {
		t.Errorf("Components[combined] = %v, expected to equal decision.TopScore = %v", d.Components["combined"], d.TopScore)
	}
}

// TestParseConfidenceWeights_Malformed verifies that a malformed weights
// string falls back to defaults wholesale (no partial overrides). We test
// every kind of malformation: missing value, missing key, non-numeric value,
// negative weight, leading "=", trailing "=".
func TestParseConfidenceWeights_Malformed(t *testing.T) {
	cases := []string{
		"top1=foo",     // non-numeric
		"top1=",        // missing value
		"=0.5",         // missing key
		"top1=-0.1",    // negative weight rejected
		"top1=NaN",     // NaN rejected
		"top1=Inf",     // Inf rejected
		"no-equals",    // missing '='
		"top1=0.5,bad", // second segment malformed
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("MEMDB_FACTUAL_CONFIDENCE_WEIGHTS", raw)
			w := confidenceWeightsFromEnv()
			if w != defaultConfidenceWeights {
				t.Errorf("malformed weights %q: w = %+v, want defaults %+v", raw, w, defaultConfidenceWeights)
			}
		})
	}
}

// TestParseConfidenceWeights_Valid exercises the happy path so the malformed
// suite has a positive control: a well-formed string MUST override defaults
// rather than silently fall back. Asserts every recognised key.
func TestParseConfidenceWeights_Valid(t *testing.T) {
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_WEIGHTS", "top1=0.4,spread=0.3,density=0.2,median=0.1")
	w := confidenceWeightsFromEnv()
	want := confidenceWeights{Top1: 0.4, Spread: 0.3, Density: 0.2, Median: 0.1}
	if w != want {
		t.Errorf("parsed weights = %+v, want %+v", w, want)
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

// ── Cross-character bleed toggle tests ────────────────────────────────────────

// TestBuildFactualRulesBlock_CrossCharBleedDefault verifies that rule #10 is
// absent when MEMDB_CROSS_CHAR_BLEED is not set (safe for production).
func TestBuildFactualRulesBlock_CrossCharBleedDefault(t *testing.T) {
	saved := crossCharBleedEnabled
	crossCharBleedEnabled = false
	defer func() { crossCharBleedEnabled = saved }()

	for _, v := range []factualPromptVariant{factualVariantHigh, factualVariantLow} {
		block := buildFactualRulesBlock(v)
		if strings.Contains(block, "Cross-character shared events") {
			t.Errorf("variant %q: rule 10 present without MEMDB_CROSS_CHAR_BLEED", v)
		}
	}
}

// TestBuildFactualRulesBlock_CrossCharBleedEnabled verifies that rule #10 is
// appended when the feature flag is on (LoCoMo eval harness).
func TestBuildFactualRulesBlock_CrossCharBleedEnabled(t *testing.T) {
	saved := crossCharBleedEnabled
	crossCharBleedEnabled = true
	defer func() { crossCharBleedEnabled = saved }()

	for _, v := range []factualPromptVariant{factualVariantHigh, factualVariantLow} {
		block := buildFactualRulesBlock(v)
		if !strings.Contains(block, "Cross-character shared events") {
			t.Errorf("variant %q: rule 10 missing with MEMDB_CROSS_CHAR_BLEED=true", v)
		}
	}
}
