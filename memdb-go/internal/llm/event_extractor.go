// Package llm — event_extractor.go: M11 F3 Memobase event extractor.
//
// Port of Memobase's `event_tagging` prompt
// (compete-research/memobase/src/server/api/memobase_server/prompts/event_tagging.py)
// — the system prompt is reproduced VERBATIM (substituting our own
// {event_tags} list and the literal `\t` tab separator).
//
// Output format the LLM emits:
//
//	[event_summary]
//	The user told the assistant they moved to Berlin to start a new job at a
//	consulting firm [mention 2024/03/12].
//
//	[event_tags]
//	- location\tBerlin
//	- goals\tStart a new consulting job
//	- emotion\texcited
//
// Two parallel signals are extracted: free-form `event_text` (with date anchor
// `[mention YYYY/MM/DD]`) and a tag list (canonical TAG → VALUE pairs). Tags
// are flattened into `["location:Berlin", "goals:..."]` so the postgres GIN
// index can do tag-overlap lookups without needing a JSONB column.
//
// The extractor is intentionally lightweight: one LLM call, single retry on
// empty parse, no merge/judge logic. Pure extraction — search-time injection
// is what turns it into a quality lift (see internal/search/event_inject.go).
package llm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	eventExtractMaxTokens = 4096
	eventMinMemoChars     = 32
	eventCallTimeout      = 60 * time.Second

	// Bounds matching profile_extractor sanitisation. Event summaries are
	// observable narrative ("user moved to Berlin"), not paragraphs.
	eventTextMaxLen = 1024
	eventTagMaxLen  = 64
)

// ErrEventEmptyConversation is returned when the conversation is too short to
// extract events from. Callers treat as a no-op (records "empty" outcome).
var ErrEventEmptyConversation = errors.New("event extract: conversation too short")

// Event is a single extracted event row, ready for db.InsertEventParams.
type Event struct {
	// EventText is the free-form summary. May contain a `[mention YYYY/MM/DD]`
	// anchor inline; that anchor is also parsed into EventDate when present.
	EventText string
	// EventDate is the resolved ISO date from the [mention …] anchor. nil when
	// no anchor was emitted by the LLM.
	EventDate *time.Time
	// Tags is the flattened tag list, "name:value" form. Empty = none.
	Tags []string
}

// EventExtractor wraps the LLM Client to produce Event rows.
type EventExtractor struct {
	client *Client
}

// NewEventExtractor returns an extractor that reuses the same Client (and
// therefore the same retry / model-fallback / metrics namespace) as the
// profile and atomic extractors.
func NewEventExtractor(c *Client) *EventExtractor { return &EventExtractor{client: c} }

// Model returns the underlying primary model name (telemetry).
func (e *EventExtractor) Model() string {
	if e == nil || e.client == nil {
		return ""
	}
	return e.client.Model()
}

// defaultEventTags is the canonical Memobase event tag list. Used when the
// caller does not supply project-specific tags (we don't have the Memobase
// "project profile" concept yet — F3.1 stretch).
var defaultEventTags = []string{
	"emotion(the user's current emotion)",
	"goals(the user's current goals or intentions)",
	"location(the location of the user)",
	"activity(what the user is doing or experiencing)",
	"relationship(people the user mentions and how they relate)",
	"preference(what the user likes or dislikes)",
}

// Extract parses `conversation` into a slice of Events. The blob is the raw
// conversation text (same shape every other extractor receives). `now` is
// used as a date anchor fallback when the LLM omits a [mention …] tag —
// callers pass the per-/add timestamp for consistency with bi-temporal edges.
//
// Returns (nil, ErrEventEmptyConversation) for trivially-short blobs.
// Returns ([], nil) when the LLM produced no parsable events — caller logs
// "empty" outcome.
func (e *EventExtractor) Extract(ctx context.Context, blob string, now time.Time) ([]Event, error) {
	if e == nil || e.client == nil {
		return nil, errors.New("EventExtractor: nil client")
	}
	memo := strings.TrimSpace(blob)
	if len(memo) < eventMinMemoChars {
		return nil, ErrEventEmptyConversation
	}

	system := buildEventSystemPrompt(defaultEventTags)
	msgs := []map[string]string{
		{"role": "system", "content": system},
		{"role": "user", "content": eventPackUser(memo, now)},
	}

	raw, err := e.client.Chat(ctx, msgs, eventExtractMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("event extract chat: %w", err)
	}

	out := parseEventResponse(raw, now)
	if len(out) > 0 {
		return out, nil
	}

	// Single retry — the Memobase prompt occasionally drops the section
	// markers. The reminder is short, the input is reused.
	retryMsgs := append(msgs[:len(msgs):len(msgs)], map[string]string{
		"role": "user",
		"content": "STRICTLY follow the format: a `[event_summary]` section with one line, " +
			"optionally containing `[mention YYYY/MM/DD]`, followed by a `[event_tags]` " +
			"section with `- TAG\tVALUE` lines. No prose around the sections.",
	})
	raw, err = e.client.Chat(ctx, retryMsgs, eventExtractMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("event extract retry: %w", err)
	}
	return parseEventResponse(raw, now), nil
}

// eventPackUser formats the user-side message for the LLM. Mirrors the
// pattern used by profilePackUser — gives the LLM both the conversation and
// today's date (so it can resolve relative phrasing like "yesterday").
func eventPackUser(memo string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("Today's date: ")
	sb.WriteString(now.UTC().Format("2006-01-02"))
	sb.WriteString("\n\n## Conversation\n")
	sb.WriteString(memo)
	sb.WriteString("\n\n## Output\n")
	sb.WriteString("Produce a `[event_summary]` line and a `[event_tags]` block as specified.")
	return sb.String()
}

// buildEventSystemPrompt formats Memobase's FACT_RETRIEVAL_PROMPT with our
// {event_tags} list. The wording (rules, examples, formatting block) is the
// upstream prompt verbatim — we add a short prefix that asks for the
// `[event_summary]` section because Memobase emits the summary from a
// SEPARATE prompt (entry_summary.py) and we collapse the two calls into one
// to stay inside the F3 latency budget (+100ms p95).
func buildEventSystemPrompt(eventTags []string) string {
	tagBody := strings.Join(eventTags, "\n")
	return `You are an expert of summarizing conversations into events and tagging them.

Given a conversation, produce TWO sections in this exact order:

[event_summary]
ONE concise sentence (≤40 words) describing what happened in this conversation
from the user's perspective. If the conversation references a specific date,
append the marker [mention YYYY/MM/DD] inline. If only a relative date is
mentioned ("yesterday", "last week"), resolve it against today's date and
emit the absolute marker. Omit the marker if no date is implied.

[event_tags]
- TAG` + "\t" + `VALUE
... one tag per line, prefixed with "- " ...

## Event Tags
Below are the event tags you may extract:
<event_tags>
` + tagBody + `
</event_tags>
each line is the tag name and (optionally) its description in parentheses.
The tag name is the leading identifier; description is the parenthetical.

### Rules
- Stick to the EXACT tag name; do not rename or invent tags.
- If a tag is not mentioned in the conversation, OMIT it from the output.
- VALUE is a short phrase (≤8 words). No JSON, no quotes, no nested structure.
- Output ONLY the two sections — no prose around them, no markdown headers.

### Example
Input:
> "I just moved to Berlin to start a new consulting job. I'm excited but a
> bit anxious about the language barrier."

Output:
[event_summary]
The user moved to Berlin to start a new consulting job and is excited but anxious about the language barrier.

[event_tags]
- location` + "\t" + `Berlin
- goals` + "\t" + `Start a new consulting job
- emotion` + "\t" + `excited but anxious

Now extract for the next conversation.`
}

// --- parser -----------------------------------------------------------------

// eventMentionRE captures the `[mention YYYY/MM/DD]` anchor inline in a
// summary line. The `/` delimiter matches Memobase's example format.
var eventMentionRE = regexp.MustCompile(`\[mention\s+(\d{4})[/-](\d{1,2})[/-](\d{1,2})\]`)

// eventTagLineRE matches a single tag line: `- TAG<TAB>VALUE`. We tolerate
// the bullet being `*` or `-` (LLMs flip between the two).
var eventTagLineRE = regexp.MustCompile(`^[\-\*]\s+([^\t\n]+)\t(.+)$`)

// parseEventResponse extracts one or more events from the LLM's reply.
// Tolerant to stray prose around the two sections; strict on the section
// markers themselves so a malformed reply produces an empty slice (caller
// triggers the retry or records "empty").
//
// We currently emit ONE event per reply (Memobase entry_summary is a
// per-blob-batch operation), but the parser supports multiple `[event_summary]`
// blocks so a future per-message variant works without parser changes.
func parseEventResponse(raw string, now time.Time) []Event {
	stripped := string(StripJSONFence([]byte(raw)))
	lower := strings.ToLower(stripped)

	// Find every "[event_summary]" header and parse the trailing block.
	var out []Event
	for {
		idx := strings.Index(lower, "[event_summary]")
		if idx < 0 {
			break
		}
		// Slice past the header on both the original and the lowered copy.
		stripped = stripped[idx+len("[event_summary]"):]
		lower = lower[idx+len("[event_summary]"):]

		// Find the matching tags header (or end of string).
		tagsIdx := strings.Index(lower, "[event_tags]")
		var summaryBlock, tagsBlock string
		if tagsIdx >= 0 {
			summaryBlock = stripped[:tagsIdx]
			tail := stripped[tagsIdx+len("[event_tags]"):]
			lowerTail := lower[tagsIdx+len("[event_tags]"):]
			// Stop the tags block at the next [event_summary] header (multi-event reply)
			// or end-of-string.
			nextIdx := strings.Index(lowerTail, "[event_summary]")
			if nextIdx >= 0 {
				tagsBlock = tail[:nextIdx]
				stripped = tail[nextIdx:]
				lower = lowerTail[nextIdx:]
			} else {
				tagsBlock = tail
				stripped = ""
				lower = ""
			}
		} else {
			summaryBlock = stripped
			stripped = ""
			lower = ""
		}

		ev, ok := buildEvent(summaryBlock, tagsBlock, now)
		if ok {
			out = append(out, ev)
		}
	}
	return out
}

// buildEvent assembles one Event from a summary block and a tags block.
// Returns (zero, false) when the summary text is empty after sanitisation.
func buildEvent(summaryBlock, tagsBlock string, now time.Time) (Event, bool) {
	// Summary: take the first non-blank line. Strip the [mention …] marker
	// from the stored text but parse it into EventDate.
	var summaryLine string
	for _, line := range strings.Split(summaryBlock, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		summaryLine = line
		break
	}
	if summaryLine == "" {
		return Event{}, false
	}

	var eventDate *time.Time
	if m := eventMentionRE.FindStringSubmatch(summaryLine); m != nil {
		// m[1]=YYYY m[2]=MM m[3]=DD
		layout := "2006-01-02"
		isoStr := fmt.Sprintf("%s-%02s-%02s", m[1], m[2], m[3])
		if t, err := time.Parse(layout, isoStr); err == nil {
			eventDate = &t
		}
		// Strip the marker from the stored text — it's denormalized into EventDate.
		summaryLine = strings.TrimSpace(eventMentionRE.ReplaceAllString(summaryLine, ""))
	}
	// Length cap. Truncate (not reject) so a single overlong narrative is
	// still surfaced — same policy as buildProfileEntry.
	if t, truncated := truncateRune(summaryLine, eventTextMaxLen); truncated {
		summaryLine = t
	}
	if summaryLine == "" {
		return Event{}, false
	}

	// Tags: parse "- TAG\tVALUE" lines, flatten to "tag:value".
	var tags []string
	for _, line := range strings.Split(tagsBlock, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := eventTagLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		tag := strings.ToLower(strings.TrimSpace(m[1]))
		val := strings.TrimSpace(m[2])
		if tag == "" || val == "" {
			continue
		}
		// Strip parenthetical descriptions if the LLM echoed the tag spec.
		if i := strings.Index(tag, "("); i > 0 {
			tag = strings.TrimSpace(tag[:i])
		}
		flat := tag + ":" + val
		if t, _ := truncateRune(flat, eventTagMaxLen); t != "" {
			flat = t
		}
		tags = append(tags, flat)
	}

	// No fallback when LLM omits [mention …]: leaving EventDate nil writes
	// NULL to event_date and the partial btree index (WHERE event_date IS
	// NOT NULL) skips the row on date-window lookups. Stamping `now` here
	// would make every freshly-extracted un-anchored event match every
	// cat-4 query within the ±7d search window — false positives on the
	// metric F3 targets.

	return Event{
		EventText: summaryLine,
		EventDate: eventDate,
		Tags:      tags,
	}, true
}
