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
type factualPromptDecision struct {
	Variant   factualPromptVariant
	Reason    chatRefusalReason
	TopScore  float64
	Threshold float64
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
// read inside factualConfidenceThreshold().
//
// Decision matrix:
//
//	len(memories) == 0          → variant=zero, reason=no_memories
//	top >= threshold            → variant=high, reason=none
//	otherwise (top < threshold) → variant=low,  reason=low_confidence
//
// The "other" reason is reserved for future post-hoc classifiers (e.g. when
// the LLM returns "no answer" against a high-confidence prompt).
func decideFactualPrompt(memories []map[string]any) factualPromptDecision {
	threshold := factualConfidenceThreshold()
	if len(memories) == 0 {
		return factualPromptDecision{
			Variant:   factualVariantZero,
			Reason:    refusalReasonNoMemories,
			TopScore:  0,
			Threshold: threshold,
		}
	}
	top := topRetrievalScore(memories)
	if top >= threshold {
		return factualPromptDecision{
			Variant:   factualVariantHigh,
			Reason:    refusalReasonNone,
			TopScore:  top,
			Threshold: threshold,
		}
	}
	return factualPromptDecision{
		Variant:   factualVariantLow,
		Reason:    refusalReasonLowConfidence,
		TopScore:  top,
		Threshold: threshold,
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
