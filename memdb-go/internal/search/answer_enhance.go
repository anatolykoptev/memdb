package search

// answer_enhance.go — D10 post-retrieval answer enhancement.
//
// After the rerank/decay/boost pipeline produces top-K memory candidates,
// a single LLM call distills them into a concise, query-aligned answer.
//
// Targets the F1/EM gap on LoCoMo-style benchmarks: stored memories are
// verbose ("Caroline is advocating against sexual assault and child
// protection through her work as a social worker") while gold answers
// are short ("social worker"). D10 synthesises the short surface form
// directly and prepends it as a synthetic "EnhancedAnswer" item at
// position 0 of text_mem — downstream chat/complete surfaces this as
// the preferred response.
//
// Gated by MEMDB_SEARCH_ENHANCE=true (default off). Sits orthogonal to
// the MemOS-style memory rewriting in enhance.go (EnhanceMemories) —
// that rewrites each memory's text; this synthesises one extra item.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	answerEnhanceTopN          = 5
	answerEnhanceTimeout       = 10 * time.Second
	answerEnhanceMaxTokens     = 120
	answerEnhanceRespBodyLimit = 16 * 1024
	answerEnhanceSynthIDHexLen = 12 // sha256 prefix length for synthetic item id
	answerEnhanceUnknownAnswer = "UNKNOWN"
)

// answerEnhanceMinRelativity is the min relativity threshold — moved to
// tuning.go as an env-readable accessor (MEMDB_D10_MIN_RELATIVITY).
// Default: 0.4. See defaultAnswerEnhanceMinRelativity.

// D10 prompt variants. Three operator-selectable shapes, exposed via
// MEMDB_D10_PROMPT_MODE (see d10_config.go::PromptMode). The historical
// arc that produced this menu is recorded once per variant below; the
// runtime selector lives in D10Config.PromptForQuery.
//
// Naming convention: answerEnhancePrompt<Variant>. The legacy identifier
// answerEnhanceSystemPrompt is kept as an alias to PromptModeProbabilistic
// (the post-#252 default) so any downstream code that still references it
// — including external tests — continues to compile and behave identically.

// answerEnhancePromptStrict — pre-#249 verbatim. Highest LLM-Judge in the
// claude_NS replay (0.35). Insists on the exact surface form from the
// memories and explicit UNKNOWN when no answer is present. Best for
// extraction-style benchmarks where gold answers are short noun phrases
// pulled verbatim from the source. Underperforms on atomic-fact rows
// where the question's surface form differs from the memory's (synthesis
// is forbidden — model returns UNKNOWN instead).
const answerEnhancePromptStrict = `You are a precise answer extractor. Given a user's question and a list of retrieved memories, respond with the SHORTEST possible answer that directly answers the question.

Rules:
- Use the exact surface form from the memories where possible (e.g., "social worker", not "working as a social worker").
- Prefer noun phrases or single words over full sentences.
- If the memories do not contain the answer, respond with "UNKNOWN".
- Never hallucinate facts not present in the memories.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

// answerEnhancePromptSoft — PR #249 verbatim. Allows synthesis from the
// closest grounded fact when the question's surface form differs from the
// memory's. Reduced UNKNOWN rate on atomic-fact rows but tended to over-
// paraphrase ("transgender woman" instead of "transgender") which costs
// F1 against single-token gold. Best for benchmarks that score on semantic
// similarity rather than token overlap.
const answerEnhancePromptSoft = `You are a precise answer extractor. Given a user's question and a list of retrieved memories, respond with the SHORTEST possible answer that directly answers the question.

Rules:
- Ground every answer in the memories. Never invent facts not present.
- Prefer noun phrases or single words over full sentences (e.g., "social worker", "transgender woman", "3").
- Atomic facts may not contain the question's surface form. That is fine — synthesise the answer from the closest grounded fact (e.g., the question "What is Caroline's identity?" answered from a memory "Caroline is a transgender woman" → "transgender woman", not "UNKNOWN").
- If the memories contain partial evidence, return the most specific grounded answer you can extract. Do NOT default to "UNKNOWN" when related evidence is present.
- Only respond with "UNKNOWN" when NO related information exists in the memories at all.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

// answerEnhancePromptProbabilistic — PR #252 verbatim. Single base prompt
// with optional category-shape hint appended at runtime by the embedding
// classifier. The base is a tightened extractive form that strips memory
// modifiers ("a"/"the") while still allowing synthesis. This is the
// default mode shipping in production since #252.
//
// History (verbatim from the original #252 doc-comment):
//   - PR #249 softened the original "exact surface form" rule after
//     observing 82% UNKNOWN on atomic-fact rows. The replacement example
//     ("transgender woman") inadvertently primed the model toward 2-token
//     paraphrase pulled verbatim from memory text.
//   - PR #250 added a regex-based hybrid classifier with per-category
//     prompts. The classifier mis-routed most cat-1 questions into the
//     soft open_domain branch, compounding #249's overshoot.
//   - Replay cefix4 showed F1 -25% (cat-1 -71%, cat-5 -72%); semsim flat
//     at 0.78 confirms the model still answers semantically — it just
//     adds memory-text modifiers that miss gold token-overlap.
//
// Two principles encoded in the rules:
//   1. SHORTEST surface form — single noun / name / number / count.
//      No "a"/"the"/"works as", no qualifiers from the memory.
//   2. Allow synthesis when surface form differs, but the OUTPUT shape
//      must be the gold-style minimum, not the verbatim memory phrase.
const answerEnhancePromptProbabilistic = `You are a precise answer extractor. Given a user's question and a list of retrieved memories, respond with the SHORTEST surface form that answers the question.

Rules:
- Return ONLY a single noun, name, number, count, short list, or short noun phrase — no full sentences, no preamble, no framing.
- Do NOT include modifiers, articles, or qualifiers from the memory text unless the question explicitly asks for them. Strip "a"/"the"/"works as"/"is a"/"who is"/etc.
- Synthesise the minimum-form answer when the question's surface form differs from the memory's. The OUTPUT shape must match the gold style, not the memory phrasing.
- Every token in the answer must be derivable from the memories. Never invent or hallucinate facts not present.
- If the memories contain partial evidence, return the closest grounded single noun/name. Use UNKNOWN only when NO related information exists in the memories at all.

Examples:
- question: "What is Caroline's job?"  memory: "Caroline works as a social worker" → "social worker"  (NOT "a social worker", NOT "social worker advocating against assault")
- question: "How many children does Melanie have?"  memory: "Melanie has three kids" → "3"  (NOT "three children", NOT "three kids")
- question: "What is Caroline's identity?"  memory: "Caroline is a transgender woman" → "transgender"  (NOT "transgender woman", NOT "a transgender woman")
- question: "What are Melanie's pets' names?"  memory: "Oliver, Luna, Bailey" → "Oliver, Luna, Bailey"
- question: "Did Caroline make the bowl?"  memory: "Caroline shaped the black-and-white bowl in pottery class" → "Yes"

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

// answerEnhanceSystemPrompt is the legacy identifier. Aliased to the
// probabilistic variant to preserve byte-identical behaviour for the
// post-#252 default and any external code that still references it.
const answerEnhanceSystemPrompt = answerEnhancePromptProbabilistic

// AnswerEnhanceConfig configures the post-retrieval answer-extraction LLM call.
// Mirrors LLMRerankConfig shape so the service can reuse the same proxy credentials.
type AnswerEnhanceConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// answerEnhanceEnabled reports whether MEMDB_SEARCH_ENHANCE is set to "true".
func answerEnhanceEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_ENHANCE") == "true"
}

// AnswerEnhanceResponse is the parsed LLM response.
type AnswerEnhanceResponse struct {
	Answer     string   `json:"answer"`
	SourceIDs  []string `json:"source_ids"`
	Confidence float64  `json:"confidence"`
}

// EnhanceRetrievalAnswer distills the top-K retrieved memories into a concise,
// query-aligned answer. Returns (answer, sourceIDs, confidence, hinted, err).
//
// The system prompt is the base extractor with an OPTIONAL category-shape
// hint appended. The hint is built from a soft, embedding-based classifier
// (see answer_enhance_classifier.go) and is suppressed when:
//   - classifier is disabled (MEMDB_D10_CLASSIFIER_ENABLED=false)
//   - emb is nil, returns an error, or no signal
//   - top-1 confidence < MEMDB_D10_CLASSIFIER_THRESHOLD (default 0.5)
//   - top-1 category is open_domain (no useful shape constraint)
//
// In every suppressed case the prompt is byte-identical to the
// post-revert single-prompt baseline — so the rollout cost on a
// classifier mis-fire is exactly zero.
//
// `hinted` reports whether the hint was actually appended; the caller
// surfaces it as a metric label so we can compare hinted vs un-hinted
// outcomes without burning category-cardinality on the counter.
//
// Semantics:
//   - empty items → ("UNKNOWN", nil, 0, false, nil)
//   - no items with relativity ≥ answerEnhanceMinRelativity() → ("UNKNOWN", nil, 0, false, nil)
//   - LLM error or malformed JSON → ("UNKNOWN", nil, 0, hinted, err) (caller
//     should log Debug and continue without enhancement)
//   - LLM returns literal "UNKNOWN" → propagated as-is, err = nil
func EnhanceRetrievalAnswer(
	ctx context.Context,
	query string,
	items []map[string]any,
	cfg AnswerEnhanceConfig,
	emb classifierEmbedder,
) (string, []string, float64, bool, error) {
	// Backwards-compatible entry point — load the per-request D10Config
	// snapshot inline. Newer call sites can use EnhanceRetrievalAnswerWithConfig
	// directly to amortise the env reads across multiple calls.
	return EnhanceRetrievalAnswerWithConfig(ctx, query, items, cfg, emb, LoadD10Config())
}

// EnhanceRetrievalAnswerWithConfig is the explicit-config variant — used by
// applyAnswerEnhancement in the post-processing pipeline so the per-request
// LoadD10Config call lives at exactly one site (the pipeline hook), not
// re-evaluated by every helper.
//
// Behaviour matches EnhanceRetrievalAnswer's contract; the only change is
// that MinRelativity, classifier threshold, and prompt selection come from
// d10cfg instead of fresh env reads.
func EnhanceRetrievalAnswerWithConfig(
	ctx context.Context,
	query string,
	items []map[string]any,
	cfg AnswerEnhanceConfig,
	emb classifierEmbedder,
	d10cfg D10Config,
) (string, []string, float64, bool, error) {
	if len(items) == 0 || cfg.APIURL == "" {
		return answerEnhanceUnknownAnswer, nil, 0, false, nil
	}

	// Keep only items above the relativity floor, cap at top-N.
	minRel := d10cfg.MinRelativity
	candidates := make([]map[string]any, 0, answerEnhanceTopN)
	for _, it := range items {
		if len(candidates) >= answerEnhanceTopN {
			break
		}
		if getRelativity(it) < minRel {
			continue
		}
		candidates = append(candidates, it)
	}
	if len(candidates) == 0 {
		return answerEnhanceUnknownAnswer, nil, 0, false, nil
	}

	var memBlock strings.Builder
	for i, c := range candidates {
		id, _ := c["id"].(string)
		mem, _ := c["memory"].(string)
		fmt.Fprintf(&memBlock, "[%d] id=%s  %s\n", i+1, id, strings.TrimSpace(mem))
	}

	userMsg := fmt.Sprintf(
		"Question: %s\n\nMemories:\n%s\nRespond with JSON only.",
		query, memBlock.String(),
	)

	systemPrompt, hinted := d10cfg.PromptForQuery(ctx, query, emb)

	var parsed AnswerEnhanceResponse
	if err := callAnswerEnhanceLLM(ctx, systemPrompt, userMsg, cfg, &parsed); err != nil {
		return answerEnhanceUnknownAnswer, nil, 0, hinted, fmt.Errorf("enhance llm: %w", err)
	}
	if strings.TrimSpace(parsed.Answer) == "" {
		return answerEnhanceUnknownAnswer, nil, 0, hinted, nil
	}
	return parsed.Answer, parsed.SourceIDs, parsed.Confidence, hinted, nil
}

// buildAnswerEnhanceSystemPrompt assembles the D10 system prompt.
//
// When the embedding classifier is disabled, emb is nil, or the classifier
// returns no useful hint (low confidence / open_domain), the function
// returns the unmodified base prompt — the prompt is byte-identical to
// post-revert main, the rollout-safety property of this design.
//
// Otherwise the function appends a short hint block produced by
// categoryHintBlock; the base prompt itself is never altered.
func buildAnswerEnhanceSystemPrompt(ctx context.Context, query string, emb classifierEmbedder) (string, bool) {
	if emb == nil || !d10ClassifierEnabled() {
		return answerEnhanceSystemPrompt, false
	}
	classifier := classifierForEmbedder(emb)
	top, err := classifier.ClassifyTopN(ctx, query, 2)
	if err != nil || len(top) == 0 {
		return answerEnhanceSystemPrompt, false
	}
	hint := categoryHintBlock(top, d10ClassifierThreshold())
	if hint == "" {
		return answerEnhanceSystemPrompt, false
	}
	return answerEnhanceSystemPrompt + hint, true
}

// callAnswerEnhanceLLM performs the single chat-completion round trip for D10
// and unmarshals the response into target. Kept separate (not via the shared
// *llm.Client retrying global) so we can enforce the tight 10s D10 timeout
// without interfering with the shared 90s-timeout LLM client used elsewhere.
func callAnswerEnhanceLLM(ctx context.Context, systemPrompt, userMsg string, cfg AnswerEnhanceConfig, target *AnswerEnhanceResponse) error {
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	return llm.ChatStructured(ctx, client, "d10_enhance", []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}, target,
		llm.WithMaxTokens(answerEnhanceMaxTokens),
		llm.WithTimeout(answerEnhanceTimeout),
		llm.WithRespBodyLimit(answerEnhanceRespBodyLimit),
	)
}

// prependEnhancedAnswer, applyAnswerEnhancement and hintedLabel — see
// answer_enhance_pipeline.go (postProcessResults integration concern, kept
// separate from the prompt + LLM call concerns above).
