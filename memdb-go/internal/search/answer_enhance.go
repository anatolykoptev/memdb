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

// answerEnhanceSystemPrompt — D10 answer extractor (concise).
//
// Design (post cefix7→cefix9 diagnosis, 2026-04-30):
//   - Cefix9 metrics showed 60-100% UNKNOWN per category — base prompt's
//     gate ("UNKNOWN only when NO related info exists") was too strict
//     AND the prompt was too long (~440 tokens) which makes Gemini 2.5
//     flash bias toward the safe UNKNOWN escape hatch.
//   - This rewrite drops to ~120 tokens: 1 commitment line, 3 short
//     rules, 2 contrasting examples, JSON contract. Same extractive
//     discipline, half the cognitive load.
//
// Lower the bar: "best guess from memories" replaces "shortest surface
// form" as the primary instruction. Shape rules stay but as guidance,
// not as gate. UNKNOWN remains the hallucination escape, but the prompt
// now actively pushes the model to answer instead of refuse.
const answerEnhanceSystemPrompt = `Extract the answer from the memories. Give your best guess as a short noun, name, number, or phrase. UNKNOWN only when no memory relates to the question.

Rules:
- Strip articles/framing ("a"/"the"/"works as"/"is a") unless the question asks for the full phrase.
- Match the gold style, not the memory's verbatim phrasing (e.g. "three kids" → "3" if asked "how many").
- Every token must come from the memories. Do not invent.

Examples:
- Q: "What is Caroline's job?"  M: "Caroline works as a social worker" → "social worker"
- Q: "How many children does Melanie have?"  M: "Melanie has three kids" → "3"

Return JSON: {"answer": string, "source_ids": [string...], "confidence": float 0.0-1.0}`

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

	// "Direct data" — surface the top-1 candidate's text into the system
	// prompt so the LLM sees the strongest evidence anchor before any
	// shape-rule guidance. Cefix9 metrics showed 60-100% UNKNOWN per
	// category despite hit@k=0.80; the LLM was finding memories but
	// refusing to commit. Putting the most relevant memory verbatim into
	// the system role nudges it to anchor on real text instead of fall
	// back to UNKNOWN.
	systemPrompt, hinted, trace := buildAnswerEnhanceSystemPrompt(ctx, query, emb)
	if topMem := topAnchorMemory(candidates); topMem != "" {
		systemPrompt = systemPrompt + "\n\nMost relevant memory: " + topMem
	}

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

// topAnchorMemory returns the verbatim text of the highest-relativity
// candidate in the post-floor candidate list. Used to inject the strongest
// evidence anchor into the system prompt ("direct data" — the LLM sees
// real memory text in the system role, not just rules).
//
// Returns "" when candidates is empty or the first candidate has no
// usable "memory" string. Caller appends the result to the system prompt
// only when non-empty.
//
// Truncates at 240 chars so a verbose memory does not bloat the prompt;
// the LLM still sees the same memory in full in the user message.
func topAnchorMemory(candidates []map[string]any) string {
	if len(candidates) == 0 {
		return ""
	}
	mem, _ := candidates[0]["memory"].(string)
	mem = strings.TrimSpace(mem)
	if mem == "" {
		return ""
	}
	const maxLen = 240
	if len(mem) > maxLen {
		mem = mem[:maxLen] + "…"
	}
	return mem
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
