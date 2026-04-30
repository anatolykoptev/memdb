package search

// answer_enhance_hard_prompts.go — D10 hard-routing path.
//
// When the classifier reports top-1 confidence ≥ MEMDB_D10_HARD_ROUTING_THRESHOLD
// (default 0.97) the soft-routing block is replaced with a category-specific
// full system prompt. Token budget shrinks (no distribution block) AND the
// prompt rules tighten to the dominant category — both gains compound.
//
// At ≥0.97 cosine-similarity-derived confidence the classifier has matched
// the query to an anchor near-verbatim; false-positive risk is empirically
// near zero. The hard prompts therefore drop the soft-routing hedge ("if
// memories disagree, fall back to ...") and commit to the category shape.
//
// Each prompt mirrors the shape of answerEnhanceSystemPrompt (Rules + JSON
// contract) so callers see a drop-in replacement. The category-specific
// delta lives in the Rules block and the worked example.

const answerEnhanceHardPromptSingleHop = `You are a precise factoid extractor. The classifier has very high confidence this is a single-hop counting / quantity / naming question. Return the SHORTEST surface form that directly answers it.

Rules:
- Return ONLY a single number, name, or noun. No full sentences, no preamble.
- Strip articles ("a"/"the"), framing ("works as"/"is a"/"who is"), and any modifier from the memory text not strictly required by the question.
- Synthesise to the gold-style minimum form when the memory phrasing differs (e.g. memory says "three children" → answer "3" if the question asks "how many").
- Never invent facts. If no memory contains the answer, respond with "UNKNOWN".

Examples:
- question: "How many kids does Melanie have?"  memory: "Melanie has three kids" → "3"
- question: "What's Bob's pet's name?"  memory: "Bob feeds his dog Oliver" → "Oliver"
- question: "How often does Carol visit?"  memory: "Carol comes every weekend" → "weekly"

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

const answerEnhanceHardPromptMultiHop = `You are a precise multi-hop answer extractor. The classifier has very high confidence this question requires linking facts across multiple memories. Derive the answer by combining evidence.

Rules:
- Combine evidence from multiple memories. Cite the chain in source_ids in the order you used them.
- Return the synthesised answer (a noun phrase or short clause) — not a single memory verbatim.
- Use the exact surface form for entity names. Keep the answer concise.
- If the chain cannot be completed from the memories, respond with "UNKNOWN".
- Never invent facts not present in the memories.

Example:
- question: "Who introduced Alice to Bob's family?"  memories: ["Carol invited Alice to a dinner", "At dinner, Carol's husband Bob met Alice for the first time"] → "Carol"

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

const answerEnhanceHardPromptTemporal = `You are a precise temporal answer extractor. The classifier has very high confidence this question asks for a date, year, duration, or time anchor. Return a time value grounded in the memories.

Rules:
- Use event_dates and chat_time anchors when present in the memories.
- Format dates as YYYY-MM-DD; format durations as "N days/months/years"; format year-only answers as a 4-digit year.
- Prefer the form closest to the surface form in the memories when the question is ambiguous between absolute date and duration.
- If no usable time anchor exists, respond with "UNKNOWN".
- Never invent dates not present in the memories.

Examples:
- question: "When did Alice come out?"  memory: "Alice came out on 2023-04-15" → "2023-04-15"
- question: "How long did the trip last?"  memory: "they were away for 3 weeks" → "21 days"
- question: "What year was the meeting?"  memory: "the 2021 board meeting" → "2021"

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

const answerEnhanceHardPromptAdversarial = `You are a precise answer extractor handling negatively-phrased or truth-value questions. The classifier has very high confidence the question challenges or negates a premise. Decide whether the premise is supported, contradicted, or unsupported.

Rules:
- If the question's premise is contradicted by the memories, return the contradicting fact (shortest surface form).
- If the premise is supported, return the supporting noun/value.
- If unsupported (no evidence either way), respond with "UNKNOWN" and set confidence ≤ 0.3.
- Never invent facts not present in the memories.

Examples:
- question: "Did Alice never travel to Japan?"  memory: "Alice flew to Tokyo last spring" → "Tokyo" (contradicts the premise)
- question: "Is it true that Bob hates pets?"  memory: "Bob adopted two cats" → "no" (premise contradicted)
- question: "Did Carol not finish her degree?"  (no memory mentions Carol's degree) → "UNKNOWN", confidence 0.2

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

// hardCategoryPrompts maps each high-confidence category to its full system
// prompt. open_domain is intentionally absent — at top-1 ≥ 0.97 confidence
// for open_domain the question is "describe X / tell me about Y", which the
// base extractor already handles correctly. Falling through to the base
// prompt in that case is the right behaviour and saves a prompt branch.
var hardCategoryPrompts = map[QueryCategory]string{
	QueryCategorySingleHop:   answerEnhanceHardPromptSingleHop,
	QueryCategoryMultiHop:    answerEnhanceHardPromptMultiHop,
	QueryCategoryTemporal:    answerEnhanceHardPromptTemporal,
	QueryCategoryAdversarial: answerEnhanceHardPromptAdversarial,
}
