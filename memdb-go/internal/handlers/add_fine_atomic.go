// Package handlers — M11 F8 atomic-fact extraction wiring.
//
// When MEMDB_ATOMIC_FACTS=true is set, runFineExtractionAtomic replaces
// runFineExtraction inside nativeFineAddForCube. It calls mem0's verbatim
// ADDITIVE_EXTRACTION_PROMPT (see internal/llm/atomic_extractor.go), parses
// the {"memory":[...]} envelope into AtomicFact objects, and converts each
// to an ExtractedFact so the rest of the add pipeline (embed → apply →
// persist → entity link → fanout) consumes them unchanged.
//
// Conversion contract (atomic → extracted):
//   - Memory      <- AtomicFact.Text        (15-80 word self-contained statement)
//   - Type        =  "LongTermMemory"        (atomic facts are always long-term)
//   - Action      =  llm.MemAdd              (additive: never UPDATE/DELETE)
//   - Confidence  =  0.9                     (above MinConfidence so parse keeps it)
//   - Tags        =  AtomicFact.LinkedMemoryIDs (passed through to entity linker via Info too)
//   - Info["kind"]              = "atomic_fact"   (mirrored to properties->>'kind' column)
//   - Info["attributed_to"]     = AtomicFact.AttributedTo
//   - Info["linked_memory_ids"] = AtomicFact.LinkedMemoryIDs
//   - Info["event_dates"]       = AtomicFact.EventDates
//
// Legacy path (paragraph_legacy) is preserved byte-for-byte when the env flag
// is unset — the env check is the only branch in nativeFineAddForCube. Old
// rows inserted before this migration carry the column DEFAULT and stay
// queryable as kind='paragraph_legacy'.
package handlers

import (
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// atomicFactsEnvVar is the staged-rollout flag for F8. Default off so the
// legacy single-paragraph path stays in production until we A/B at corpus
// size on LoCoMo.
const atomicFactsEnvVar = "MEMDB_ATOMIC_FACTS"

// atomicFactConfidence is the synthetic confidence we stamp on every
// converted AtomicFact. mem0's ADDITIVE prompt does not emit confidence —
// the prompt's design assumption is that everything it returns is worth
// keeping. We pin it above llm.MinConfidence (0.65) so parseExtractedFacts'
// downstream filters never drop the fact.
const atomicFactConfidence = 0.9

// atomicFactKind is the value stored in properties->>'kind' for every
// atomic-extracted memory. Migration 0022 makes the kind column a
// generated expression of this property.
const atomicFactKind = "atomic_fact"

// atomicFactsEnabled returns true when MEMDB_ATOMIC_FACTS is set to a
// truthy value. Mirrors the dateAwareExtractEnabled / profileExtractEnabled
// patterns elsewhere in this package.
func atomicFactsEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(atomicFactsEnvVar)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// atomicExtractorCache holds the lazy singleton. Keyed off the underlying
// llm.Client pointer because tests inject fresh extractors per case and we
// want a fresh wrapper when that happens.
type atomicExtractorCacheT struct {
	mu      sync.Mutex
	client  *llm.Client
	wrapper *llm.AtomicExtractor
}

var atomicExtractorCache atomicExtractorCacheT //nolint:gochecknoglobals // cache singleton

// getAtomicExtractor returns the cached AtomicExtractor for the handler's
// configured LLM client. Re-allocates if the underlying client pointer
// changed (test re-init).
func (h *Handler) getAtomicExtractor() *llm.AtomicExtractor {
	if h.llmExtractor == nil {
		return nil
	}
	c := h.llmExtractor.Client()
	atomicExtractorCache.mu.Lock()
	defer atomicExtractorCache.mu.Unlock()
	if atomicExtractorCache.wrapper == nil || atomicExtractorCache.client != c {
		atomicExtractorCache.client = c
		atomicExtractorCache.wrapper = llm.NewAtomicExtractor(c)
	}
	return atomicExtractorCache.wrapper
}

// atomicToExtracted converts a mem0 AtomicFact into the ExtractedFact shape
// the rest of the add pipeline already understands. The only F8-specific
// fields (kind, attributed_to, linked_memory_ids, event_dates) ride along
// inside ExtractedFact via a sidecar map written by the caller — see the
// hand-off in nativeFineAddForCube where Info gets merged into factInfo
// during buildAddNodes.
//
// We use Tags as a no-op pass-through (empty) — the legacy hallucination
// filter expects non-empty Tags but is gated behind the legacy path. In
// the atomic path we skip filterHallucinatedFacts entirely (the
// ADDITIVE prompt's integrity rules already forbid fabrication).
func atomicToExtracted(f llm.AtomicFact) llm.ExtractedFact {
	return llm.ExtractedFact{
		Memory:       f.Text,
		ResolvedText: f.Text,
		RawText:      f.Text, // mem0 doesn't separate raw/resolved; same value goes both ways for audit consistency.
		Type:         "LongTermMemory",
		Action:       llm.MemAdd,
		Confidence:   atomicFactConfidence,
		// Tags are populated from the topic_tag plus a marker so the existing
		// search-side tag filters still see the fact.
		Tags: tagsForAtomic(f),
		// Promote NamedEntitiesInText to Entities so the shared
		// linkEntitiesAsync path (collectHandlerEntityPairs filters on
		// len(Entities)>0) upserts entity_nodes + MENTIONS_ENTITY edges
		// for atomic facts. Without this, query-time NER lookups
		// (GetMemoriesByEntityIDs) returned 0 entities for atomic-only
		// cubes, skipping AGE graph traversal entirely and degrading
		// cat2 multi-hop recall ("Melanie's pets") to plain dense+sparse.
		Entities: namedEntitiesToMentions(f.NamedEntitiesInText),
	}
}

// namedEntitiesToMentions converts the LLM's flat string list of proper nouns
// into typed EntityMention rows. The atomic prompt does NOT classify entity
// types (Mem0 ADDITIVE schema returns names only), so we apply a tiny
// heuristic at convert time:
//
//   - quoted strings (`"Becoming Nicole"`, `“…”`) → "WORK"   (titles)
//   - all-uppercase tokens of len>=2                → "ORG"    (acronyms)
//   - single-token capitalized name                 → "PERSON" (default)
//   - everything else                                → "ENTITY" (safe fallback)
//
// The type label primarily drives entity_type analytics — search/recall
// uses normalized id (lowercased name) for joins, so misclassification
// degrades reporting only, not retrieval. Empty input → nil (no-op).
func namedEntitiesToMentions(names []string) []llm.EntityMention {
	if len(names) == 0 {
		return nil
	}
	out := make([]llm.EntityMention, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		// Dedup within the same fact — the LLM occasionally repeats a
		// proper noun across enumerations. Idempotency is the upsert
		// layer's job, but cheaper to filter here.
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, llm.EntityMention{
			Name: stripEntityQuotes(name),
			Type: inferEntityType(name),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stripEntityQuotes removes wrapping ASCII or smart quotes from a name so
// the canonical form stored in entity_nodes.name doesn't double up on
// punctuation when the LLM emits `"Becoming Nicole"` verbatim.
func stripEntityQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	pairs := [][2]rune{{'"', '"'}, {'\'', '\''}, {'“', '”'}, {'«', '»'}}
	r := []rune(s)
	for _, p := range pairs {
		if r[0] == p[0] && r[len(r)-1] == p[1] {
			return strings.TrimSpace(string(r[1 : len(r)-1]))
		}
	}
	return s
}

// inferEntityType returns a coarse type label for a raw entity string from
// NamedEntitiesInText. See namedEntitiesToMentions for the heuristic table.
func inferEntityType(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "ENTITY"
	}
	// Quoted → title of a work / book / film / song.
	if len(trimmed) >= 2 {
		first, last := trimmed[0], trimmed[len(trimmed)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return "WORK"
		}
		// Smart quotes (UTF-8 multi-byte): cheap byte-prefix check.
		if strings.HasPrefix(trimmed, "“") && strings.HasSuffix(trimmed, "”") {
			return "WORK"
		}
	}
	// All-uppercase short token → acronym/org (NASA, IBM).
	if isAllUpper(trimmed) && len(trimmed) >= 2 {
		return "ORG"
	}
	// Single-token capitalized word → PERSON default.
	if !strings.ContainsAny(trimmed, " \t") {
		runes := []rune(trimmed)
		if len(runes) > 0 && unicode.IsUpper(runes[0]) {
			return "PERSON"
		}
	}
	return "ENTITY"
}

// isAllUpper returns true when every letter rune in s is uppercase. Digits
// and punctuation are ignored so "F-35" still classifies as ORG.
func isAllUpper(s string) bool {
	hasLetter := false
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		hasLetter = true
		if !unicode.IsUpper(r) {
			return false
		}
	}
	return hasLetter
}

// tagsForAtomic builds the Tag list. Always includes "atomic_fact" plus
// the optional topic tag, plus a "speaker:<who>" tag for EVERY non-empty
// AttributedTo (canonical "user"/"assistant" included). Asymmetric
// suppression of canonical roles made those facts unfilterable on the
// search path — removed so per-speaker scope works uniformly.
func tagsForAtomic(f llm.AtomicFact) []string {
	tags := []string{atomicFactKind}
	if f.TopicTag != "" {
		tags = append(tags, f.TopicTag)
	}
	if f.AttributedTo != "" {
		tags = append(tags, "speaker:"+f.AttributedTo)
	}
	return tags
}

// atomicInfoFromFact returns the per-fact Info-map fragment that the add
// pipeline merges into properties.info during node construction. Called
// from the env-on branch of nativeFineAddForCube right before
// applyAndPersistFineFacts.
//
// The map writes properties->>'kind' = 'atomic_fact' (mirrored to the
// generated kind column by migration 0022) plus the F8-specific soft-edge
// metadata so F12 can wire memory_edges from the JSONB without re-parsing
// the LLM output.
func atomicInfoFromFact(f llm.AtomicFact) map[string]any {
	out := map[string]any{
		"kind": atomicFactKind,
	}
	if f.AttributedTo != "" {
		out["attributed_to"] = f.AttributedTo
	}
	if len(f.LinkedMemoryIDs) > 0 {
		// Cast through []any so json.Marshal in marshalProps emits a JSON array.
		ids := make([]any, len(f.LinkedMemoryIDs))
		for i, s := range f.LinkedMemoryIDs {
			ids[i] = s
		}
		out["linked_memory_ids"] = ids
	}
	if len(f.EventDates) > 0 {
		dates := make([]any, len(f.EventDates))
		for i, s := range f.EventDates {
			dates[i] = s
		}
		out["event_dates"] = dates
	}
	if len(f.NamedEntitiesInText) > 0 {
		// v2 schema-bind: persisted so downstream observability can verify
		// the LLM actually populated the field (memdb_atomic_named_entity_count
		// histogram derives from this — empty arrays are excluded by the
		// len() > 0 gate, mirroring EventDates).
		ents := make([]any, len(f.NamedEntitiesInText))
		for i, s := range f.NamedEntitiesInText {
			ents[i] = s
		}
		out["named_entities_in_text"] = ents
	}
	return out
}

// liftAtomicDiscriminators copies the F8-specific discriminator keys from a
// per-fact info bag onto the TOP LEVEL of the JSONB props map produced by
// buildNodeProps. This is required because:
//   - migration 0022 generates the `kind` column from properties->>'kind'
//     (top-level) — nested properties.info.kind is invisible to it and every
//     atomic row degrades to kind='paragraph_legacy';
//   - idx_memory_linked_ids is a GIN index on (properties -> 'linked_memory_ids')
//     and never matches values nested inside `info`;
//   - idx_memory_attributed_to is a partial index on properties->>'attributed_to';
//   - migration 0024 (F11) creates a GIN partial index on (properties ->
//     'event_dates') predicated on `properties ? 'event_dates'` — the same
//     top-level lift contract applies, otherwise SearchMemoriesByDateRange
//     never matches.
//
// We also keep the values inside `info` so existing JSONB-property tests and
// any downstream consumers reading info.kind still see them — the cost of the
// duplicated keys is a few bytes per row.
func liftAtomicDiscriminators(props map[string]any, factInfo map[string]any) {
	if props == nil || factInfo == nil {
		return
	}
	if v, ok := factInfo["kind"]; ok {
		props["kind"] = v
	}
	if v, ok := factInfo["linked_memory_ids"]; ok {
		props["linked_memory_ids"] = v
	}
	if v, ok := factInfo["attributed_to"]; ok {
		props["attributed_to"] = v
	}
	if v, ok := factInfo["event_dates"]; ok {
		props["event_dates"] = v
	}
}

// applyAtomicInfoToFacts merges F8 Info fragments into a list of
// ExtractedFacts paired with their source AtomicFacts. Used by the env-on
// branch of nativeFineAddForCube to inject kind/attributed_to/linked_ids
// into each fact's existing factInfo bag right before persistence.
//
// Returns a fresh slice so the caller can keep the original input intact
// (the embed pipeline mutates ContentHash on the slice items).
func applyAtomicInfoToFacts(
	atomicFacts []llm.AtomicFact,
	extractedFacts []llm.ExtractedFact,
) []map[string]any {
	if len(atomicFacts) != len(extractedFacts) {
		// Length mismatch shouldn't happen because runAtomicFineExtraction
		// builds them in lock-step; defensive zero-slice keeps the caller
		// from panicking.
		return nil
	}
	out := make([]map[string]any, len(atomicFacts))
	for i := range atomicFacts {
		out[i] = atomicInfoFromFact(atomicFacts[i])
	}
	return out
}
