package handlers

// add_fast_event_dates.go — pure-regex YYYY-MM-DD extractor for fast/raw
// no-LLM paths. The fine path gets event_dates from the LLM extractor
// (atomic_extractor returns AtomicFact.EventDates). Fast/raw rows have
// nothing to feed the F7 SearchMemoriesByDateRange query unless we can
// pull a calendar anchor from the message text or chat_time stamp.
//
// Contract:
//   - Pattern: \b(\d{4})-(\d{2})-(\d{2})\b in the source text.
//   - Validation via time.Parse("2006-01-02") so impossible dates
//     (2025-13-01, 2024-02-30) are rejected, not silently indexed.
//   - chat_time anchor: when chatTime starts with YYYY-MM-DD prefix,
//     include it — every message has a temporal home.
//   - Output: deduped + sorted ASC. Empty input → nil (omits the property).
//
// Why not the LLM here: fast mode is the no-LLM zone (CLAUDE.md
// add-mode contract). Regex is enough to catch user-typed ISO dates and
// the conversation's own anchor — that's where 95% of "when did X happen"
// signal lives in the LoCoMo benchmark corpus.

import (
	"regexp"
	"sort"
	"time"
)

// isoDatePattern matches YYYY-MM-DD anywhere in text, with word boundaries
// so longer numeric runs don't false-match. Anchored \d{4}-\d{2}-\d{2} —
// month/day range validation happens in time.Parse, not the regex, to keep
// the pattern simple and the failure mode "rejected by parse" rather than
// "invisible to regex".
var isoDatePattern = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)

// extractEventDatesFromText returns a deduped, sorted list of ISO YYYY-MM-DD
// dates found in text plus the chat_time anchor (if the chat_time string
// has a 10-char YYYY-MM-DD prefix that parses cleanly). Returns nil when no
// valid dates were found — buildNodeProps omits the property in that case.
func extractEventDatesFromText(text, chatTime string) []string {
	seen := make(map[string]struct{}, 4)

	// Body scan — every regex hit gets validated through time.Parse so
	// impossible calendar dates (2025-02-30, 2024-13-01) drop out.
	for _, m := range isoDatePattern.FindAllString(text, -1) {
		if _, err := time.Parse("2006-01-02", m); err != nil {
			continue
		}
		seen[m] = struct{}{}
	}

	// chat_time anchor: chat_time is ISO-ish (YYYY-MM-DDTHH:MM:SS or longer).
	// Take the first 10 chars and revalidate; anything else is rejected.
	if len(chatTime) >= 10 {
		head := chatTime[:10]
		if _, err := time.Parse("2006-01-02", head); err == nil {
			seen[head] = struct{}{}
		}
	}

	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// extractEventDatesForFastMemory pulls dates from the first source's content
// + chat_time. Fast mode emits one extractedMemory per message in the
// per-message extractor (Sources len = 1), so source[0] is the canonical
// home. Returns nil when no signal — caller omits the property.
func extractEventDatesForFastMemory(mem extractedMemory) []string {
	if len(mem.Sources) == 0 {
		return nil
	}
	src := mem.Sources[0]
	content, _ := src["content"].(string)
	chatTime, _ := src["chat_time"].(string)
	if content == "" && chatTime == "" {
		return nil
	}
	return extractEventDatesFromText(content, chatTime)
}

// attributedToForFastMemory returns the speaker label (usually "user" or
// "assistant") from the first source row, or "" when sources are empty.
// Empty result omits the property so the partial index idx_memory_attributed_to
// excludes the row.
func attributedToForFastMemory(mem extractedMemory) string {
	if len(mem.Sources) == 0 {
		return ""
	}
	role, _ := mem.Sources[0]["role"].(string)
	return role
}

// isPerMessageFastMemory returns true when the extractedMemory came from
// extractFastMemoriesPerMessage (Sources len == 1) — distinct from the
// legacy windowed extractor which bundles N messages into a single row.
// The no-LLM enrichment fields (kind/attributed_to/event_dates/linked_ids/
// per-msg flatten) only make sense for the per-message granularity:
//
//   - attributed_to needs a single canonical speaker; window mode mixes them.
//   - per-msg metadata (chat_time/uuid/agent_id/role) needs a 1:1 source.
//   - kind='fast_msg' is the per-message discriminator (window rows look
//     more like the legacy paragraph format and stay implicitly indexed
//     under the migration default 'paragraph_legacy').
//
// Window-mode rows skip the lift and preserve their pre-2026-04-30 shape.
func isPerMessageFastMemory(mem extractedMemory) bool {
	return len(mem.Sources) == 1
}

// applyPerMessageFastInfo lifts the per-message metadata from sources[0] to
// TOP LEVEL of the info bag (mirroring add_raw.go::processRawMemory). The
// memInfo map is mutated in place and also returned for chaining clarity.
//
// Filter pushdown semantics: every existing search predicate that tests
// info.agent_id / info.uuid / info.chat_time / info.role on raw rows now
// works the same on per-message fast rows. Empty values stay absent so
// indices stay sparse and back-compat with legacy rows is preserved.
//
// No-op when mem is not a per-message row (sources empty or len > 1) — caller
// is expected to gate on isPerMessageFastMemory but the helper is defensive.
func applyPerMessageFastInfo(memInfo map[string]any, mem extractedMemory) map[string]any {
	if memInfo == nil || !isPerMessageFastMemory(mem) {
		return memInfo
	}
	src := mem.Sources[0]
	if v, _ := src["chat_time"].(string); v != "" {
		memInfo["chat_time"] = v
	}
	if v, _ := src["uuid"].(string); v != "" {
		memInfo["uuid"] = v
	}
	if v, _ := src["agent_id"].(string); v != "" {
		memInfo["agent_id"] = v
	}
	if v, _ := src["role"].(string); v != "" {
		memInfo["role"] = v
	}
	return memInfo
}
