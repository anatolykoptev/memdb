package search

import (
	"testing"
	"time"
)

// TestExtractTemporalRange pins the date-hint resolver against the patterns
// the F7 stage promises to handle. The resolver must:
//   - return ok=false for queries with no temporal signal (no spurious
//     range — the stage skips and saves a DB round-trip)
//   - resolve ISO literals to a single-day range
//   - resolve year tokens to a full-year range
//   - resolve common relative phrases against a fixed "now" passed in
func TestExtractTemporalRange(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC) // Tuesday
	cases := []struct {
		name      string
		query     string
		wantOK    bool
		wantStart string
		wantEnd   string
	}{
		{"empty", "", false, "", ""},
		{"plain", "what did we discuss yesterday at the meeting", true, "2026-04-27", "2026-04-27"},
		{"iso_date", "the call on 2024-03-15 was the one", true, "2024-03-15", "2024-03-15"},
		{"year", "in 2024 we shipped the migration", true, "2024-01-01", "2024-12-31"},
		{"last_month", "what happened last month with the redis fix", true, "2026-03-01", "2026-03-31"},
		{"this_year", "this year's roadmap", true, "2026-01-01", "2026-12-31"},
		{"last_year", "last year's review", true, "2025-01-01", "2025-12-31"},
		{"no_signal", "what is redis used for", false, "", ""},
		{"year_out_of_range", "the year 1850 was historic", false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := extractTemporalRange(c.query, now)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (range=%+v)", ok, c.wantOK, r)
			}
			if !ok {
				return
			}
			if r.Start != c.wantStart || r.End != c.wantEnd {
				t.Fatalf("range = [%s, %s], want [%s, %s]", r.Start, r.End, c.wantStart, c.wantEnd)
			}
		})
	}
}

// TestIsCat4Query pins the cat-4 temporal-extent heuristic — used by F7
// metrics tagging and downstream tuning.  False positives are tolerated
// (the augmentation stage no-ops on empty DB matches), but false negatives
// on the canonical phrasings would mean we miss boost opportunities.
func TestIsCat4Query(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"how long ago did we ship redis", true},
		{"how many months ago was the launch", true},
		{"what year did the migration land", true},
		{"when did we add the F7 index", true},
		{"since when has dozor been broken", true},
		{"what is redis used for", false},
		{"how does the F7 stage work", false},
		{"", false},
	}
	for _, c := range cases {
		got := isCat4Query(c.query)
		if got != c.want {
			t.Errorf("isCat4Query(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

// TestTemporalEnabled covers the env-flag parser.  Default (unset) must be
// ON; only the explicit off-values disable the stage.
func TestTemporalEnabled(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"1":     true,
		"true":  true,
		"on":    true,
		"yes":   true,
		"0":     false,
		"false": false,
		"off":   false,
		"no":    false,
		"OFF":   false,
	}
	for v, want := range cases {
		t.Setenv(temporalEnvFlag, v)
		if got := temporalEnabled(); got != want {
			t.Errorf("temporalEnabled with %q = %v, want %v", v, got, want)
		}
	}
}
