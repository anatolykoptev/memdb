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

// answerEnhanceSystemPrompt — D10 answer extractor.
//
// History:
//   - PR #249 (2026-04-30) softened the original "exact surface form" rule
//     after observing 82% UNKNOWN on atomic-fact rows. The replacement
//     example ("transgender woman" answer) inadvertently primed the model
//     toward 2-token paraphrase pulled verbatim from memory text.
//   - PR #250 (2026-04-30) added a regex-based hybrid classifier with
//     per-category prompts. The classifier mis-routed most cat-1 questions
//     ("what are X's pets' names", "did X do Y", etc.) into the soft
//     open_domain branch, compounding #249's overshoot.
//   - Replay cefix4 (2026-04-30) showed F1 -25% (cat-1 -71%, cat-5 -72%);
//     advisor + reviewer agreed the regression is real on single-token
//     gold ("3", "Oliver"), not just paraphrase drift. semsim flat at
//     0.78 confirms the model still answers semantically — it just adds
//     memory-text modifiers that miss the gold token-overlap.
//
// This rewrite restores extractive discipline while keeping the synthesis
// permission for atomic-fact rows. Two principles:
//   1. SHORTEST surface form — single noun / name / number / count.
//      No "a"/"the"/"works as", no qualifiers from the memory.
//   2. Allow synthesis when surface form differs, but the OUTPUT shape
//      must be the gold-style minimum, not the verbatim memory phrase.
//
// The hallucination guard is now grounded in a stronger rule: every
// returned token must be derivable from the memories — adjectives /
// modifiers / framing words that are not strictly necessary to answer
// must be omitted.
const answerEnhanceSystemPrompt = `You are a precise answer extractor. Given a user's question and a list of retrieved memories, respond with the SHORTEST surface form that answers the question.

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
// The system prompt is selected by buildAnswerEnhanceSystemPrompt according
// to the hybrid soft/hard routing strategy (full design + decision rules
// live with that function). At a glance:
//
//   - classifier disabled / nil embedder / no signal / top-1 == open_domain
//     at hard threshold → unmodified base prompt (hinted=false). Byte-identical
//     to the post-revert single-prompt baseline in every suppressed case.
//   - top-1 confidence ≥ MEMDB_D10_HARD_ROUTING_THRESHOLD (default 0.97) AND
//     a category-specific hard prompt exists → full-replacement hard prompt
//     (hinted=true).
//   - else → base prompt + soft distribution block (hinted=true).
//
// `hinted` is surfaced as a metric label so we can compare classifier-
// influenced vs base-prompt outcomes without burning category-cardinality
// on the counter.
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
) (string, []string, float64, bool, d10RoutingTrace, error) {
	if len(items) == 0 || cfg.APIURL == "" {
		return answerEnhanceUnknownAnswer, nil, 0, false, d10RoutingTrace{Mode: D10RouteBase}, nil
	}

	// Keep only items above the relativity floor, cap at top-N.
	minRel := answerEnhanceMinRelativity()
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
		return answerEnhanceUnknownAnswer, nil, 0, false, d10RoutingTrace{Mode: D10RouteBase}, nil
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

	systemPrompt, hinted, trace := buildAnswerEnhanceSystemPrompt(ctx, query, emb)

	var parsed AnswerEnhanceResponse
	if err := callAnswerEnhanceLLM(ctx, systemPrompt, userMsg, cfg, &parsed); err != nil {
		return answerEnhanceUnknownAnswer, nil, 0, hinted, trace, fmt.Errorf("enhance llm: %w", err)
	}
	if strings.TrimSpace(parsed.Answer) == "" {
		return answerEnhanceUnknownAnswer, nil, 0, hinted, trace, nil
	}
	return parsed.Answer, parsed.SourceIDs, parsed.Confidence, hinted, trace, nil
}

// buildAnswerEnhanceSystemPrompt assembles the D10 system prompt with a
// hybrid soft/hard routing strategy:
//
//  1. Classifier disabled / no embedder / no signal → unmodified base
//     prompt (byte-identical to main).
//  2. Top-1 ≥ MEMDB_D10_HARD_ROUTING_THRESHOLD AND a hard prompt is
//     registered for that category → full replacement with the
//     category-specific hard prompt (shorter prompt + tighter rules).
//  3. Otherwise → base prompt + soft-routing distribution block, so the
//     LLM sees the classifier's full distribution and decides itself
//     which shape rules apply.
//
// The boolean return ("hinted") is true whenever the classifier
// influenced the prompt — both the hard-route and soft-distribution paths
// flag it. Callers use this for the metric label so we can compare
// classifier-influenced vs base-prompt outcomes.
//
// Returns also a d10RoutingTrace capturing the numeric signals that fed
// the routing decision; the trace is recorded as observability metrics
// downstream (see recordD10Routing in d10_metrics.go).
func buildAnswerEnhanceSystemPrompt(ctx context.Context, query string, emb classifierEmbedder) (string, bool, d10RoutingTrace) {
	if emb == nil || !d10ClassifierEnabled() {
		return answerEnhanceSystemPrompt, false, d10RoutingTrace{Mode: D10RouteBase}
	}
	classifier := classifierForEmbedder(emb)
	dist, err := classifier.classifyAndDistribute(ctx, query)
	if err != nil || len(dist) == 0 {
		return answerEnhanceSystemPrompt, false, d10RoutingTrace{Mode: D10RouteBase}
	}
	// Build the trace bundle once — every return path below references
	// these numbers, so computing them here keeps the routing branches
	// aligned with what the metrics show.
	trace := d10RoutingTrace{
		Top1Cat:   dist[0].Category,
		Top1Conf:  dist[0].Confidence,
		Entropy:   distributionEntropy(dist),
		HasSignal: !(len(dist) == 1 && dist[0].Confidence == 0),
	}
	if len(dist) > 1 {
		trace.Top2Conf = dist[1].Confidence
	}
	// No-signal sentinel from classifyAndDistribute: nil emb / empty
	// embed / single-entry zero-confidence — fall through to base.
	if !trace.HasSignal {
		trace.Mode = D10RouteBase
		return answerEnhanceSystemPrompt, false, trace
	}
	// Hard routing: top-1 confidence saturates the gate.
	// - Have a dedicated full prompt for this category → return it.
	// - Top-1 == open_domain at hard confidence → 97/1/1/1/0 distribution
	//   carries no actionable shape signal; spending ~125 tokens on it would
	//   just dilute the base prompt. Return base prompt unmodified
	//   (hinted=false) so the caller's metric labels stay accurate.
	if dist[0].Confidence >= d10HardRoutingThreshold() {
		if hardPrompt, ok := hardCategoryPrompts[dist[0].Category]; ok {
			trace.Mode = D10RouteHard
			return hardPrompt, true, trace
		}
		if dist[0].Category == QueryCategoryOpenDomain {
			trace.Mode = D10RouteBase
			return answerEnhanceSystemPrompt, false, trace
		}
	}
	// Soft routing: append distribution block to the base prompt.
	block := distributionBlock(dist, d10SoftTopN())
	if block == "" {
		trace.Mode = D10RouteBase
		return answerEnhanceSystemPrompt, false, trace
	}
	trace.Mode = D10RouteSoft
	return answerEnhanceSystemPrompt + block, true, trace
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
