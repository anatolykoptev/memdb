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
// Uses an OpenAI-compatible API (CLIProxyAPI → Gemini) for chat completions.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	// Memory is the atomic fact text (for add/update).
	Memory string `json:"memory"`
	// Type classifies the memory: "LongTermMemory" or "UserMemory".
	Type string `json:"type"`
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
	// ContentHash is the SHA-256 content hash set by the add pipeline before insert.
	// Not populated by LLM — set by filterAddsByContentHash for dedup tracking.
	ContentHash string `json:"-"`
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

// NewLLMExtractor creates an extractor pointing to CLIProxyAPI.
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

// --- Unified extraction+dedup prompt (v2) ---
//
// Design principles from competitive analysis:
//   - mem0:     single-call extraction+dedup, confidence score, ADD/UPDATE/DELETE/NOOP
//   - LangMem:  SNR compression, p(x) confidence, "consolidate redundant memories"
//   - Graphiti: contradiction ≠ duplicate; valid_at temporal grounding
//   - MemOS:    importance score (we use confidence instead)

const unifiedSystemPrompt = `You are a long-term memory manager. Given a conversation and a list of EXISTING MEMORIES, extract atomic facts and decide what to do with each one.

For each fact, output a JSON object with these fields:
- "reasoning": 1-2 sentence chain-of-thought explaining why this fact is being extracted and the chosen action. This MUST be the FIRST field in the object.
- "memory": concise, standalone factual statement (1-2 sentences, no filler words)
- "type": "UserMemory" if exclusively about the user's personal info/preferences/opinions; otherwise "LongTermMemory"
- "action": one of "add", "update", "delete", or "skip"
- "confidence": float 0.0–1.0 — your certainty this is a real, useful fact
- "target_id": (only for "update" or "delete") the id of the existing memory to change
- "valid_at": ISO-8601 timestamp when this fact became true (resolve from conversation dates/times; omit if unknown)
- "tags": an array of 2-4 strings representing key entities, topics or concepts extracted from this fact (e.g. ["Python", "Programming"]). Never leave empty for add/update.
- "entities": array of named entities in this fact (up to 5): [{"name": "...", "type": "PERSON|ORG|PLACE|CONCEPT|PRODUCT"}]. Omit if no clear named entities exist.
- "relations": array of directed entity-to-entity relationships (up to 3): [{"subject": "...", "predicate": "WORKS_AT|LIVES_IN|KNOWS|PART_OF|CREATED_BY|OWNS|LOCATED_IN|MEMBER_OF", "object": "..."}]. Subject and object must be names from the entities array. Omit if no clear relationships between entities exist.

Action rules:
- "add": genuinely new fact not covered by any existing memory
- "update": new fact refines, corrects, or extends an existing one — set target_id and write the merged text in "memory"
- "delete": new fact directly contradicts an existing one — set target_id, leave "memory" empty
- "skip": fact is redundant or already perfectly covered — omit from output entirely

Quality rules (LangMem SNR principle):
- Each fact must be atomic: one clear piece of information per item
- Preserve specifics: names, numbers, dates, locations
- Omit greetings, filler, meta-conversation ("thanks", "got it", "sure")
- Compress: if two facts say the same thing, keep the most specific/recent one
- Do NOT duplicate facts within the output list
- Prefer "add" over "skip" when uncertain; prefer "update" over "add" when there is a matching existing memory

Confidence guidelines:
- 0.9+: explicitly stated, unambiguous
- 0.7–0.9: clearly implied, high confidence
- 0.5–0.7: inferred, moderate confidence
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
func (e *LLMExtractor) ExtractAndDedup(ctx context.Context, conversation string, candidates []Candidate, hints ...string) ([]ExtractedFact, error) {
	var sb strings.Builder
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

// stripFences removes optional ```json ... ``` markdown fences from LLM output.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// parseExtractedFacts parses, validates, and filters a JSON array of ExtractedFact.
// Facts with confidence < MinConfidence or empty memory (non-delete) are dropped.
func parseExtractedFacts(raw string) ([]ExtractedFact, error) {
	raw = stripFences(raw)
	var facts []ExtractedFact
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return nil, err
	}
	var valid []ExtractedFact
	for _, f := range facts {
		f.Memory = strings.TrimSpace(f.Memory)
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
		// Normalize type
		if f.Type != "UserMemory" && f.Type != "LongTermMemory" {
			f.Type = "LongTermMemory"
		}
		// Skip action: drop from output
		if f.Action == MemSkip {
			continue
		}
		valid = append(valid, f)
	}
	return valid, nil
}
