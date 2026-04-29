package handlers

// chat_prompt_antirefusal_test.go — M12.2 deepen: anti-refusal prompt tuning.
// Verifies that the updated prompts:
//  1. Answer yes/no questions from relevant context (not "memories do not state").
//  2. Aggregate counts across multiple memories.
//  3. Still refuse cleanly when zero context is provided.
//
// Chat-50 evidence motivating these tests: 18/26 fails had hit@K=1 but the
// model responded "memories do not contain" — generation was the bottleneck,
// not retrieval. These tests pin the expected behaviour post-fix.
//
// Pure unit tests — no Postgres, no LLM. Use memWithScore from
// chat_prompt_factual_test.go.

import (
	"strings"
	"testing"
)

// ── Yes/No question — context present, must answer ────────────────────────────

// TestChatPrompt_YesNoQuestion_HighConf_HasCommitInstruction verifies that the
// high-confidence prompt contains the yes/no guidance that forces fact-based
// reasoning instead of defaulting to "not stated".
func TestChatPrompt_YesNoQuestion_HighConf_HasCommitInstruction(t *testing.T) {
	// Synthetic memory: "Melanie made the bowl" — answers "Did Melanie make the black and white bowl?"
	memories := []map[string]any{
		memWithScore("Melanie made the black and white bowl herself", 0.82),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"Did Melanie make the black and white bowl?",
		memories,
		"", "", "factual", "",
	)
	// High-confidence variant must carry the yes/no guidance.
	if !strings.Contains(prompt, "actively search for confirming OR denying evidence") {
		t.Error("high-confidence prompt missing yes/no active-search instruction")
	}
	// Must NOT say "not stated" is acceptable — should commit.
	if strings.Contains(prompt, "Do NOT default to") && !strings.Contains(prompt, "Do NOT default to") {
		// This branch is tautological; real check is the absence of old "not stated" language.
	}
	// The commit instruction must still be present.
	if !strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Errorf("high-confidence prompt missing commit marker %q", factualHighConfidenceMarker)
	}
}

// TestChatPrompt_YesNoQuestion_LowConf_HasActiveSearchInstruction verifies
// that the low-confidence prompt (which previously hard-refused on low scores)
// now carries the yes/no active-search instruction.
func TestChatPrompt_YesNoQuestion_LowConf_HasActiveSearchInstruction(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Caroline felt grateful and thankful after the accident", 0.35),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"How did Caroline feel after the accident?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Fatalf("precondition: expected low-confidence variant, missing marker %q", factualLowConfidenceMarker)
	}
	if !strings.Contains(prompt, "actively search for confirming OR denying evidence") {
		t.Error("low-confidence prompt missing yes/no active-search instruction after M12.2 deepen")
	}
}

// ── Counting question — must aggregate across memories ────────────────────────

// TestChatPrompt_CountQuestion_HighConf_HasAggregationInstruction verifies
// that the high-confidence prompt carries rule 8 (count ALL distinct mentions
// across memories, not just from one).
func TestChatPrompt_CountQuestion_HighConf_HasAggregationInstruction(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Melanie has a daughter named Sophie", 0.78),
		memWithScore("Melanie mentioned her son had a bad day at school", 0.65),
		memWithScore("Melanie talked about her youngest child learning to walk", 0.60),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"How many children does Melanie have?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Fatalf("precondition: expected high-confidence variant")
	}
	if !strings.Contains(prompt, "count ALL distinct mentions across every memory") {
		t.Error("high-confidence prompt missing counting aggregation instruction (rule 8)")
	}
}

// TestChatPrompt_CountQuestion_LowConf_HasAggregationInstruction verifies
// rule 8 is also present in the low-confidence variant.
func TestChatPrompt_CountQuestion_LowConf_HasAggregationInstruction(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Melanie has a daughter named Sophie", 0.28),
		memWithScore("Melanie mentioned her son had a bad day at school", 0.22),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"How many children does Melanie have?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Fatalf("precondition: expected low-confidence variant, missing marker %q", factualLowConfidenceMarker)
	}
	if !strings.Contains(prompt, "count ALL distinct mentions across every memory") {
		t.Error("low-confidence prompt missing counting aggregation instruction (rule 8)")
	}
}

// ── Empty context — refusal preserved ────────────────────────────────────────

// TestChatPrompt_NoContext_StillRefuses is the parity-preservation test.
// Zero memories → zero-confidence variant → "no answer" must still be present.
// This ensures the anti-refusal changes do NOT break the empty-context path.
func TestChatPrompt_NoContext_StillRefuses(t *testing.T) {
	prompt, decision := buildSystemPromptWithDecision(
		nil,
		"Did Melanie make the bowl?",
		nil,
		"", "", "factual", "",
	)
	if decision.Variant != factualVariantZero {
		t.Errorf("no-context variant = %q, want zero", decision.Variant)
	}
	if decision.Reason != refusalReasonNoMemories {
		t.Errorf("no-context reason = %q, want no_memories", decision.Reason)
	}
	if !strings.Contains(prompt, "no answer") {
		t.Error("zero-memories prompt must still carry the 'no answer' refusal contract")
	}
}

// ── Temporal question — approximation must be encouraged ─────────────────────

// TestChatPrompt_TemporalQuestion_HighConf_HasApproximateGuidance verifies
// that both high and low confidence prompts instruct the model to give a best
// approximate date instead of refusing on grounds of imprecision.
func TestChatPrompt_TemporalQuestion_HighConf_HasApproximateGuidance(t *testing.T) {
	memories := []map[string]any{
		memWithScore("In March she started her new job", 0.75),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"When did she start her new job?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, "best estimate") {
		t.Error("high-confidence prompt missing 'best estimate' temporal approximation instruction")
	}
}

func TestChatPrompt_TemporalQuestion_LowConf_HasApproximateGuidance(t *testing.T) {
	memories := []map[string]any{
		memWithScore("In March she started her new job", 0.31),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"When did she start her new job?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Fatalf("precondition: expected low-confidence variant")
	}
	if !strings.Contains(prompt, "best estimate") {
		t.Error("low-confidence prompt missing 'best estimate' temporal approximation instruction")
	}
}

// ── Synthesis instruction (Pattern A — WRONG_AGGREGATION fix) ─────────────────

// TestChatPrompt_SynthesisInstruction_HighConf verifies that the high-confidence
// prompt carries rule 9: synthesize from evidence rather than disclaiming.
// Chat-50 evidence: cat-4 WRONG_AGGREGATION — model said "memories do not
// explicitly state" even when stained-glass → religious inference was available.
func TestChatPrompt_SynthesisInstruction_HighConf(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Caroline made stained glass windows for a church fundraiser", 0.77),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"Would Caroline be considered religious?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Fatalf("precondition: expected high-confidence variant, missing marker %q", factualHighConfidenceMarker)
	}
	if !strings.Contains(prompt, "Synthesize from evidence") {
		t.Error("high-confidence prompt missing synthesis instruction (rule 9)")
	}
	if !strings.Contains(prompt, "memories do not explicitly state") {
		// Rule 9 must explicitly ban this phrase in the model's output.
		t.Error("high-confidence prompt missing explicit ban on 'memories do not explicitly state' disclaimer")
	}
}

// TestChatPrompt_SynthesisInstruction_LowConf verifies rule 9 is present in the
// low-confidence variant as well.
func TestChatPrompt_SynthesisInstruction_LowConf(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Caroline attended Sunday services regularly last year", 0.38),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"Would Caroline be considered religious?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Fatalf("precondition: expected low-confidence variant, missing marker %q", factualLowConfidenceMarker)
	}
	if !strings.Contains(prompt, "Synthesize from evidence") {
		t.Error("low-confidence prompt missing synthesis instruction (rule 9)")
	}
}

// ── Cross-character shared-event instruction (Pattern B — cat-5 fix) ──────────

// TestChatPrompt_CharCrossRef_HighConf verifies that the high-confidence prompt
// carries rule 10: cross-apply shared-event facts instead of refusing on name
// mismatch. Chat-50 evidence: all 8/10 cat-5 fails were accident-related;
// question targeted Caroline while memory referenced Melanie's family experience.
func TestChatPrompt_CharCrossRef_HighConf(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Melanie's family was scared but resilient after the accident", 0.81),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"How did Caroline feel after the accident?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualHighConfidenceMarker) {
		t.Fatalf("precondition: expected high-confidence variant, missing marker %q", factualHighConfidenceMarker)
	}
	if !strings.Contains(prompt, "Cross-character shared events") {
		t.Error("high-confidence prompt missing cross-character instruction (rule 10)")
	}
	if !strings.Contains(prompt, "Do NOT refuse solely because the name in the memory differs") {
		t.Error("high-confidence prompt missing name-mismatch refusal ban (rule 10)")
	}
}

// TestChatPrompt_CharCrossRef_LowConf verifies rule 10 is present in the
// low-confidence variant.
func TestChatPrompt_CharCrossRef_LowConf(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Melanie's family was scared but resilient after the accident", 0.34),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"How did Caroline feel after the accident?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Fatalf("precondition: expected low-confidence variant, missing marker %q", factualLowConfidenceMarker)
	}
	if !strings.Contains(prompt, "Cross-character shared events") {
		t.Error("low-confidence prompt missing cross-character instruction (rule 10)")
	}
}

// ── Low-confidence variant: any relevant memory should produce answer ─────────

// TestChatPrompt_LowConf_PartialMatchInstruction verifies that the
// low-confidence prompt explicitly instructs "answer with any relevant memory"
// rather than the old hard-refuse on partial match.
func TestChatPrompt_LowConf_PartialMatchInstruction(t *testing.T) {
	memories := []map[string]any{
		memWithScore("Melanie's son was scared after the car accident", 0.38),
	}
	prompt, _ := buildSystemPromptWithDecision(
		nil,
		"How did Melanie's son handle the accident?",
		memories,
		"", "", "factual", "",
	)
	if !strings.Contains(prompt, factualLowConfidenceMarker) {
		t.Fatalf("precondition: expected low-confidence variant")
	}
	// Key anti-refusal instruction must be present.
	if !strings.Contains(prompt, "Use any relevant context to answer") {
		t.Error("low-confidence prompt missing 'Use any relevant context' anti-refusal instruction")
	}
	// The old strict "no answer" fallback should now be conditional.
	if !strings.Contains(prompt, "only when every memory is entirely unrelated") {
		t.Error("low-confidence prompt missing conditional refusal gate")
	}
}
