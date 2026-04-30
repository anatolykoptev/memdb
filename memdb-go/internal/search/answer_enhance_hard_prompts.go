package search

// answer_enhance_hard_prompts.go — D10 hard-routing path (concise).
//
// At top-1 ≥ MEMDB_D10_HARD_ROUTING_THRESHOLD (default 0.97) the soft
// distribution block is replaced with a category-specific full system
// prompt. Each prompt mirrors the base extractor shape but commits to
// one answer style.
//
// Sized at ~80 tokens each so the LLM does not drown in a long system
// while a short user message holds the memories. Cefix9 diagnosis showed
// the long base prompt (~440 tokens) biased the model toward UNKNOWN —
// the same lesson applies double here.

const answerEnhanceHardPromptSingleHop = `Single-hop factoid question. Return ONLY a number, name, or single noun. Strip "a"/"the". Synthesise to gold form ("three kids" → "3"). Every token from memories. UNKNOWN only if no memory relates.

Return JSON: {"answer": string, "source_ids": [string...], "confidence": float 0.0-1.0}`

const answerEnhanceHardPromptMultiHop = `Multi-hop question — answer by linking facts across memories. Return a noun phrase or short clause derived from the chain. Cite source_ids in linking order. Do not invent. UNKNOWN only if the chain breaks.

Return JSON: {"answer": string, "source_ids": [string...], "confidence": float 0.0-1.0}`

const answerEnhanceHardPromptTemporal = `Temporal question. Return a date (YYYY-MM-DD), a year (4 digits), or a duration ("3 days", "2 years"). Use the closest form to memory's surface form. Every value from memories. UNKNOWN only if no time anchor exists.

Return JSON: {"answer": string, "source_ids": [string...], "confidence": float 0.0-1.0}`

const answerEnhanceHardPromptAdversarial = `Truth-value or negated-premise question. If memory contradicts the premise, return the contradicting fact (shortest form). If supported, return the supporting noun. If unsupported, UNKNOWN with confidence ≤ 0.3. Do not invent.

Return JSON: {"answer": string, "source_ids": [string...], "confidence": float 0.0-1.0}`

// hardCategoryPrompts maps each high-confidence category to its full system
// prompt. open_domain is intentionally absent — at top-1 ≥ 0.97 confidence
// for "tell me about X" questions the base extractor handles them directly.
var hardCategoryPrompts = map[QueryCategory]string{
	QueryCategorySingleHop:   answerEnhanceHardPromptSingleHop,
	QueryCategoryMultiHop:    answerEnhanceHardPromptMultiHop,
	QueryCategoryTemporal:    answerEnhanceHardPromptTemporal,
	QueryCategoryAdversarial: answerEnhanceHardPromptAdversarial,
}
