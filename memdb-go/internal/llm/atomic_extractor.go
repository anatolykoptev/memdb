// Package llm — atomic_extractor.go: M11 F8 mem0 ADDITIVE_PROMPT port.
//
// This is the atomic-fact extractor that replaces the condensed
// `LongTermMemory` paragraph extraction when MEMDB_ATOMIC_FACTS=true.
// It is a straight port of mem0's ADDITIVE_EXTRACTION_PROMPT
// (mem0/mem0/configs/prompts.py, lines 468-944) — the prompt body is
// reproduced verbatim. Do NOT paraphrase; the wording carries calibration
// signal (anti-aggregation rule, 5-15 facts per chunk, multi-speaker
// attribution, transition preservation).
//
// Output shape mirrors mem0's:
//
//	{"memory": [
//	  {"id": "0", "text": "...", "attributed_to": "user",
//	   "linked_memory_ids": ["uuid-of-related-existing-memory"]}
//	]}
//
// AtomicFact is the parsed Go-side struct. Conversion to ExtractedFact
// (so the rest of the add pipeline can consume it unchanged) lives in
// internal/handlers/add_fine_atomic.go.
package llm

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ADDITIVE_EXTRACTION_PROMPT is a verbatim copy of
// mem0/mem0/configs/prompts.py::ADDITIVE_EXTRACTION_PROMPT (lines 468-944).
//
// It lives in additive_extraction_prompt.txt (33 KB, embedded at build time)
// so the Go source stays readable and so future prompt-version diffs are
// visible in `git log -- additive_extraction_prompt.txt`. Do NOT edit the
// .txt file in-place — re-extract from the upstream source if you bump.
//
// Source: https://github.com/mem0ai/mem0/blob/main/mem0/configs/prompts.py
//
//go:embed additive_extraction_prompt.txt
var ADDITIVE_EXTRACTION_PROMPT string

// AtomicFact is one mem0-style atomic memory: a 15-80-word self-contained
// factual statement with optional speaker attribution and soft graph edges.
type AtomicFact struct {
	// ID is the per-extraction sequential id from the LLM ("0", "1", ...).
	// Not a UUID — used only to disambiguate facts within one batch.
	ID string `json:"id"`
	// Text is the 15-80-word factual statement (up to ~100 for detail-rich content).
	Text string `json:"text"`
	// AttributedTo is the speaker the memory belongs to: "user" or "assistant"
	// (mem0 default), OR a named speaker for multi-speaker conversations
	// (Example 12: "John", "Maria"). Required by the prompt.
	AttributedTo string `json:"attributed_to,omitempty"`
	// LinkedMemoryIDs is the set of EXISTING memory UUIDs this fact links to
	// (same entity, updated preference, continuation, or contradiction).
	// Soft graph edges populated at extract time. F12 will wire these into
	// the memory_edges table; for now they are stored on the fact properties.
	LinkedMemoryIDs []string `json:"linked_memory_ids,omitempty"`
	// TopicTag is an optional single-word topic tag — not in mem0's spec but
	// useful for downstream classification. Populated opportunistically.
	TopicTag string `json:"topic_tag,omitempty"`
	// EventDates are ISO-8601 dates the fact references (resolved against
	// observation_date). Empty if the fact has no temporal anchor.
	EventDates []string `json:"event_dates,omitempty"`
}

// atomicFactsResponse mirrors the {"memory": [...]} envelope mem0 emits.
type atomicFactsResponse struct {
	Memory []AtomicFact `json:"memory"`
}


// AtomicExtractor is the F8 atomic-fact extractor. It wraps the existing
// LLM Client, calls ChatStructured under the "atomic_facts" prompt id (so
// the structured-call metrics show up under that label), and returns
// validated AtomicFact slices.
type AtomicExtractor struct {
	client *Client
}

// NewAtomicExtractor builds an AtomicExtractor that reuses the supplied
// chat Client. Pass the same client used by LLMExtractor so credentials
// and fallback model lists stay in sync.
func NewAtomicExtractor(c *Client) *AtomicExtractor {
	return &AtomicExtractor{client: c}
}

// Client returns the underlying chat client. Exposed so tests can swap in
// httptest-backed transports without rebuilding the wrapper.
func (e *AtomicExtractor) Client() *Client { return e.client }

// AtomicWordMin / AtomicWordMax bracket the per-fact word-count window the
// mem0 prompt promises (15-80 with up to 100 for detail-rich content).
// Validation is lenient: out-of-window facts log a warning but still ship.
const (
	AtomicWordMin     = 15
	AtomicWordMax     = 100
	atomicMaxTokens   = 8192
	atomicCallTimeout = 60 * time.Second
)

// ExtractAtomicFacts runs the ADDITIVE_EXTRACTION_PROMPT over `conversation`.
//
// candidates is the list of existing memories (UUID + text) the LLM may use
// for soft-link references — populated from the same VSET / pgvector top-N
// path the legacy extractor uses. observationDate is the conversation
// timestamp ("YYYY-MM-DD"); empty defaults to time.Now() UTC.
//
// Validation:
//   - empty Text is dropped.
//   - facts outside [AtomicWordMin, AtomicWordMax] words are KEPT (logged as
//     a warning by the caller — mem0's prompt explicitly allows up to ~100
//     for detail-rich content, and we'd rather have a slightly long fact
//     than lose it). Bench histograms record the actual distribution.
//   - linked_memory_ids that fail uuid.Parse are dropped silently.
func (e *AtomicExtractor) ExtractAtomicFacts(
	ctx context.Context,
	conversation string,
	candidates []Candidate,
	observationDate string,
) ([]AtomicFact, error) {
	if e == nil || e.client == nil {
		return nil, errors.New("AtomicExtractor: nil client")
	}
	if observationDate == "" {
		observationDate = time.Now().UTC().Format("2006-01-02")
	}

	user := buildAtomicUserMessage(conversation, candidates, observationDate)

	msgs := []Message{
		{Role: "system", Content: ADDITIVE_EXTRACTION_PROMPT},
		{Role: "user", Content: user},
	}

	var resp atomicFactsResponse
	err := ChatStructured(ctx, e.client, "atomic_facts", msgs, &resp,
		WithMaxTokens(atomicMaxTokens),
		WithTimeout(atomicCallTimeout),
		WithMaxRetries(1),
	)
	if err != nil {
		return nil, fmt.Errorf("atomic extract: %w", err)
	}

	out := make([]AtomicFact, 0, len(resp.Memory))
	for _, f := range resp.Memory {
		f.Text = strings.TrimSpace(f.Text)
		if f.Text == "" {
			continue
		}
		f.AttributedTo = strings.TrimSpace(f.AttributedTo)
		f.LinkedMemoryIDs = filterValidUUIDs(f.LinkedMemoryIDs)
		out = append(out, f)
	}
	return out, nil
}

// Model returns the configured chat model name for telemetry.
func (e *AtomicExtractor) Model() string {
	if e == nil || e.client == nil {
		return ""
	}
	return e.client.Model()
}

// buildAtomicUserMessage assembles the user-side prompt body. We don't run
// the full mem0 prompt-builder (Summary / Recently Extracted / Last k
// Messages) — those slots live elsewhere and will be wired in F12. For F8
// we pass conversation + existing memories + observation/current dates.
func buildAtomicUserMessage(conversation string, candidates []Candidate, observationDate string) string {
	var sb strings.Builder
	sb.WriteString("## New Messages\n")
	sb.WriteString(conversation)
	sb.WriteString("\n\n## Existing Memories\n")
	if len(candidates) == 0 {
		sb.WriteString("[]")
	} else {
		sb.WriteString("[")
		for i, c := range candidates {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, `{"id": %q, "text": %q}`, c.ID, c.Memory)
		}
		sb.WriteString("]")
	}
	fmt.Fprintf(&sb, "\n\n## Observation Date\n%s\n", observationDate)
	fmt.Fprintf(&sb, "\n## Current Date\n%s\n", time.Now().UTC().Format("2006-01-02"))
	return sb.String()
}

// filterValidUUIDs drops entries that don't parse as UUIDs. Empty input
// returns nil (omits the field from the JSONB blob downstream).
func filterValidUUIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := uuid.Parse(s); err != nil {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CountWords returns a whitespace-split word count. Used by tests + the
// fact_word_count histogram to verify the mem0 spec window.
func CountWords(s string) int {
	if s = strings.TrimSpace(s); s == "" {
		return 0
	}
	return len(strings.Fields(s))
}

// HasProperNoun is a lenient heuristic: any whitespace-split token that
// starts with an uppercase letter (after trimming surrounding punctuation)
// counts. Used to log a warning when a fact lacks any apparent named
// entity — never to reject the fact (mem0's prompt is the ground truth).
func HasProperNoun(s string) bool {
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, `"',.;:!?()[]{}`)
		if w == "" {
			continue
		}
		// Skip "I" / "User" / sentence-initial filler? The fact is
		// considered to have a proper noun if ANY token starts uppercase
		// AND is not the first token of the sentence.
		r := []rune(w)[0]
		if r >= 'A' && r <= 'Z' {
			return true
		}
		// Cyrillic capitals — Russian content also has proper nouns.
		if r >= 'А' && r <= 'Я' {
			return true
		}
	}
	return false
}
