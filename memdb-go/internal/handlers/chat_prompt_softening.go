package handlers

// chat_prompt_softening.go — M12.2: confidence-conditional factual-prompt
// routing. Holds the decision matrix that picks between the high-confidence
// and low-confidence factualQAPrompt variants based on the top retrieval
// cosine score. Split out of chat_prompt.go on the M12.2 PR to keep the
// builder file under the 200-line target — see CLAUDE.md "Architecture
// triggers". Tested by chat_prompt_softening_test.go.

import (
	"math"
	"os"
	"strconv"
)

// factualPromptVariant identifies which factualQAPrompt* template was used.
// Stable enum string (used as a metric label and debug-header value).
type factualPromptVariant string

const (
	factualVariantNone factualPromptVariant = "none" // not the factual branch
	factualVariantHigh factualPromptVariant = "high" // top score >= threshold
	factualVariantLow  factualPromptVariant = "low"  // memories present but all below threshold
	factualVariantZero factualPromptVariant = "zero" // no memories at all
)

// chatRefusalReason classifies WHY the prompt steered toward refusal. Used as
// the X-Memdb-Refusal-Reason debug header value and the {reason} label on
// memdb.chat.refusal_total. "none" means the high-confidence variant fired
// (no refusal bias) — it is still emitted so dashboards see the full picture.
type chatRefusalReason string

const (
	refusalReasonNone          chatRefusalReason = "none"
	refusalReasonNoMemories    chatRefusalReason = "no_memories"
	refusalReasonLowConfidence chatRefusalReason = "low_confidence"
	refusalReasonOther         chatRefusalReason = "other"
)

// factualPromptDecision captures the routing inputs and outputs for the
// factual branch so the caller can emit metrics / debug headers without
// re-deriving the threshold check.
//
// TopScore is the maximum metadata.relativity across `memories` (0 when the
// slice is empty). Variant is the chosen template family. Reason is the
// classification used for the X-Memdb-Refusal-Reason header. Threshold is
// the threshold value applied (env-overridable; copied here for observability).
//
// Components carries the per-term contributions of the multi-feature
// confidence formula (top1, spread, density, median, combined). Populated
// for both top1 and multifeature paths so operators can A/B without a code
// change. May be nil on legacy callers — the metric path treats nil as
// "skip per-component emission".
type factualPromptDecision struct {
	Variant    factualPromptVariant
	Reason     chatRefusalReason
	TopScore   float64
	Threshold  float64
	Components map[string]float64
}

// defaultFactualConfidenceThreshold is the lower bound on top-1
// metadata.relativity above which we trust retrieval enough to bias the
// model toward committing to an answer instead of replying "no answer".
//
// Default 0.5 picked from M11 RCA: 220/841 cat-4 wrong predictions had
// hit@k=1 (gold doc top-retrieved) AND model refused — those have median
// top-1 cosine well above 0.5. Value can be tuned via
// MEMDB_FACTUAL_CONFIDENCE_THRESHOLD ∈ [0, 1].
const defaultFactualConfidenceThreshold = 0.5

// factualConfidenceThreshold returns the env-overridable threshold used by
// decideFactualPrompt. Mirrors the parseEnvFloat pattern in search/tuning.go
// (kept local to avoid an import cycle: search → handlers is forbidden).
//
// Env: MEMDB_FACTUAL_CONFIDENCE_THRESHOLD in [0, 1]. Out-of-range / unparseable
// / NaN / Inf values fall back silently to the compile-time default.
func factualConfidenceThreshold() float64 {
	raw := os.Getenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD")
	if raw == "" {
		return defaultFactualConfidenceThreshold
	}
	v, err := strconv.ParseFloat(raw, 64)
	// Reject NaN / ±Inf explicitly: ParseFloat accepts "NaN" / "Inf" but
	// they propagate through `>=` comparisons in unhelpful ways
	// (NaN >= threshold is always false → would always pick the
	// low-confidence variant, which is technically safe but masks the
	// misconfiguration). Fall back to the default in those cases.
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return defaultFactualConfidenceThreshold
	}
	return v
}

// topRetrievalScore returns max(metadata.relativity) across memories, or 0
// when the slice is empty. Memories are typically pre-sorted by relativity
// (mergeCubeResults / sortByRelativity) but we don't rely on that — a
// linear scan is O(n) over <= top_k items, negligible cost.
func topRetrievalScore(memories []map[string]any) float64 {
	top := 0.0
	for _, m := range memories {
		if r := relativity(m); r > top {
			top = r
		}
	}
	return top
}

// decideFactualPrompt picks the factual prompt variant and classifies the
// refusal-bias reason for a given memory pool. Pure: no I/O beyond the env
// reads inside factualConfidenceThreshold() / confidenceFormulaFromEnv() /
// densityFloorFromEnv() / confidenceWeightsFromEnv().
//
// Decision matrix:
//
//	len(memories) == 0          → variant=zero, reason=no_memories
//	score >= threshold          → variant=high, reason=none
//	otherwise (score < threshold) → variant=low, reason=low_confidence
//
// `score` is either:
//   - max(metadata.relativity) — when MEMDB_FACTUAL_CONFIDENCE_FORMULA=top1
//     (legacy M12.2 behaviour, safe-rollback path).
//   - multi-feature combined score — when MEMDB_FACTUAL_CONFIDENCE_FORMULA=
//     multifeature (default). See chat_prompt_confidence.go.
//
// TopScore on the returned struct is ALWAYS the multi-feature combined score
// when the multi-feature formula is active, and the top-1 cosine when the
// legacy formula is active. This is what gets compared to threshold and
// what dashboards interpret as "the gate input".
//
// The "other" reason is reserved for future post-hoc classifiers (e.g. when
// the LLM returns "no answer" against a high-confidence prompt).
func decideFactualPrompt(memories []map[string]any) factualPromptDecision {
	threshold := factualConfidenceThreshold()
	if len(memories) == 0 {
		return factualPromptDecision{
			Variant:    factualVariantZero,
			Reason:     refusalReasonNoMemories,
			TopScore:   0,
			Threshold:  threshold,
			Components: map[string]float64{"top1": 0, "spread": 0, "density": 0, "median": 0, "combined": 0},
		}
	}

	// Compute the multi-feature components unconditionally — they are cheap
	// and let dashboards see the full feature vector even when the gate
	// uses the legacy top1 path. This is what makes the A/B comparison
	// possible without a code change.
	comps := computeRetrievalConfidenceWithComponents(memories)
	componentMap := map[string]float64{
		"top1":     comps.Top1,
		"spread":   comps.Spread,
		"density":  comps.Density,
		"median":   comps.Median,
		"combined": comps.Combined,
	}

	var score float64
	switch confidenceFormulaFromEnv() {
	case confidenceFormulaTop1:
		score = topRetrievalScore(memories)
	default:
		score = comps.Combined
	}

	if score >= threshold {
		return factualPromptDecision{
			Variant:    factualVariantHigh,
			Reason:     refusalReasonNone,
			TopScore:   score,
			Threshold:  threshold,
			Components: componentMap,
		}
	}
	return factualPromptDecision{
		Variant:    factualVariantLow,
		Reason:     refusalReasonLowConfidence,
		TopScore:   score,
		Threshold:  threshold,
		Components: componentMap,
	}
}

// pickFactualTemplate returns the prompt template for the given variant + lang.
// Falls back to the low-confidence variant on unknown variant values
// (defensive — variant is enum-typed so this should never hit).
func pickFactualTemplate(variant factualPromptVariant, lang string) string {
	if variant == factualVariantHigh {
		if lang == "zh" {
			return factualQAPromptHighConfidenceZH
		}
		return factualQAPromptHighConfidenceEN
	}
	// factualVariantLow / factualVariantZero / unknown → low-confidence.
	if lang == "zh" {
		return factualQAPromptLowConfidenceZH
	}
	return factualQAPromptLowConfidenceEN
}

// ── M12.4: anti-refusal rules for custom-prompt callers ─────────────────────
//
// The dual-speaker LoCoMo harness (and any other client passing a non-empty
// system_prompt + answer_style=factual) bypasses pickFactualTemplate entirely:
// the basePrompt branch in buildSystemPromptWithDecision wins, and the M12.2
// anti-refusal rules embedded in factualQAPromptHigh/LowConfidence* are
// LOST. Result on full-corpus LoCoMo eval before this fix:
// chat_refused_with_evidence = 954/1984 (48%) — the model refuses despite
// receiving relevant retrieval.
//
// Fix: when basePrompt != "" + answerStyle=factual, append a variant-marked
// "## Answer Rules:" block AFTER the caller's system prompt and BEFORE the
// memory section. The rule fragments below mirror the exact text found in
// factualQAPromptHigh/LowConfidenceEN (rules 1-10) and the variant-specific
// preamble that distinguishes high-confidence ("commit, do not refuse") from
// low/zero ("use any relevant context; refuse only when zero relevant info").
//
// Why a separate fragment instead of substring-extracting from the templates:
// the templates carry %s placeholders for time/memory that we cannot easily
// strip with a single pass. Defining the rule blocks separately keeps the
// custom-prompt path explicit and the natural factual branch byte-for-byte
// unchanged. When the natural branch fires (basePrompt == "") it continues
// to use pickFactualTemplate; the rules below are NOT injected on top of it
// (would double the rules — see buildSystemPromptWithDecision branch logic).
//
// The marker substrings (factualHighConfidenceMarker / factualLowConfidenceMarker
// in chat_prompt_factual_test.go) are reproduced verbatim so the same
// detection logic that flags variant-leak in the natural-branch tests also
// works on the injected block.

const factualRulesPreambleHighEN = "The retrieval system has high confidence that these memories contain the answer.\n**Commit to an answer based on the retrieved evidence; do not refuse.**"

const factualRulesPreambleLowEN = "The retrieval system has lower confidence in these memories, but they may still contain the answer.\n**Use any relevant context to answer; only refuse if the memories contain zero relevant information.**"

// factualRulesBodyHighEN — rules 1-10 of factualQAPromptHighConfidenceEN.
// Rule 6 is the high-confidence variant ("commit; reply 'no answer' only if
// every memory is unambiguously off-topic"). Keep this string in sync with
// chat_prompt_tpl.go::factualQAPromptHighConfidenceEN.
//
//nolint:lll // prompt rules are long by nature
const factualRulesBodyHighEN = `1. Reply with a concise but complete factual answer (usually 1-15 words). Include the entity AND the qualifying detail when both are present in the memories (e.g. "Yes, Caroline supports LGBTQ rights" not bare "Yes"; "a bookcase filled with DVDs and movies" not just "a bookcase").
2. Do NOT say "based on the memories", "it appears", "the user mentioned", or similar meta-framing.
3. For dates/times: give the most specific form present in the memories (e.g. "May 2023", "last summer", "Tuesday"). If the exact date is absent but inferable (e.g. "a few months ago" + known reference point), give your best estimate — do not refuse on grounds of approximation.
4. For names/entities, reply with the bare name when the question asks "who" (e.g. "Emma" not "Her sister Emma"); include the relationship when the question asks for it.
5. For yes/no questions: actively search for confirming OR denying evidence in every memory. If any memory implies an answer, reply "yes" or "no" followed by the supporting fact (e.g. "Yes, in March 2023"). Do NOT default to "not stated" — derive the answer from closest related context.
6. **Commit**: at least one memory above carries strong evidence for the question. Synthesize an answer even if the phrasing is approximate. Reply "no answer" only if every memory is unambiguously off-topic — NOT because the wording differs from the question.
7. Match the phrasing and register used in the memories themselves — do not paraphrase more than needed.
8. For counting questions (how many, how often, how much): count ALL distinct mentions across every memory, even if the total is not stated explicitly. Never base a count on a single memory when multiple memories contribute.
9. **Synthesize from evidence**: When the question asks about a property or trait and the memories show a related action or behaviour (not the trait stated directly), SYNTHESIZE the answer from that evidence. Do NOT add disclaimers like "memories do not explicitly state" — commit to your best-fit inference. Example: Q "Is X religious?" + memory "X made stained glass windows for a church" → answer "Somewhat religious" based on the evidence.
10. **Cross-character shared events**: When the question asks about person X but the memories reference a SHARED event involving person Y (accident, party, trip, conversation), facts about Y's experience often apply to X as witness, family, or co-participant. Do NOT refuse solely because the name in the memory differs from the name in the question. Example: Q "How did X feel after the accident?" + memory "Y's family was scared but resilient after the accident" → X as a family member shares that emotional scope; answer accordingly. When in doubt, prefer cross-applying shared-event facts over refusing.`

// factualRulesBodyLowEN — rules 1-10 of factualQAPromptLowConfidenceEN. Rule
// 6 is the low/zero-confidence variant ("provide best answer from any
// relevant memory; reply exactly: no answer — only when every memory is
// entirely unrelated"). Keep in sync with
// chat_prompt_tpl.go::factualQAPromptLowConfidenceEN.
//
//nolint:lll // prompt rules are long by nature
const factualRulesBodyLowEN = `1. Reply with a concise but complete factual answer (usually 1-15 words). Include the entity AND the qualifying detail when both are present in the memories.
2. Do NOT say "based on the memories", "it appears", "the user mentioned", or similar meta-framing.
3. For dates/times: give the most specific form present in the memories (e.g. "May 2023", "last summer", "Tuesday"). If the exact date is absent but inferable, give your best estimate rather than refusing.
4. For names/entities, reply with the bare name when the question asks "who" (e.g. "Emma" not "Her sister Emma").
5. For yes/no questions: actively search for confirming OR denying evidence in every memory. If any memory implies an answer, reply "yes" or "no" followed by the supporting fact. Do NOT default to "not stated" — derive the answer from closest related context.
6. Provide the best answer you can from any relevant memory, even if the match is partial or approximate. Reply exactly: no answer — only when every memory is entirely unrelated to the question.
7. Match the phrasing and register used in the memories themselves — do not paraphrase more than needed.
8. For counting questions (how many, how often, how much): count ALL distinct mentions across every memory, even if the total is not stated explicitly. Never base a count on a single memory when multiple memories contribute.
9. **Synthesize from evidence**: When the question asks about a property or trait and the memories show a related action or behaviour (not the trait stated directly), SYNTHESIZE the answer from that evidence. Do NOT add disclaimers like "memories do not explicitly state" — commit to your best-fit inference. Example: Q "Is X religious?" + memory "X made stained glass windows for a church" → answer "Somewhat religious" based on the evidence.
10. **Cross-character shared events**: When the question asks about person X but the memories reference a SHARED event involving person Y (accident, party, trip, conversation), facts about Y's experience often apply to X as witness, family, or co-participant. Do NOT refuse solely because the name in the memory differs from the name in the question. Example: Q "How did X feel after the accident?" + memory "Y's family was scared but resilient after the accident" → X as a family member shares that emotional scope; answer accordingly. When in doubt, prefer cross-applying shared-event facts over refusing.`

// buildFactualRulesBlock returns the variant-conditional anti-refusal block
// to inject after a custom system_prompt. Only English — Chinese custom
// prompts in dual-speaker harness are not in scope (LoCoMo is English-only).
// The block is wrapped in a "## Answer Rules:" header so the model treats it
// as a distinct directive section rather than free text.
//
// Variant routing:
//   - factualVariantHigh        → high-confidence preamble + high body (rule 6 = commit)
//   - factualVariantLow / Zero  → low-confidence preamble + low body (rule 6 = strict-but-conditional)
//
// Empty string for variant=none (caller checks answerStyle before calling).
func buildFactualRulesBlock(variant factualPromptVariant) string {
	preamble, body := factualRulesPreambleLowEN, factualRulesBodyLowEN
	if variant == factualVariantHigh {
		preamble, body = factualRulesPreambleHighEN, factualRulesBodyHighEN
	}
	return "## Answer Rules\n" + preamble + "\n\n" + body
}
