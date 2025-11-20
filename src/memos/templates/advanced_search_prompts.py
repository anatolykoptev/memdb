# Memory context assembly: recombine useful facts from memories to build a coherent context for answering
MEMORY_SUMMARY_PROMPT = """
# Memory Summary and Context Assembly

## Role
You are a precise context assembler. Given a user query and a set of retrieved memories (each indexed), your task is to synthesize a factual, concise, and coherent context using only the information explicitly present in the memories.

## Instructions

### Core Principles
- Use ONLY facts from the provided memories. Do not invent, infer, guess, or hallucinate.
- Resolve all pronouns (e.g., "he", "it", "they") and vague terms (e.g., "this", "that", "some people") to explicit entities using memory content.
- Merge overlapping or redundant facts. Preserve temporal, spatial, and relational details.
- Each fact must be atomic, unambiguous, and verifiable.
- Cite the source memory index for every fact using [mem:X] notation.

### Processing Logic
- Aggregate logically connected memories (e.g., events involving the same person, cause-effect chains, repeated entities).
- Exclude any memory that does not directly support answering the query.
- Prioritize specificity: e.g., "Travis Tang moved to Singapore in 2021" > "He relocated abroad."

## Input
- Query: {query}
- Current context:
{context}
- Current Memories:
{memories}

## Output Format (STRICT TAG-BASED)
Respond ONLY with the following XML-style tags. Do NOT include any other text, explanations, or formatting.

<context>
A single, compact, fluent paragraph synthesizing the above facts into a coherent narrative directly relevant to the query. Use resolved entities and logical flow. No bullet points. No markdown. No commentary.
</context>
<memories>
- Fact 1
- Fact 2
</memories>

Answer:
"""

# Stage 1: determine answerability; if not answerable, produce concrete retrieval phrases for missing info
STAGE1_EXPAND_RETRIEVE_PROMPT = """
# Stage 1 — Answerability and Missing Retrieval Phrases

## Goal
Determine whether the current memories can answer the query using concrete, specific facts. If not, generate 3–8 precise retrieval phrases that capture the missing information.

## Strict Criteria for Answerability
- The answer MUST be factual, precise, and grounded solely in memory content.
- Do NOT use vague adjectives (e.g., "usually", "often"), unresolved pronouns ("he", "it"), or generic statements.
- Do NOT answer with placeholders, speculation, or inferred information.

## Retrieval Phrase Requirements (if can_answer = false)
- Output 3–8 short, discriminative noun phrases or attribute-value pairs.
- Each phrase must include at least one explicit entity, attribute, time, or location.
- Avoid fuzzy words, subjective terms, or pronouns.
- Phrases must be directly usable as search queries in a vector or keyword retriever.

## Input
- Query: {query}
- Previous retrieval phrases:
{previous_retrieval_phrases}
- Current Memories:
{memories}

## Output (STRICT TAG-BASED FORMAT)
Respond ONLY with the following structure. Do not add any other text, explanation, or formatting.

<can_answer>
true or false
</can_answer>
<context>
summary of current memories
</context>
<reason>
Brief, one-sentence explanation for why the query is or isn't answerable with current memories.
</reason>
<retrieval_phrases>
- missing phrase 1
- missing phrase 2
...
</retrieval_phrases>

Answer:
"""


# Stage 2: if Stage 1 phrases still fail, rewrite the retrieval query and phrases to maximize recall
STAGE2_EXPAND_RETRIEVE_PROMPT = """
# Stage 2 — Rewrite Retrieval Query and Phrases to Improve Recall

## Goal
If Stage 1's retrieval phrases failed to yield an answer, rewrite the original query and expand the phrase list to maximize recall of relevant memories. Use canonicalization, synonym expansion, and constraint enrichment.

## Rewrite Strategy
- Canonicalize entities: use full names, official titles, or known aliases.
- Normalize time formats: e.g., "last year" → "2024", "in 2021" → "2021".
- Add discriminative tokens: entity + attribute + time + location where applicable.
- Split complex queries into focused sub-queries targeting distinct facets.
- Never include pronouns, vague terms, or subjective language.

## Input
- Query: {query}
- Previous retrieval phrases:
{previous_retrieval_phrases}
- Context: {context}
- Current Memories:
{memories}


## Output (STRICT TAG-BASED FORMAT)
Respond ONLY with the following structure. Do not add any other text, explanation, or formatting.

<can_answer>
true or false
</can_answer>
<reason>
Brief explanation (1–2 sentences) of how this rewrite improves recall over Stage 1 phrases.
</reason>
<context>
summary of current memories
</context>
<retrieval_phrases>
- new phrase 1 (Rewritten version of the original query. More precise, canonical, and retrieval-optimized.)
- new phrase 2
...
</retrieval_phrases>

Answer:
"""


# Stage 3: generate grounded hypotheses to guide retrieval when still not answerable
STAGE3_EXPAND_RETRIEVE_PROMPT = """
# Stage 3 — Hypothesis Generation for Retrieval

## Goal
When the query remains unanswerable, generate grounded, plausible hypotheses based ONLY on provided context and memories. Each hypothesis must imply a concrete retrieval target and validation criteria.

## Rules
- Base hypotheses strictly on facts from the memories. No new entities or assumptions.
- Frame each hypothesis as a testable statement: "If [X] is true, then the query is answered."
- For each hypothesis, define 1–3 specific evidence requirements that would confirm it.
- Do NOT guess. Do NOT invent. Only extrapolate from existing facts.

## Input
- Query: {query}
- Previous retrieval phrases:
{previous_retrieval_phrases}
- Context: {context}
- Memories:
{memories}

## Output (STRICT TAG-BASED FORMAT)
Respond ONLY with the following structure. Do not add any other text, explanation, or formatting.

<can_answer>
true or false
</can_answer>
<context>
summary of current memories
</context>
<reason>
- statement: <tentative, grounded hypothesis>
  retrieval_query: <searchable query derived from the hypothesis>
  validation_criteria:
  - <evidence requirement 1>
  - <evidence requirement 2>
- statement: <another hypothesis>
  retrieval_query: <searchable query>
  validation_criteria:
  - <evidence requirement>
</reason>

<retrieval_phrases>
- hypothesis retrieval query 1 (searchable query derived from the hypothesis)
- hypothesis retrieval query 2:
...
</retrieval_phrases>

Answer:
"""


PROMPT_MAPPING = {
    "memory_summary": MEMORY_SUMMARY_PROMPT,
    "stage1_expand_retrieve": STAGE1_EXPAND_RETRIEVE_PROMPT,
    "stage2_expand_retrieve": STAGE2_EXPAND_RETRIEVE_PROMPT,
    "stage3_expand_retrieve": STAGE3_EXPAND_RETRIEVE_PROMPT,
}
