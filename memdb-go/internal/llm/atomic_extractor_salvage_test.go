package llm

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// truncateForLog caps long inputs in test failure messages so CI log isn't
// overwhelmed by the perf-test 12 KB payloads.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(" + fmt.Sprintf("%d more bytes", len(s)-n) + ")"
}

// salvageCase is a table-test row for the main salvage suite.
type salvageCase struct {
	name     string
	raw      string
	wantN    int                                    // number of facts recovered (post empty-Text filter)
	wantTier int                                    // expected tier (salvageTierOne|Two|None) — use 0 to skip
	verify   func(t *testing.T, facts []AtomicFact) // optional deep assertions
}

// TestSalvageAtomicFacts is the umbrella table covering production failure
// modes: clean envelopes, fence-wrapped, prose-prefixed, broken envelopes
// recovered via tier-2, garbage rejected, edge cases on empty/null/typo.
func TestSalvageAtomicFacts(t *testing.T) {
	cases := []salvageCase{
		// ── happy paths (tier-1 should fire) ──────────────────────────────
		{
			name:     "clean envelope",
			raw:      `{"memory":[{"id":"0","text":"User has a cat named Oliver"}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
			verify: func(t *testing.T, facts []AtomicFact) {
				if facts[0].Text != "User has a cat named Oliver" {
					t.Errorf("text mismatch: %q", facts[0].Text)
				}
			},
		},
		{
			name:     "markdown json fence",
			raw:      "```json\n{\"memory\":[{\"id\":\"0\",\"text\":\"User likes hiking\"}]}\n```",
			wantN:    1,
			wantTier: salvageTierOne,
		},
		{
			name:     "prose preamble before JSON",
			raw:      `Sure, here is the JSON: {"memory":[{"id":"0","text":"User runs marathons"}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
		},

		// ── multi-byte / escape edge cases ────────────────────────────────
		{
			name:     "cyrillic text (Russian production data)",
			raw:      `{"memory":[{"id":"0","text":"Пользователь Марк работает в Шопифай"}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
			verify: func(t *testing.T, facts []AtomicFact) {
				if facts[0].Text != "Пользователь Марк работает в Шопифай" {
					t.Errorf("cyrillic mangled: %q", facts[0].Text)
				}
			},
		},
		{
			name:     "emoji in text",
			raw:      `{"memory":[{"id":"0","text":"User loves coffee ☕ and works late 🌙"}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
			verify: func(t *testing.T, facts []AtomicFact) {
				if !strings.Contains(facts[0].Text, "☕") || !strings.Contains(facts[0].Text, "🌙") {
					t.Errorf("emoji lost: %q", facts[0].Text)
				}
			},
		},
		{
			name:     "escaped quotes in text via tier-1",
			raw:      `{"memory":[{"id":"0","text":"User said \"hello\" to Mark"}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
			verify: func(t *testing.T, facts []AtomicFact) {
				want := `User said "hello" to Mark`
				if facts[0].Text != want {
					t.Errorf("escaped-quote text mismatch: got %q, want %q", facts[0].Text, want)
				}
			},
		},
		{
			name:     "escaped quotes survive tier-2 regex",
			raw:      `garbage prefix {"id":"0","text":"He said \"yes\""} more garbage suffix`,
			wantN:    1,
			wantTier: salvageTierTwo,
			verify: func(t *testing.T, facts []AtomicFact) {
				want := `He said "yes"`
				if facts[0].Text != want {
					t.Errorf("regex tier-2 escaped-quote: got %q, want %q", facts[0].Text, want)
				}
			},
		},
		{
			name:     "multi-paragraph text with newlines",
			raw:      `{"memory":[{"id":"0","text":"Line one.\nLine two.\nLine three."}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
		},

		// ── all optional fields populated (struct shape regression guard) ─
		{
			name: "all optional fields populated",
			raw: `{"memory":[{"id":"0","text":"Mark hired Elena","attributed_to":"narrator",` +
				`"named_entities_in_text":["Mark","Elena"],"linked_memory_ids":["e0e0e0e0-1111-2222-3333-444444444444"],` +
				`"event_dates":["2026-01-15"]}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
			verify: func(t *testing.T, facts []AtomicFact) {
				f := facts[0]
				if f.AttributedTo != "narrator" {
					t.Errorf("attributed_to lost: %q", f.AttributedTo)
				}
				if len(f.NamedEntitiesInText) != 2 || f.NamedEntitiesInText[0] != "Mark" {
					t.Errorf("named_entities_in_text mangled: %+v", f.NamedEntitiesInText)
				}
				if len(f.LinkedMemoryIDs) != 1 {
					t.Errorf("linked_memory_ids count: %+v", f.LinkedMemoryIDs)
				}
				if len(f.EventDates) != 1 || f.EventDates[0] != "2026-01-15" {
					t.Errorf("event_dates: %+v", f.EventDates)
				}
			},
		},

		// ── tier-2 recovery scenarios ─────────────────────────────────────
		{
			name:     "trailing comma, broken envelope → per-line recovery",
			raw:      `{"memory":[{"id":"0","text":"Fact A"},{"id":"1","text":"Fact B"},]}`,
			wantN:    2,
			wantTier: salvageTierTwo,
			verify: func(t *testing.T, facts []AtomicFact) {
				if facts[0].Text != "Fact A" || facts[1].Text != "Fact B" {
					t.Errorf("ordering wrong: %q, %q", facts[0].Text, facts[1].Text)
				}
			},
		},
		{
			name:     "missing closing brace, partial recovery",
			raw:      `{"memory":[{"id":"0","text":"Recovered fact"},{"id":"1","text":"Other fact"`,
			wantN:    1, // only first object is complete
			wantTier: salvageTierTwo,
			verify: func(t *testing.T, facts []AtomicFact) {
				if facts[0].Text != "Recovered fact" {
					t.Errorf("first survivor wrong: %q", facts[0].Text)
				}
			},
		},
		{
			name:     "bare array no envelope, tier-2 recovers",
			raw:      `[{"id":"0","text":"Fact A"},{"id":"1","text":"Fact B"}]`,
			wantN:    2,
			wantTier: salvageTierTwo,
		},
		{
			name:     "memories plural typo, tier-2 falls through",
			raw:      `{"memories":[{"id":"0","text":"Tolerated typo fact"}]}`,
			wantN:    1,
			wantTier: salvageTierTwo,
		},
		{
			name: "mixed valid/invalid facts in envelope",
			raw: `{"memory":[{"id":"0","text":"Good A"},{"id":"1","text":bad},` +
				`{"id":"2","text":"Good C"}]}`,
			wantN:    2,
			wantTier: salvageTierTwo,
		},

		// ── reject paths ──────────────────────────────────────────────────
		{
			name:     "garbage with no JSON at all",
			raw:      `I'm sorry, I cannot process this request.`,
			wantN:    0,
			wantTier: salvageTierNone,
		},
		{
			name:     "empty input",
			raw:      "",
			wantN:    0,
			wantTier: salvageTierNone,
		},
		{
			name:     "memory null",
			raw:      `{"memory":null}`,
			wantN:    0,
			wantTier: salvageTierNone,
		},
		{
			name:     "memory empty array",
			raw:      `{"memory":[]}`,
			wantN:    0,
			wantTier: salvageTierNone,
		},
		{
			name:     "envelope present but every fact has empty text",
			raw:      `{"memory":[{"id":"0","text":""},{"id":"1","text":"   "}]}`,
			wantN:    0,
			wantTier: salvageTierNone,
		},
		{
			name:     "fact with empty text dropped, others survive (tier-1)",
			raw:      `{"memory":[{"id":"0","text":""},{"id":"1","text":"Real fact"}]}`,
			wantN:    1,
			wantTier: salvageTierOne,
		},

		// ── nested braces inside quoted text are tolerated by tier-2 ─────
		// because regex `(?:[^"\\]|\\.)*` matches anything except unescaped
		// quotes, INCLUDING braces. Documented strength, not limitation.
		{
			name:     "nested braces in quoted text recover via tier-2",
			raw:      `garbage {"id":"0","text":"price {discount:10}"} end`,
			wantN:    1,
			wantTier: salvageTierTwo,
			verify: func(t *testing.T, facts []AtomicFact) {
				if facts[0].Text != "price {discount:10}" {
					t.Errorf("nested-brace text mangled: %q", facts[0].Text)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, tier := salvageWithTier(tc.raw)
			if len(facts) != tc.wantN {
				t.Fatalf("got %d facts, want %d (tier=%d, raw=%s, parsed=%+v)",
					len(facts), tc.wantN, tier, truncateForLog(tc.raw, 200), facts)
			}
			if tc.wantTier != 0 && tier != tc.wantTier {
				t.Errorf("got tier=%d, want tier=%d (raw=%s)",
					tier, tc.wantTier, truncateForLog(tc.raw, 200))
			}
			if tc.verify != nil && len(facts) > 0 {
				tc.verify(t, facts)
			}
		})
	}
}

// TestSalvageAtomicFacts_LLMRamble pins recovery from prose-interleaved
// per-line objects — the most common Flash-lite failure pattern observed in
// production logs.
func TestSalvageAtomicFacts_LLMRamble(t *testing.T) {
	raw := `Here are the facts I extracted from the conversation:
{"id":"0","text":"User Marcus was promoted at Shopify"}
{"id":"1","text":"Marcus is married to Elena"}
That's all I could find.`
	facts, tier := salvageWithTier(raw)
	if len(facts) != 2 {
		t.Fatalf("expected 2 salvaged facts, got %d: %+v", len(facts), facts)
	}
	if tier != salvageTierTwo {
		t.Errorf("expected tier-2, got tier=%d", tier)
	}
	want := []string{
		"User Marcus was promoted at Shopify",
		"Marcus is married to Elena",
	}
	for i, w := range want {
		if facts[i].Text != w {
			t.Errorf("fact[%d] = %q, want %q", i, facts[i].Text, w)
		}
	}
}

// TestSalvageAtomicFacts_Concurrent verifies the package-level regex and
// metric counter survive concurrent access. Catches future changes that
// accidentally introduce stateful logic in the salvage hot path.
func TestSalvageAtomicFacts_Concurrent(t *testing.T) {
	const goroutines = 100
	const iters = 50
	raw := `{"memory":[{"id":"0","text":"Concurrent fact"}]}`

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				facts := salvageAtomicFacts(raw)
				if len(facts) != 1 || facts[0].Text != "Concurrent fact" {
					t.Errorf("concurrent salvage corrupted: %+v", facts)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestSalvageAtomicFacts_LargeInput catches accidental O(n²) regressions in
// the regex pass. A 200-fact, ~20 KB raw payload should salvage in well under
// 50 ms on commodity hardware. Doubling time → flag the regression.
func TestSalvageAtomicFacts_LargeInput(t *testing.T) {
	const n = 200
	var b strings.Builder
	b.WriteString(`{"memory":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"%d","text":"Fact %d about Marcus and Elena visiting Osteria Francescana"}`, i, i)
	}
	b.WriteString(`]}`)
	raw := b.String()

	start := time.Now()
	facts, tier := salvageWithTier(raw)
	elapsed := time.Since(start)

	if len(facts) != n {
		t.Errorf("got %d facts, want %d (tier=%d)", len(facts), n, tier)
	}
	if tier != salvageTierOne {
		t.Errorf("expected tier-1 for valid large envelope, got tier=%d", tier)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("salvage took %v for %d-fact payload (~%d KB) — possible O(n²) regression",
			elapsed, n, len(raw)/1024)
	}
}

// TestFilterValidFacts is a table covering the empty/whitespace/trim
// invariants used by both salvage tiers and the happy-path extractor.
func TestFilterValidFacts(t *testing.T) {
	cases := []struct {
		name string
		in   []AtomicFact
		want []string // expected Text values after filter
	}{
		{
			name: "trim + drop empty + keep order",
			in: []AtomicFact{
				{Text: "  Fact A  "},
				{Text: ""},
				{Text: "Fact B"},
			},
			want: []string{"Fact A", "Fact B"},
		},
		{
			name: "all empty input",
			in:   []AtomicFact{{Text: ""}, {Text: "   "}, {Text: "\t\n"}},
			want: []string{},
		},
		{
			name: "nil input",
			in:   nil,
			want: []string{},
		},
		{
			name: "single valid fact",
			in:   []AtomicFact{{Text: "Lone fact"}},
			want: []string{"Lone fact"},
		},
		{
			name: "preserves all optional fields after trim",
			in: []AtomicFact{{
				Text:                " Trimmed ",
				AttributedTo:        "user",
				NamedEntitiesInText: []string{"X"},
				LinkedMemoryIDs:     []string{"id-1"},
				EventDates:          []string{"2026-05-01"},
			}},
			want: []string{"Trimmed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filterValidFacts(tc.in)
			if len(out) != len(tc.want) {
				t.Fatalf("got %d facts, want %d: %+v", len(out), len(tc.want), out)
			}
			for i, w := range tc.want {
				if out[i].Text != w {
					t.Errorf("out[%d].Text = %q, want %q", i, out[i].Text, w)
				}
			}
			// Spot-check field preservation on the last case.
			if tc.name == "preserves all optional fields after trim" && len(out) > 0 {
				f := out[0]
				if f.AttributedTo != "user" || len(f.NamedEntitiesInText) != 1 ||
					len(f.LinkedMemoryIDs) != 1 || len(f.EventDates) != 1 {
					t.Errorf("optional fields lost during filter: %+v", f)
				}
			}
		})
	}
}
