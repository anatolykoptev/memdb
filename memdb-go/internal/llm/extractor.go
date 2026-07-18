// Package llm provides LLM-based memory extraction and deduplication utilities.
//
// v2 design (based on competitive analysis of mem0, LangMem, Graphiti/Zep):
//
//   - Unified extraction+dedup in ONE LLM call: existing candidates are passed
//     alongside the conversation so the LLM can decide ADD/UPDATE/DELETE/SKIP
//     per fact in a single round-trip. (mem0 pattern — saves one LLM call per fact)
//
//   - Confidence score 0.0–1.0 per fact: facts below MinConfidence are dropped
//     before insert. (mem0 pattern)
//
//   - Contradiction detection: separate from duplicate — a contradicted existing
//     memory gets action="delete" so it is invalidated. (Graphiti/Zep pattern)
//
//   - valid_at timestamp: each extracted fact carries the ISO-8601 time it became
//     true, resolved from the conversation context. (Graphiti/Zep bi-temporal model)
//
//   - LangMem SNR rule: "consolidate and compress redundant memories; avoid idle words"
//     is baked into the extraction prompt.
//
// Uses an OpenAI-compatible API for chat completions.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	// MinConfidence is the minimum confidence score for a fact to be persisted.
	// Facts below this threshold are treated as "skip" regardless of action.
	MinConfidence = 0.65

	// extractMaxTokens is the max_tokens cap for the unified extraction+dedup LLM call.
	extractMaxTokens = 4096
)

// MemAction is the operation to perform for an extracted fact.
type MemAction string

const (
	MemAdd    MemAction = "add"
	MemUpdate MemAction = "update"
	MemDelete MemAction = "delete" // contradicts an existing memory → invalidate it
	MemSkip   MemAction = "skip"
)

// ExtractedFact is the result of the unified extraction+dedup LLM call.
// It combines what was previously two separate structs (Fact + DedupDecision).
type ExtractedFact struct {
	// Reasoning is the chain-of-thought explaining why this fact is being extracted and its action.
	Reasoning string `json:"reasoning,omitempty"`
	// Memory is the atomic fact text (for add/update). Primary retrieval form —
	// populated from ResolvedText when present, else falls back to the raw extraction.
	Memory string `json:"memory"`
	// RawText is the verbatim original text from the conversation (audit trail).
	// Kept alongside Memory so operators can trace the resolved form back to the
	// exact source utterance. (D6 — pronoun/temporal resolution audit)
	RawText string `json:"raw_text,omitempty"`
	// ResolvedText is the pronoun+temporal-resolved form of the fact.
	// When set, parseExtractedFacts promotes it to Memory so retrieval uses the
	// resolved (self-contained, context-free) text. (D6)
	ResolvedText string `json:"resolved_text,omitempty"`
	// Type classifies the memory: "LongTermMemory", "UserMemory", or "PreferenceMemory".
	Type string `json:"type"`
	// PreferenceCategory is the 22-category MemOS-style taxonomy key
	// (food|communication|schedule|...) for PreferenceMemory entries. Empty for
	// non-preference facts. Enables per-category retrieval filters. (D8)
	PreferenceCategory string `json:"preference_category,omitempty"`
	// Action is what to do: add, update, delete, or skip.
	Action MemAction `json:"action"`
	// Confidence is the LLM's certainty 0.0–1.0. Facts below MinConfidence are dropped.
	Confidence float64 `json:"confidence"`
	// TargetID is the id of the existing memory to update or delete (empty for add/skip).
	TargetID string `json:"target_id,omitempty"`
	// ValidAt is the ISO-8601 timestamp when this fact became true (from conversation context).
	// Empty string means "now" (caller should fill in current time).
	ValidAt string `json:"valid_at,omitempty"`
	// Tags contains extracted topics or entities for the memory (Topic/Entity Extraction)
	Tags []string `json:"tags,omitempty"`
	// Entities contains named entities extracted from this fact for the knowledge graph.
	// Each entity has a Name and Type (PERSON, ORG, PLACE, CONCEPT, PRODUCT).
	// Populated by LLM; used to build entity_nodes and entity-level edges.
	Entities []EntityMention `json:"entities,omitempty"`
	// Relations contains directed entity-to-entity relationships (triplets) for this fact.
	// Each relation links two entities from the Entities array via a predicate.
	// Populated by LLM; used to build entity-to-entity edges in the knowledge graph.
	Relations []EntityRelation `json:"relations,omitempty"`
	// Hallucinated is set to true by the LLM when the fact is not explicitly supported
	// by the user's messages (inferred or contradicted). Hallucinated facts are dropped
	// before insert, eliminating the need for a separate filterHallucinatedFacts call.
	Hallucinated bool `json:"hallucinated,omitempty"`
	// EventDates are ISO-8601 (YYYY-MM-DD) calendar dates the fact references
	// — explicit anchors only ("On August 17, 2023…", "Friday Oct 18", "August 2023").
	// Empty when the fact has no explicit calendar date or only carries generic
	// relative time ("yesterday", "last week"). F11 bi-temporal index
	// (migration 0024_event_dates.sql) reads top-level properties.event_dates;
	// see internal/db/postgres_temporal.go for the search-time consumer.
	EventDates []string `json:"event_dates,omitempty"`
	// ContentHash is the SHA-256 content hash set by the add pipeline before insert.
	// Not populated by LLM — set by filterAddsByContentHash for dedup tracking.
	ContentHash string `json:"-"`
}

// EdgeValidity derives the bi-temporal (valid_at, invalid_at) pair for an
// edge written from this fact. F11 lift: explicit calendar dates the LLM
// extracted into EventDates are the strongest signal of "fact happened on
// date X" — they outrank ValidAt (chat-time fallback) on entity_edges and
// memory_edges.
//
// Rules:
//   - len(EventDates) >= 1 → valid_at = EventDates[0] + "T00:00:00Z"
//     (the YYYY-MM-DD strings are pre-validated by validateISODates /
//     filterISODatesNoCtx, so we promote without re-parsing).
//   - len(EventDates) == 2 → invalid_at = EventDates[1] + "T00:00:00Z"
//     (date range: start..end). Single-date facts leave invalid_at empty
//     so the contradiction-judge / async validator can still decide.
//   - len(EventDates) == 0 → fall back to ValidAt (legacy behaviour).
//
// fallbackValidAt is what the call site previously passed (typically
// ExtractedFact.ValidAt). Returning it unchanged when EventDates is empty
// preserves the pre-F11 path for facts without explicit calendar anchors.
func (f *ExtractedFact) EdgeValidity(fallbackValidAt string) (validAt, invalidAt string) {
	if f == nil || len(f.EventDates) == 0 {
		return fallbackValidAt, ""
	}
	validAt = f.EventDates[0] + "T00:00:00Z"
	if len(f.EventDates) >= 2 {
		invalidAt = f.EventDates[1] + "T00:00:00Z"
	}
	return validAt, invalidAt
}

// PreferenceCategories is the closed enum of valid preference_category values
// emitted by the LLM for PreferenceMemory entries. 14 explicit + 8 implicit
// (MemOS-style taxonomy). Kept exported so retrieval-side filter validation
// can share the same source of truth.
var PreferenceCategories = map[string]bool{
	// Explicit (spoken/written preferences)
	"food":          true,
	"communication": true,
	"schedule":      true,
	"entertainment": true,
	"social":        true,
	"professional":  true,
	"learning":      true,
	"health":        true,
	"location":      true,
	"technology":    true,
	"finance":       true,
	"values":        true,
	"product":       true,
	"service":       true,
	// Implicit (inferred from behaviour)
	"frequency":         true,
	"confidence_level":  true,
	"risk_tolerance":    true,
	"detail_preference": true,
	"proactivity":       true,
	"humour":            true,
	"formality":         true,
	"consistency":       true,
}

// EntityMention is a named entity extracted from a memory fact.
// Used to populate entity_nodes and MENTIONS_ENTITY edges in the knowledge graph.
type EntityMention struct {
	Name string `json:"name"` // canonical display name, e.g. "Яндекс"
	Type string `json:"type"` // PERSON | ORG | PLACE | CONCEPT | PRODUCT
}

// EntityRelation is a directed relationship between two named entities (a triplet).
// Subject and Object must match names from the Entities array of the same fact.
// Used to build entity-to-entity edges in the knowledge graph.
type EntityRelation struct {
	Subject   string `json:"subject"`   // entity name (must be in Entities list)
	Predicate string `json:"predicate"` // relation label, e.g. "WORKS_AT", "LIVES_IN", "KNOWS"
	Object    string `json:"object"`    // entity name (must be in Entities list)
}

// Candidate is an existing memory passed to the LLM for dedup context.
type Candidate struct {
	ID     string `json:"id"`
	Memory string `json:"memory"`
}

// --- Legacy types kept for backward compatibility with JudgeDedupMerge callers ---

// DedupAction is kept for callers that still use the two-step API.
type DedupAction = MemAction

const (
	DedupAdd    = MemAdd
	DedupUpdate = MemUpdate
	DedupSkip   = MemSkip
)

// DedupDecision is kept for backward compatibility.
type DedupDecision struct {
	Action    MemAction `json:"action"`
	TargetID  string    `json:"target_id,omitempty"`
	NewMemory string    `json:"new_memory,omitempty"`
}

// Fact is kept for backward compatibility.
type Fact = ExtractedFact

// LLMExtractor calls an OpenAI-compatible chat completion API to extract
// atomic facts from conversations and judge deduplication decisions.
type LLMExtractor struct {
	client *Client
}

// NewLLMExtractor creates an extractor using the configured OpenAI-compatible LLM API.
// model defaults to "gemini-2.0-flash-lite" if empty.
func NewLLMExtractor(baseURL, apiKey, model string, fallbackModels []string, logger *slog.Logger) *LLMExtractor {
	if model == "" {
		model = "gemini-2.0-flash-lite"
	}
	return &LLMExtractor{
		client: NewClient(baseURL, apiKey, model, fallbackModels, logger),
	}
}

// NewLLMExtractorWithClient creates an extractor using a pre-configured Client.
func NewLLMExtractorWithClient(c *Client) *LLMExtractor {
	return &LLMExtractor{client: c}
}

// Model returns the configured LLM model name.
func (e *LLMExtractor) Model() string { return e.client.Model() }

// Client returns the underlying chat client. Exposed so adjacent extractors
// (e.g. ProfileExtractor) can reuse the same retry + model-fallback config
// without duplicating credentials. Do NOT mutate the returned client.
func (e *LLMExtractor) Client() *Client { return e.client }

// --- Unified extraction+dedup prompt (v2) ---
//
// Design principles from competitive analysis:
//   - mem0:     single-call extraction+dedup, confidence score, ADD/UPDATE/DELETE/NOOP
//   - LangMem:  SNR compression, p(x) confidence, "consolidate redundant memories"
//   - Graphiti: contradiction ≠ duplicate; valid_at temporal grounding
//   - MemOS:    importance score (we use confidence instead)

const unifiedSystemPrompt = `You are a long-term memory manager. Given a conversation and a list of EXISTING MEMORIES, extract atomic facts and decide what to do with each one.

Resolution rules (D6 — apply BEFORE extracting any fact):
- Resolve all pronouns ("she", "he", "they", "it", "this", "that") using the preceding conversation context. Replace them with the concrete referent name (e.g. "she" → "Caroline").
- Convert relative temporal references to absolute using the "## Current Date" header below (the in-conversation anchor — NOT today's wall-clock). E.g. if Current Date is 2023-05-08: "yesterday" → "2023-05-07", "next Thursday" → the next Thursday after 2023-05-08, "last week" → the week before 2023-05-08. NEVER assume today's wall-clock when the Current Date header is present.
- If a pronoun or temporal reference CANNOT be resolved reliably from context, leave it AS-IS and cap "confidence" at 0.7.
- Store BOTH forms: "raw_text" = verbatim original from the conversation (for audit), "resolved_text" = the pronoun+temporal-resolved form (used as primary retrieval text).

Third-person rule (D8):
- ALWAYS express facts in third person with an explicit subject. Never use first-person pronouns ("I", "me", "my", "we", "our").
- When the user speaks, replace with the user's name if known, otherwise with "The user" (e.g. "I love hiking" → "The user loves hiking").
- When the assistant speaks, prefix with "The assistant..." (e.g. "I recommend X" → "The assistant recommends X").
- Third person applies to BOTH "memory" (resolved) and "raw_text" is kept verbatim — do NOT rewrite raw_text to third person.

For each fact, output a JSON object with these fields:
- "reasoning": 1-2 sentence chain-of-thought explaining why this fact is being extracted and the chosen action. This MUST be the FIRST field in the object.
- "raw_text": verbatim original utterance from the conversation (keeps first-person/pronouns AS-IS for audit).
- "resolved_text": third-person, pronoun-resolved, temporally-resolved form — 1-2 sentences, no filler words. This is the primary retrieval text.
- "memory": same as "resolved_text" (kept for backward compatibility; when both present, resolved_text wins).
- "type": "UserMemory" for the user's general personal info; "PreferenceMemory" for explicit preferences or inferred behavioural patterns; otherwise "LongTermMemory".
- "preference_category": (ONLY when type=="PreferenceMemory") one of the 22 taxonomy keys below. Omit for non-preference facts.
- "action": one of "add", "update", "delete", or "skip"
- "confidence": float 0.0–1.0 — your certainty this is a real, useful fact. Cap at 0.7 when resolution (D6) left pronouns/temporal refs unresolved.
- "target_id": (only for "update" or "delete") the id of the existing memory to change
- "valid_at": ISO-8601 timestamp when this fact became true (resolve from conversation dates/times; omit if unknown)
- "event_dates": REQUIRED array of ISO-8601 (YYYY-MM-DD) dates whenever the **raw_text** (verbatim source utterance) contains an explicit calendar reference. This field is INDEPENDENT of valid_at — emit BOTH when both apply (valid_at is the timestamp the fact became true; event_dates lists every calendar date the raw_text mentions). **Judge against raw_text, NOT resolved_text. The D6 resolution rule may convert "yesterday" → ISO inside resolved_text, but that resolution does NOT count as an explicit calendar reference for event_dates — only the verbatim raw_text counts.** Trigger conditions (raw_text MUST contain): full date ("August 17, 2023" → ["2023-08-17"]), month+year ("August 2023" → ["2023-08-01"]), weekday+month+day with year resolvable from raw_text context ("Friday Oct 18, 2024" → ["2024-10-18"]), date ranges (emit start AND end). Do NOT emit for generic relative time in raw_text like "yesterday", "last week", "next month", "this morning", "a few days ago", "recently", "soon", "the other day" — even if D6 resolved them to ISO inside resolved_text. Omit the field entirely when raw_text has no explicit calendar date; never emit an empty array or a placeholder. Examples: raw "On August 17, 2023 I went bowling" → "event_dates": ["2023-08-17"]. raw "I started the job in March 2025" → "event_dates": ["2025-03-01"]. raw "Yesterday I had coffee" → OMIT event_dates (raw_text has no explicit date even though D6 may put one in resolved_text). raw "I love hiking" → OMIT event_dates.
- "hallucinated": true if the fact is NOT explicitly stated by the user (inferred, assumed, or contradicted by the user's words). Omit or set false for facts the user clearly stated.
- "tags": an array of 2-4 strings representing key entities, topics or concepts extracted from this fact (e.g. ["Python", "Programming"]). Never leave empty for add/update.
- "entities": array of named entities in this fact (up to 5): [{"name": "...", "type": "PERSON|ORG|PLACE|CONCEPT|PRODUCT"}]. Omit if no clear named entities exist.
- "relations": array of directed entity-to-entity relationships (up to 3): [{"subject": "...", "predicate": "WORKS_AT|LIVES_IN|KNOWS|PART_OF|CREATED_BY|OWNS|LOCATED_IN|MEMBER_OF", "object": "..."}]. Subject and object must be names from the entities array. Omit if no clear relationships between entities exist.

Preference categories (D8 — only for type=="PreferenceMemory"):
Explicit (14):
  food — diet, favourite/disliked foods
  communication — preferred style (formal/casual, email/voice, verbose/terse)
  schedule — typical wake/sleep/work hours, time zones, availability
  entertainment — media, games, books, music genres
  social — relationship status, family, friend circle
  professional — job, industry, career goals
  learning — subjects/skills of interest, learning style
  health — fitness, dietary restrictions, medical conditions (sensitive)
  location — home city, travel patterns
  technology — preferred tools, OS, languages
  finance — budget habits, savings goals (sensitive)
  values — ethical stances, political views (sensitive)
  product — product preferences, brand loyalty
  service — service providers (banks, carriers)
Implicit (8, inferred from behaviour):
  frequency — how often user does X
  confidence_level — user's self-assessed skill
  risk_tolerance — willingness to try new things
  detail_preference — prefers brief or verbose responses
  proactivity — wants suggestions or waits for questions
  humour — style/frequency of humour
  formality — consistent tone across context
  consistency — stable over time or drifts

Action rules:
- "add": genuinely new fact not covered by any existing memory
- "update": new fact refines, corrects, or extends an existing one — set target_id and write the merged text in "resolved_text"/"memory"
- "delete": new fact directly contradicts an existing one — set target_id, leave "resolved_text"/"memory" empty
- "skip": fact is redundant or already perfectly covered — omit from output entirely

Quality rules (LangMem SNR principle):
- Each fact must be atomic: one clear piece of information per item
- Preserve specifics: names, numbers, dates, locations
- Omit greetings, filler, meta-conversation ("thanks", "got it", "sure")
- Compress: if two facts say the same thing, keep the most specific/recent one
- Do NOT duplicate facts within the output list
- Prefer "add" over "skip" when uncertain; prefer "update" over "add" when there is a matching existing memory

Confidence guidelines:
- 0.9+: explicitly stated, unambiguous, all pronouns/temporal refs resolved
- 0.7–0.9: clearly implied, high confidence, resolution clean
- 0.5–0.7: inferred, moderate confidence, OR pronoun/temporal resolution uncertain
- <0.5: speculative — omit these entirely

Return ONLY a JSON array of fact objects (no "skip" entries needed). Return [] if no meaningful facts exist.`

// ExtractAndDedup is the v2 unified method: one LLM call extracts facts AND
// decides ADD/UPDATE/DELETE against the provided candidates.
//
// candidates should be the top-N most similar existing memories (from vector search).
// hints are optional quality signals from the content router, injected into the user
// message to guide extraction focus. Pass no hints for default behavior.
// Facts with confidence < MinConfidence are filtered out before returning.
// The caller is responsible for acting on each fact's Action field.
//
// This wrapper uses today's UTC date as the D6 temporal anchor. Callers that
// have an in-conversation date (e.g. LoCoMo chat_time) MUST use
// ExtractAndDedupAt instead so "yesterday"/"next Thursday" resolve relative
// to the conversation, not to today's wall-clock.
func (e *LLMExtractor) ExtractAndDedup(ctx context.Context, conversation string, candidates []Candidate, hints ...string) ([]ExtractedFact, error) {
	return e.ExtractAndDedupAt(ctx, conversation, candidates, "", hints...)
}

// ExtractAndDedupAt is ExtractAndDedup with an explicit "current date" anchor.
//
// `now` should be "YYYY-MM-DD" (the in-conversation date — typically the
// latest message's chat_time). Empty string falls back to time.Now().UTC()
// for non-LoCoMo callers that have no conversation timestamp.
//
// The anchor is injected into the user message under "## Current Date" so
// the D6 resolution rule (relative → absolute temporal references) anchors
// against the conversation, not today's wall-clock. This is the LoCoMo
// data-fidelity fix: 2023-05-08 conversation MUST resolve "yesterday" to
// 2023-05-07, not 2026-04-28.
func (e *LLMExtractor) ExtractAndDedupAt(ctx context.Context, conversation string, candidates []Candidate, now string, hints ...string) ([]ExtractedFact, error) {
	if strings.TrimSpace(now) == "" {
		now = time.Now().UTC().Format("2006-01-02")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Current Date\n%s\n\n", now)
	sb.WriteString("Conversation:\n")
	sb.WriteString(conversation)

	if len(candidates) > 0 {
		sb.WriteString("\n\nEXISTING MEMORIES (for dedup context):\n")
		enc, _ := json.Marshal(candidates)
		sb.Write(enc)
	}

	if len(hints) > 0 {
		sb.WriteString("\n\n<content_hints>\n")
		for _, h := range hints {
			sb.WriteString("- ")
			sb.WriteString(h)
			sb.WriteString("\n")
		}
		sb.WriteString("</content_hints>")
	}

	msgs := []map[string]string{
		{"role": "system", "content": unifiedSystemPrompt},
		{"role": "user", "content": sb.String()},
	}

	raw, err := e.client.Chat(ctx, msgs, extractMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("extract and dedup: %w", err)
	}

	facts, err := parseExtractedFacts(raw)
	if err != nil {
		return nil, fmt.Errorf("extract and dedup parse: %w (raw: %.300s)", err, raw)
	}
	return facts, nil
}

// ExtractFacts is the legacy single-step extraction (no dedup context).
// Kept for backward compatibility and for cases with no existing memories.
func (e *LLMExtractor) ExtractFacts(ctx context.Context, conversation string) ([]ExtractedFact, error) {
	return e.ExtractAndDedup(ctx, conversation, nil)
}

// JudgeDedupMerge is the legacy two-step dedup judge.
// Kept for backward compatibility. New code should use ExtractAndDedup.
func (e *LLMExtractor) JudgeDedupMerge(ctx context.Context, newMem string, candidates []Candidate) (DedupDecision, error) {
	if len(candidates) == 0 {
		return DedupDecision{Action: DedupAdd}, nil
	}

	// Wrap as a minimal "conversation" for the unified prompt
	facts, err := e.ExtractAndDedup(ctx, "user: "+newMem, candidates)
	if err != nil || len(facts) == 0 {
		return DedupDecision{Action: DedupAdd}, nil
	}
	f := facts[0]
	switch f.Action {
	case MemUpdate:
		return DedupDecision{Action: DedupUpdate, TargetID: f.TargetID, NewMemory: f.Memory}, nil
	case MemSkip, MemDelete:
		return DedupDecision{Action: DedupSkip}, nil
	default:
		return DedupDecision{Action: DedupAdd}, nil
	}
}

// --- Internal helpers ---

// filterISODatesNoCtx is the context-less twin of validateISODates (atomic_extractor.go).
// Drops entries that fail time.Parse against isoDateLayout. Returns nil for an empty
// input or all-bad slice so the caller can omit the field entirely (json.Marshal with
// omitempty will skip a nil slice). Bad entries are silently dropped — the OTel
// counter in validateISODates is intentionally NOT mirrored here because parseExtractedFacts
// runs without a request context, and the legacy-path drop rate is observable through
// the F11 SQL coverage check ("count(properties ? 'event_dates')").
func filterISODatesNoCtx(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := time.Parse(isoDateLayout, s); err != nil {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseExtractedFacts parses, validates, and filters a JSON array of ExtractedFact.
// Facts with confidence < MinConfidence or empty memory (non-delete) are dropped.
//
// D6: promotes resolved_text to the primary Memory field when the LLM returned it.
// raw_text is preserved AS-IS (verbatim audit trail — never rewritten).
// D8: validates preference_category against the closed enum and clears the field
// when type != "PreferenceMemory" or the key is unknown.
func parseExtractedFacts(raw string) ([]ExtractedFact, error) {
	var facts []ExtractedFact
	if err := json.Unmarshal(StripJSONFence([]byte(raw)), &facts); err != nil {
		return nil, err
	}
	var valid []ExtractedFact
	for _, f := range facts {
		f.Memory = strings.TrimSpace(f.Memory)
		f.RawText = strings.TrimSpace(f.RawText)
		f.ResolvedText = strings.TrimSpace(f.ResolvedText)

		// D6: when resolved_text is present, it is the primary retrieval form.
		// Promote it to Memory so downstream code (embedding, storage, dedup)
		// sees the resolved (self-contained) text.
		if f.ResolvedText != "" {
			f.Memory = f.ResolvedText
		}

		// Normalize action
		switch f.Action {
		case MemAdd, MemUpdate, MemDelete, MemSkip:
		default:
			f.Action = MemAdd
		}
		// Skip low-confidence facts
		if f.Confidence < MinConfidence && f.Action != MemDelete {
			continue
		}
		// Skip empty memory unless it's a delete (delete only needs target_id)
		if f.Memory == "" && f.Action != MemDelete {
			continue
		}
		// Normalize type (UserMemory | LongTermMemory | PreferenceMemory).
		// PreferenceMemory is a valid extraction-time type (D8); non-pref
		// values fall back to LongTermMemory.
		if f.Type != "UserMemory" && f.Type != "LongTermMemory" && f.Type != "PreferenceMemory" {
			f.Type = "LongTermMemory"
		}
		// D8: preference_category is only meaningful for PreferenceMemory, and
		// must be one of the 22 taxonomy keys. Clear it otherwise.
		f.PreferenceCategory = strings.TrimSpace(f.PreferenceCategory)
		if f.Type != "PreferenceMemory" || !PreferenceCategories[f.PreferenceCategory] {
			f.PreferenceCategory = ""
		}
		// F11: event_dates must parse as ISO-8601 (YYYY-MM-DD). Drop entries
		// that don't (e.g. "summer 2023", "circa 2010", placeholder "YYYY-MM-DD")
		// silently — same policy as the atomic extractor (see validateISODates).
		// Nil result omits the field downstream.
		f.EventDates = filterISODatesNoCtx(f.EventDates)
		// Skip action: drop from output
		if f.Action == MemSkip {
			continue
		}
		valid = append(valid, f)
	}
	return valid, nil
}
