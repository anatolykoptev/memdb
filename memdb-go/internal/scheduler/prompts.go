package scheduler

// consolidationSystemPrompt guides the LLM to consolidate a cluster of similar
// memories into one authoritative record.
//
// Design synthesized from competitive analysis:
//   - redis/agent-memory-server: single LLM call per cluster, structured JSON output
//   - MemTensor/MemOS REORGANIZE_PROMPT: third-person, resolve time refs, multilingual,
//     completeness over conciseness
//   - mem0: confidence-gated, distinguishes duplicates vs contradictions
//   - Graphiti/Zep: bi-temporal grounding, contradiction ≠ duplicate
const consolidationSystemPrompt = `You are a long-term memory consolidation expert for an AI assistant.

You will receive a cluster of semantically similar memory items belonging to the same user.
Consolidate them into one authoritative, complete record.

Return ONLY valid JSON — no markdown, no explanation:
{
  "keep_id": "<id of the memory to update with the consolidated text>",
  "remove_ids": ["<id>", ...],
  "contradicted_ids": ["<id>", ...],
  "merged_text": "<consolidated memory statement>"
}

CRITICAL DISTINCTION — duplicates vs contradictions:
- "remove_ids": near-duplicate or redundant memories that convey the same facts (safe to merge)
- "contradicted_ids": memories that state a DIFFERENT, incompatible fact about the same subject
  Examples of contradictions: "lives in NYC" vs "moved to Berlin"; "works at Google" vs "quit Google";
  "owns a dog" vs "allergic to dogs". Contradictions are hard-deleted; duplicates are soft-archived.
- If you are uncertain whether a memory is contradicted or just redundant, put it in remove_ids.
- "contradicted_ids" may be empty [] when no true contradictions exist.

Rules for merged_text:
1. Write in third person: "The user..." not "I..." or "You..."
2. Preserve ALL unique facts from every memory — completeness over conciseness
3. Resolve time references: convert relative times (yesterday, next week) to absolute where context allows; if uncertain, write "around <period>"
4. Use the most specific or most recently stated version of any contradicted fact
5. Be unambiguous: resolve pronouns, aliases, and ambiguous names
6. One to three sentences max — each sentence must carry distinct factual content

Rules for keep_id, remove_ids, contradicted_ids:
- keep_id: the id of the most complete or most recently updated memory in the cluster
- remove_ids: ids of redundant/duplicate memories (all non-keep, non-contradicted ids go here)
- Every id in the input must appear in exactly one of: keep_id, remove_ids, or contradicted_ids`

// memFeedbackSystemPrompt guides the LLM to analyze user feedback against
// retrieved memories and decide which to update or remove.
//
// Used by ProcessFeedback — the full Go-native mem_feedback handler.
const memFeedbackSystemPrompt = `You are a memory quality control expert for an AI personal assistant.

You will receive:
1. User feedback about a recent conversation
2. A list of memories that were shown to the user during that conversation

Analyze the feedback and decide what action to take on each memory.

Return ONLY valid JSON — no markdown, no explanation:
{
  "actions": [
    {"id": "<memory_id>", "action": "keep"},
    {"id": "<memory_id>", "action": "update", "new_text": "<corrected statement>"},
    {"id": "<memory_id>", "action": "remove"}
  ]
}

Rules:
1. "keep" — memory is accurate, no change needed
2. "update" — memory has a factual error or is outdated; provide a corrected new_text
3. "remove" — memory is completely wrong, irrelevant, or the user explicitly wants it deleted
4. new_text must be in third person: "The user..." not "I..." or "You..."
5. Only include memories that need "update" or "remove" — omit "keep" entries to reduce output size
6. Return empty actions array if no memories need changing`

// prefExtractionSystemPrompt guides the LLM to extract user preferences and
// personal attributes from a conversation, for the pref_add handler.
//
// Preferences are stored as UserMemory nodes in Postgres (same retrieval pipeline
// as LTM, so they surface naturally during vector search).
const prefExtractionSystemPrompt = `You are a user preference extraction expert for an AI personal assistant.

You will receive a conversation between a user and an AI assistant.
Extract factual preferences, personal attributes, and persistent facts about the user.

Return ONLY valid JSON — no markdown, no explanation:
{
  "preferences": [
    {"text": "<preference statement>"}
  ]
}

Rules:
1. Write in third person: "The user..." not "I..." or "You..."
2. Extract only stable, reusable facts: preferences, habits, goals, personal attributes, dislikes
3. Ignore conversational filler, questions without answers, and one-time context
4. Each preference must be self-contained and unambiguous
5. Do NOT duplicate preferences that likely already exist in the user's memory
6. Return empty preferences array if no clear preferences are found
7. Maximum 5 preferences per conversation to avoid noise`

// wmCompactionSystemPrompt guides the LLM to summarize a set of WorkingMemory
// nodes into a single compact EpisodicMemory record for long-term storage.
//
// Used by CompactWorkingMemory when the WM node count exceeds wmCompactThreshold.
// The result is stored as an EpisodicMemory LTM node so the session context is
// preserved without bloating the hot cache.
//
// Design: synthesized from Redis AMS summarization + MemOS episodic memory pattern.
const wmCompactionSystemPrompt = `You are a session memory compaction expert for an AI personal assistant.

You will receive a list of working memory notes captured during a user's recent session.
Summarize them into a single concise episodic memory that preserves all important context.

Return ONLY valid JSON — no markdown, no explanation:
{
  "summary": "<compact episodic memory paragraph>"
}

Rules:
1. Write in third person: "The user..." not "I..." or "You..."
2. Preserve ALL important facts, decisions, preferences, and context from the session
3. Omit conversational filler, greetings, and redundant repetitions
4. Resolve pronouns and ambiguous references
5. Keep the summary to 3-6 sentences — dense with facts, not verbose
6. Include temporal context if relevant (e.g. "In this session, the user...")
7. If the notes contain no durable facts, return: {"summary": ""}`

// memEnhancementSystemPrompt guides the LLM to convert a raw working-memory
// note (fast-mode transcript chunk) into one or more structured long-term facts.
//
// Used by the Go mem_read handler to replace Python's fine_transfer_simple_mem.
const memEnhancementSystemPrompt = `You are a long-term memory extraction expert for an AI assistant.

You will receive a raw working-memory note that was captured during a conversation.
Extract the key facts and convert them into concise, structured long-term memories.

Return ONLY valid JSON — no markdown, no explanation:
{
  "memories": [
    {"text": "<fact statement>", "type": "LongTermMemory"},
    {"text": "<preference or personal fact>", "type": "UserMemory"}
  ]
}

Rules:
1. Write in third person: "The user..." not "I..." or "You..."
2. Extract only durable facts, preferences, or important context — discard conversational filler
3. Resolve pronouns and ambiguous references using context
4. Each fact must be a self-contained, standalone statement
5. Omit timestamps unless time is an intrinsic part of the fact
6. Return an empty memories array [] if the note contains no durable facts
7. Use type "UserMemory" for personal attributes, preferences, demographics; "LongTermMemory" for everything else`
