package search

// answer_enhance_types.go — D10 hybrid answer-extractor: per-category
// system prompts gated by a heuristic query classifier.
//
// Inspired by Cognee's BEAMRouter
// (cognee/eval_framework/answer_generation/beam_router.py:20–74), which
// routes each probing question to a question-type-specific system prompt
// before the answer LLM call. Cognee uses the dataset's pre-labelled
// question_type field; we don't have that at retrieval time, so we
// classify with a single regex+keyword pass over the raw query string
// (~10µs, no LLM). The downstream D10 LLM call is unchanged — only the
// system prompt switches.
//
// Categories mirror our LoCoMo taxonomy: cat-1 single_hop, cat-2
// multi_hop, cat-3 temporal, cat-4 open_domain, cat-5 adversarial.
//
// First-match-wins: the classifier walks rules in priority order
// (single_hop → temporal → adversarial → multi_hop variants →
// open_domain catch-all) so a question like "how many days did X NOT
// stay" lands in single_hop (the "how many" gate fires first), not
// adversarial. Tweak the order, not individual rules, if priority
// needs to change.

import (
	"regexp"
	"strings"
)

// QueryCategory is the heuristic classification of a search query, used
// to pick the D10 answer-extractor system prompt.
type QueryCategory string

const (
	CategorySingleHop   QueryCategory = "single_hop"
	CategoryMultiHop    QueryCategory = "multi_hop"
	CategoryTemporal    QueryCategory = "temporal"
	CategoryOpenDomain  QueryCategory = "open_domain"
	CategoryAdversarial QueryCategory = "adversarial"
)

// AllQueryCategories is the canonical, stable-order list used for
// metric pre-registration so every category label appears at zero on
// cold start.
var AllQueryCategories = []QueryCategory{
	CategorySingleHop,
	CategoryMultiHop,
	CategoryTemporal,
	CategoryOpenDomain,
	CategoryAdversarial,
}

// adversarialNegationRe matches "did <X> not|never <Y>" patterns —
// negative-phrased premises typical of adversarial questions.
var adversarialNegationRe = regexp.MustCompile(`(?i)\bdid\b.*\b(not|never)\b`)

// adversarialFalseRe matches explicit truth-value challenges ("is X
// false", "was X false").
var adversarialFalseRe = regexp.MustCompile(`(?i)\b(is|was|are|were)\b.*\bfalse\b`)

// properNounTokenRe matches a Capitalised token (first letter A-Z,
// remaining letters lowercase). Used to count proper-noun-style tokens
// for the cat-2 multi_hop heuristic ("what did Alice tell Bob ...").
var properNounTokenRe = regexp.MustCompile(`\b[A-Z][a-z]+\b`)

// classifyQueryCategory returns the heuristic category for a query.
// First-match-wins; empty/unknown fall through to open_domain.
//
// Rule order (priority):
//  1. single_hop  — starts with "how many" / "how often" / "how much"
//  2. temporal    — starts with "when" / "what date" / "what year" / "how long"
//  3. adversarial — contains "did .* (not|never)" / "is it true" / "is .* false"
//  4. multi_hop   — starts with "what" + 2+ proper-noun tokens, OR
//     starts with "who" + contains "and" / "with" / "while"
//  5. open_domain — catch-all (also empty/whitespace-only input)
func classifyQueryCategory(query string) QueryCategory {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return CategoryOpenDomain
	}

	// Rule 1: single_hop — counting / frequency / quantity prefixes.
	switch {
	case strings.HasPrefix(q, "how many"),
		strings.HasPrefix(q, "how often"),
		strings.HasPrefix(q, "how much"):
		return CategorySingleHop
	}

	// Rule 2: temporal — date/duration prefixes.
	switch {
	case strings.HasPrefix(q, "when"),
		strings.HasPrefix(q, "what date"),
		strings.HasPrefix(q, "what year"),
		strings.HasPrefix(q, "how long"):
		return CategoryTemporal
	}

	// Rule 3: adversarial — negative-phrased premise / truth-value challenge.
	if strings.Contains(q, "is it true") ||
		adversarialFalseRe.MatchString(q) ||
		adversarialNegationRe.MatchString(q) {
		return CategoryAdversarial
	}

	// Rule 4: multi_hop — "what" + 2+ proper-noun-style tokens
	// (preserve original-case query for capital detection).
	if strings.HasPrefix(q, "what") {
		caps := properNounTokenRe.FindAllString(query, -1)
		if len(caps) >= 2 {
			return CategoryMultiHop
		}
	}

	// Rule 4b: multi_hop — "who" + connective.
	if strings.HasPrefix(q, "who") {
		if strings.Contains(q, " and ") ||
			strings.Contains(q, " with ") ||
			strings.Contains(q, " while ") {
			return CategoryMultiHop
		}
	}

	// Rule 5: catch-all.
	return CategoryOpenDomain
}

// Per-category system prompts. Each carries the same hallucination
// guard + JSON contract, plus a category-specific delta line. The
// open_domain prompt lives in answer_enhance.go (canonical baseline,
// softened in PR #249 to permit synthesis from atomic facts); the
// others tune the "Rules:" block to match LoCoMo answer shapes for
// their category.

const answerEnhancePromptSingleHop = `You are a precise factoid extractor. Given a user's question and a list of retrieved memories, return the exact noun, number, or name that directly answers the question.

Rules:
- Return the exact noun/number/name. Synthesise from atomic facts when the surface form differs (e.g., memory says "three children" → answer "3" if the question asks "how many").
- Prefer the shortest surface form: a single word, a count, or a list of named entities.
- If the memories do not contain the answer, respond with "UNKNOWN".
- Never hallucinate facts not present in the memories.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

const answerEnhancePromptMultiHop = `You are a precise multi-hop answer extractor. Given a user's question and a list of retrieved memories, derive the answer by linking facts across multiple memories.

Rules:
- Combine evidence from multiple memories. Cite the chain of facts in source_ids in the order you used them.
- Return the answer derived from linking them — not a single memory verbatim.
- Use the exact surface form for entity names; keep the answer concise (noun phrase or short clause).
- If the chain cannot be completed from the memories, respond with "UNKNOWN".
- Never hallucinate facts not present in the memories.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

const answerEnhancePromptTemporal = `You are a precise temporal answer extractor. Given a user's question and a list of retrieved memories, return a date, year, or duration grounded in the memories.

Rules:
- Use event_dates and chat_time anchors in the memories. If the answer is a date or duration, format ISO-style (YYYY-MM-DD for dates, "P3D" / "3 days" for durations — prefer the form closest to the surface form in the memories).
- For year-only answers, return a 4-digit year.
- If the memories do not contain a usable time anchor, respond with "UNKNOWN".
- Never hallucinate dates not present in the memories.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

const answerEnhancePromptAdversarial = `You are a precise answer extractor handling negatively-phrased or truth-value questions. Given a user's question and a list of retrieved memories, decide whether the question's premise is supported, contradicted, or unsupported.

Rules:
- If the question's premise is contradicted by evidence in the memories, return the contradicting fact (the shortest surface form).
- If the premise is supported, return the supporting noun/value.
- If unsupported (no evidence either way), respond with "UNKNOWN" and set confidence ≤ 0.3.
- Never hallucinate facts not present in the memories.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

// selectAnswerEnhancePrompt returns the system prompt for the given
// category. Empty / unknown / open_domain → open_domain (safe default,
// preserves the pre-hybrid behaviour).
func selectAnswerEnhancePrompt(cat QueryCategory) string {
	switch cat {
	case CategorySingleHop:
		return answerEnhancePromptSingleHop
	case CategoryMultiHop:
		return answerEnhancePromptMultiHop
	case CategoryTemporal:
		return answerEnhancePromptTemporal
	case CategoryAdversarial:
		return answerEnhancePromptAdversarial
	case CategoryOpenDomain:
		return answerEnhancePromptOpenDomain
	default:
		return answerEnhancePromptOpenDomain
	}
}
