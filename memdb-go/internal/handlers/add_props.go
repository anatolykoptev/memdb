package handlers

// add_props.go — memory node property construction and source serialization.
// Responsibility: build the JSONB properties map for a memory node and
// serialize/deserialize sources. No I/O, no DB calls.

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Extraction state values written to every Memory row (migration 0028). The
// partial index on state='pending' keeps any future filler batch SELECT cheap
// on multi-million-row cubes.
const (
	extractionStatePending    = "pending"
	extractionStateExtracting = "extracting"
	extractionStateExtracted  = "extracted"
	extractionStateFailed     = "failed"
)

// memoryNodeProps holds the parameters for building a memory node's property map.
// All fields correspond 1:1 to Python's memory properties format.
type memoryNodeProps struct {
	ID         string
	Memory     string
	MemoryType string // "LongTermMemory" | "UserMemory" | "PreferenceMemory" | "WorkingMemory"
	UserName   string // cube partition key (upstream MemOS convention; holds cube_id)
	UserID     string // person identity — Phase 2 split from cube_id
	AgentID    string // agent scope
	SessionID  string
	Mode       string // "fast" | "fine" — becomes tag "mode:<mode>"
	Now        string // updated_at timestamp (always current)
	CreatedAt  string // created_at timestamp (may differ for valid_at from LLM)
	Info       map[string]any
	CustomTags []string
	Sources    []map[string]any
	Background string // "[working_binding:<wm_id>]" for LTM nodes; "" for WM nodes

	// D6: verbatim original utterance, preserved alongside the resolved form in
	// Memory. Empty string means no raw text was provided by the extractor
	// (e.g. non-LLM callers like manual add).
	RawText string
	// D8: one of the 22-key MemOS preference taxonomy. Only set for PreferenceMemory
	// entries; empty otherwise.
	PreferenceCategory string

	// Key is an optional caller-supplied stable identifier (e.g. an
	// Anthropic memory-tool path "/memories/foo.txt"). Empty preserves the
	// historical default. Validation enforced at the request boundary.
	Key string

	// M12.1: observation_date is the in-conversation timestamp of the latest
	// message in the source batch (YYYY-MM-DD). Distinct from CreatedAt which
	// records server wall-clock at ingest. Empty when no chat_time was present
	// on any message — callers/consumers fall back to CreatedAt as before.
	//
	// Background: LoCoMo (and any replay-style benchmark with historic dates)
	// must answer questions like "when did X happen?" against the
	// conversation's own timeline, not server NOW. Surfacing this on memory
	// metadata lets retrieval clients prefix candidates with the real
	// conversation date instead of a stale ingest stamp.
	ObservationDate string

	// EventDates (F11): ISO-8601 (YYYY-MM-DD) calendar dates this fact
	// references — populated from the LLM extractor's "event_dates" field
	// after ISO validation. Stamped at TOP LEVEL of properties so the GIN
	// partial index from migration 0024 (`USING GIN ((properties::jsonb ->
	// 'event_dates'))`) actually catches it, and the F7 search-time
	// SearchMemoriesByDateRange query (internal/db/postgres_temporal.go)
	// can resolve "when X happened" questions against the conversation's
	// own calendar references. Empty/nil omits the field entirely (keeps
	// JSONB compact for facts with no temporal anchor).
	EventDates []string

	// ExtractionState (migration 0028): one of the extractionState* constants.
	// Always written by buildNodeProps. Callers must pass one of the
	// extractionState* constants — passing an empty string lands an empty value
	// in JSONB and is treated as a programming bug; not enforced at runtime.
	ExtractionState string
	// ExtractionAttemptedAt is the ISO-8601 timestamp of the last asynchronous
	// enrichment attempt, when one is wired in. Written only when non-empty
	// (absent when no async enrichment ran).
	ExtractionAttemptedAt string
	// ExtractionCompletedAt is the ISO-8601 timestamp of the last successful
	// asynchronous enrichment completion, when one is wired in. Written only
	// when non-empty (absent when no async enrichment ran).
	ExtractionCompletedAt string
}

// buildNodeProps constructs the JSONB properties dict matching the Python format.
func buildNodeProps(p memoryNodeProps) map[string]any {
	tags := []string{"mode:" + p.Mode}
	tags = append(tags, p.CustomTags...)

	props := map[string]any{
		"id":          p.ID,
		"memory":      p.Memory,
		"memory_type": p.MemoryType,
		"status":      "activated",
		// user_name is the cube partition key (upstream MemOS convention; populated from cube_id)
		"user_name":         p.UserName,
		"user_id":           p.UserID,
		"agent_id":          p.AgentID,
		"session_id":        p.SessionID,
		"created_at":        p.CreatedAt,
		"updated_at":        p.Now,
		"delete_time":       "",
		"delete_record_id":  "",
		"tags":              tags,
		"key":               p.Key,
		"usage":             []string{},
		"sources":           serializeSources(p.Sources),
		"background":        p.Background,
		"confidence":        0.99,
		"type":              "fact",
		"info":              p.Info,
		"graph_id":          uuid.New().String(),
		"importance_score":  1.0,
		"retrieval_count":   0,
		"last_retrieved_at": "",
		// D3 hierarchy defaults — new memories start as 'raw' (direct extraction).
		// TreeManager promotes clusters to 'episodic' and themes to 'semantic',
		// populating parent_memory_id on children at promotion time.
		"hierarchy_level":  "raw",
		"parent_memory_id": nil,
	}
	// D6: include raw_text only when provided — keeps payload lean for callers
	// that don't pass it (manual add, legacy paths).
	if p.RawText != "" {
		props["raw_text"] = p.RawText
	}
	// D8: preference_category populated only for PreferenceMemory entries.
	// Null-by-default keeps JSON size small and makes property introspection
	// explicit about which rows are categorised.
	if p.PreferenceCategory != "" {
		props["preference_category"] = p.PreferenceCategory
	}
	// M12.1: observation_date — only emit when known. Absence means
	// "no in-conversation timestamp available; use created_at" (the
	// pre-M12 behaviour).
	if p.ObservationDate != "" {
		props["observation_date"] = p.ObservationDate
	}
	// F11: event_dates — only emit when the extractor produced at least one
	// validated ISO date. Empty/nil keeps the property absent so the GIN
	// partial index from migration 0024 (predicate `WHERE properties ?
	// 'event_dates'`) excludes the row, matching the documented contract.
	// Cast through []any so json.Marshal emits a JSON array.
	if len(p.EventDates) > 0 {
		dates := make([]any, len(p.EventDates))
		for i, s := range p.EventDates {
			dates[i] = s
		}
		props["event_dates"] = dates
	}
	// Migration 0028: extraction_state is ALWAYS written so any future filler
	// SELECT on state='pending' hits the partial index rather than table-scanning.
	// The two timing fields are optional — absent on initial insert, set when
	// an async enrichment pass runs.
	props["extraction_state"] = p.ExtractionState
	if p.ExtractionAttemptedAt != "" {
		props["extraction_attempted_at"] = p.ExtractionAttemptedAt
	}
	if p.ExtractionCompletedAt != "" {
		props["extraction_completed_at"] = p.ExtractionCompletedAt
	}
	return props
}

// buildMemoryProperties is a convenience wrapper for fast-mode (created_at == updated_at).
// userName is the cube partition key; userID is the person identity (Phase 2 split).
// state must be one of the extractionState* constants (migration 0028).
// attemptedAt and completedAt are optional ISO-8601 timestamps; empty means "not yet".
func buildMemoryProperties(
	id, memory, memoryType, userName, userID, agentID, sessionID, timestamp string,
	info map[string]any, customTags []string,
	sources []map[string]any, background string,
	state, attemptedAt, completedAt string,
) map[string]any {
	return buildNodeProps(memoryNodeProps{
		ID:                    id,
		Memory:                memory,
		MemoryType:            memoryType,
		UserName:              userName,
		UserID:                userID,
		AgentID:               agentID,
		SessionID:             sessionID,
		Mode:                  modeFast,
		Now:                   timestamp,
		CreatedAt:             timestamp,
		Info:                  info,
		CustomTags:            customTags,
		Sources:               sources,
		Background:            background,
		ExtractionState:       state,
		ExtractionAttemptedAt: attemptedAt,
		ExtractionCompletedAt: completedAt,
	})
}

// buildSourcesFromMessages creates a sources slice from the raw messages list.
// Per-message uuid and agent_id propagate into source rows when supplied so
// downstream consumers (and future per-msg dedup) can address the original
// message. Empty values are preserved as empty (not synthesised) so absence
// stays distinguishable from "explicitly empty" at the source-of-truth level.
func buildSourcesFromMessages(messages []chatMessage) []map[string]any {
	sources := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = time.Now().UTC().Format("2006-01-02T15:04:05")
		}
		src := map[string]any{
			"role":      msg.Role,
			"content":   msg.Content,
			"chat_time": chatTime,
		}
		if msg.UUID != "" {
			src["uuid"] = msg.UUID
		}
		if msg.AgentID != "" {
			src["agent_id"] = msg.AgentID
		}
		sources = append(sources, src)
	}
	return sources
}

// serializeSources converts each source map to a JSON string, matching Python's format
// where each element in the sources array is a JSON-serialized string.
func serializeSources(sources []map[string]any) []string {
	if len(sources) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(sources))
	for _, src := range sources {
		b, err := json.Marshal(src)
		if err != nil {
			continue
		}
		result = append(result, string(b))
	}
	return result
}

// extractIDAndMemory parses a properties JSON blob to extract the id and memory fields.
// Used when building LLM candidate lists from vector search results.
func extractIDAndMemory(propertiesJSON string) (id, memory string) {
	var props map[string]any
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		return "", ""
	}
	id, _ = props["id"].(string)
	memory, _ = props["memory"].(string)
	return id, memory
}
