package handlers

// chat_prompt_external_memory_test.go — Karpathy r3 forensic fixes (2026-05-01).
//
// Verifies the three prompt-builder behaviours added to kill the conflicting
// refusal contracts that caused 50% of F1 regressions on Run #18 vs the
// brevity-pattB peak:
//
//   Fix #1 — When basePrompt is non-empty AND the variant resolved to Zero
//            AND ExternalMemoryCount > 0, the "## Answer Rules" block must
//            NOT be injected (the harness already supplied a brevity tail;
//            two refusal contracts cause the LLM to pick the strictest one).
//   Fix #1 back-compat — When basePrompt is empty (or ExternalMemoryCount
//            is 0), the rules block injection behaviour must be byte-for-byte
//            unchanged.
//   Fix #3 — When ExternalMemoryCount > 0 AND server memories pool is empty,
//            decideFactualPromptWithExternal must upgrade variant to High
//            (trust the harness, do not bias toward strict refusal).
//
// Pure unit tests — no Postgres, no LLM.

import (
	"strings"
	"testing"
)

// ── Fix #1: rules block skipped when external memories present + variant=Zero ──

// TestBuildSystemPrompt_ExternalMemories_NoRulesBlock verifies that when the
// caller supplied a custom basePrompt + answer_style=factual + signalled
// external memories via ExternalMemoryCount > 0, the strict refusal rules
// block is NOT appended on top of the harness's basePrompt. The
// decideFactualPromptWithExternal upgrade should bump the variant to High,
// AND shouldInjectFactualRules must defend against a Zero variant slipping
// through (defence in depth).
func TestBuildSystemPrompt_ExternalMemories_NoRulesBlock(t *testing.T) {
	basePrompt := "You are a factual QA assistant.\n## Memories\n[harness-supplied]\nAnswer briefly."
	prompt, decision := buildSystemPromptWithBudget(
		"What month did Caroline start her new job?",
		nil, // server pool empty (harness suppressed via top_k=1+threshold=0.99)
		"",
		basePrompt,
		"factual",
		"",
		0, // maxContextTokens
		20, // externalMemoryCount > 0 → harness signal
	)
	// Variant should be upgraded to High (Fix #3).
	if decision.Variant != factualVariantHigh {
		t.Errorf("variant = %q, want high (external pool present, upgrade expected)", decision.Variant)
	}
	// The strict refusal sentinel from factualRulesBodyLowEN must NOT appear:
	// it would conflict with the harness brevity tail.
	if strings.Contains(prompt, "Reply exactly: no answer") {
		t.Error("rules block leaked strict refusal contract into prompt despite ExternalMemoryCount > 0")
	}
	// Specifically, the low-variant preamble (which carries the strict
	// refusal contract) must NOT be appended.
	if strings.Contains(prompt, factualRulesPreambleLowEN) {
		t.Error("low-variant preamble injected on top of harness basePrompt — refusal contract conflict")
	}
}

// TestBuildSystemPrompt_ExternalMemories_HighVariantStillInjects verifies that
// when ExternalMemoryCount > 0 routes the variant to High (Fix #3), the
// high-confidence rules block IS still injected — high carries the
// "commit, do not refuse" preamble which reinforces the harness tail rather
// than conflicting with it.
func TestBuildSystemPrompt_ExternalMemories_HighVariantStillInjects(t *testing.T) {
	basePrompt := "You are a factual QA assistant.\n## Memories\n[harness-supplied]\nAnswer briefly."
	prompt, decision := buildSystemPromptWithBudget(
		"What month did Caroline start her new job?",
		nil,
		"",
		basePrompt,
		"factual",
		"",
		0,
		20,
	)
	if decision.Variant != factualVariantHigh {
		t.Fatalf("precondition: variant = %q, want high", decision.Variant)
	}
	// High-variant preamble carries the commit instruction — must be present.
	if !strings.Contains(prompt, factualRulesPreambleHighEN) {
		t.Error("high-variant preamble missing — commit instruction lost")
	}
}

// TestBuildSystemPrompt_NoExternalMemories_LegacyBehaviour verifies the
// back-compat path: when ExternalMemoryCount is 0 AND server pool is empty,
// the legacy "Zero variant + rules block injected" behaviour stays.
// Production callers (vaelor, oxpulse) that never set ExternalMemoryCount
// must see byte-identical output to pre-fix.
func TestBuildSystemPrompt_NoExternalMemories_LegacyBehaviour(t *testing.T) {
	basePrompt := "You are a factual QA assistant.\n## Memories\n{memories}\nAnswer briefly."
	prompt, decision := buildSystemPromptWithBudget(
		"What did the user say last Tuesday?",
		nil, // server pool empty, no external pool either
		"",
		basePrompt,
		"factual",
		"",
		0,
		0, // back-compat: legacy callers omit the field
	)
	if decision.Variant != factualVariantZero {
		t.Errorf("variant = %q, want zero (legacy zero-pool path)", decision.Variant)
	}
	// The low-variant rules block (which is what Zero falls into) must be
	// injected — preserves M12.4 anti-refusal behaviour for legacy clients.
	if !strings.Contains(prompt, factualRulesPreambleLowEN) {
		t.Error("legacy zero-pool path lost the low-variant preamble injection")
	}
}

// ── Fix #3: decideFactualPromptWithExternal upgrade ──────────────────────────

func TestDecideFactualPromptWithExternal_UpgradesVariant(t *testing.T) {
	d := decideFactualPromptWithExternal(nil, 15)
	if d.Variant != factualVariantHigh {
		t.Errorf("variant = %q, want high (external pool > 0 + empty server pool)", d.Variant)
	}
	if d.Reason != refusalReasonNone {
		t.Errorf("reason = %q, want none (no refusal bias when harness pool exists)", d.Reason)
	}
}

func TestDecideFactualPromptWithExternal_LegacyPath(t *testing.T) {
	// External count zero → identical to decideFactualPrompt.
	d := decideFactualPromptWithExternal(nil, 0)
	if d.Variant != factualVariantZero {
		t.Errorf("variant = %q, want zero (legacy: no external pool, no server pool)", d.Variant)
	}
	if d.Reason != refusalReasonNoMemories {
		t.Errorf("reason = %q, want no_memories", d.Reason)
	}
}

func TestDecideFactualPromptWithExternal_ServerPoolWins(t *testing.T) {
	// When server returned actual memories, score them — do not blindly
	// trust the external pool. Server-side evidence is authoritative when
	// it exists; the external-pool upgrade is ONLY a zero-pool safety net.
	memories := []map[string]any{
		memWithScore("weak hit", 0.10),
	}
	d := decideFactualPromptWithExternal(memories, 50)
	// With top1 formula at default threshold 0.5, 0.10 → low.
	if d.Variant == factualVariantHigh {
		t.Errorf("server pool with weak score must NOT be silently upgraded; variant = %q", d.Variant)
	}
}

// ── shouldInjectFactualRules: defence in depth ───────────────────────────────

func TestShouldInjectFactualRules_LegacyAlwaysInjects(t *testing.T) {
	// externalMemoryCount = 0 (legacy callers): inject for every variant.
	for _, v := range []factualPromptVariant{
		factualVariantHigh, factualVariantLow, factualVariantZero,
	} {
		if !shouldInjectFactualRules(v, 0) {
			t.Errorf("legacy path (external=0) variant=%q: shouldInject = false, want true", v)
		}
	}
}

func TestShouldInjectFactualRules_ExternalPoolSkipsZero(t *testing.T) {
	if shouldInjectFactualRules(factualVariantZero, 20) {
		t.Error("external pool present + variant=Zero: should SKIP injection (refusal-contract conflict)")
	}
	if !shouldInjectFactualRules(factualVariantHigh, 20) {
		t.Error("external pool present + variant=High: must still inject (commit preamble reinforces)")
	}
	if !shouldInjectFactualRules(factualVariantLow, 20) {
		t.Error("external pool present + variant=Low: must still inject (best-effort preamble OK)")
	}
}

// ── Sanity: buildSystemPromptWithBudget returns the decision struct ──────────

// TestBuildSystemPrompt_DecisionRoundTrip ensures the decision struct returned
// from the budget builder reflects the variant chosen — chat.go relies on this
// to emit the X-Memdb-Refusal-Reason debug header and metrics.
func TestBuildSystemPrompt_DecisionRoundTrip(t *testing.T) {
	_, decision := buildSystemPromptWithBudget(
		"any query",
		nil, "", "harness-prompt", "factual", "", 0, 5,
	)
	if decision.Variant != factualVariantHigh {
		t.Errorf("variant round-trip = %q, want high", decision.Variant)
	}
}
