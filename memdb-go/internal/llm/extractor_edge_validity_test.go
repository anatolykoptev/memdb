package llm

// extractor_edge_validity_test.go — covers ExtractedFact.EdgeValidity, the
// F11 promotion of EventDates onto entity_edges / memory_edges valid_at +
// invalid_at. Pure helper; no LLM, no fixtures.

import "testing"

func TestExtractedFact_EdgeValidity(t *testing.T) {
	cases := []struct {
		name              string
		eventDates        []string
		fallback          string
		wantValidAt       string
		wantInvalidAt     string
	}{
		{
			name:          "empty event_dates falls back to ValidAt",
			eventDates:    nil,
			fallback:      "2023-10-18T09:00:00Z",
			wantValidAt:   "2023-10-18T09:00:00Z",
			wantInvalidAt: "",
		},
		{
			name:          "empty event_dates and empty fallback yields empty valid_at",
			eventDates:    []string{},
			fallback:      "",
			wantValidAt:   "",
			wantInvalidAt: "",
		},
		{
			name:          "single event_date promotes to YYYY-MM-DDT00:00:00Z, no invalid_at",
			eventDates:    []string{"2023-10-18"},
			fallback:      "2024-01-01T00:00:00Z",
			wantValidAt:   "2023-10-18T00:00:00Z",
			wantInvalidAt: "",
		},
		{
			name:          "two event_dates yields range valid_at + invalid_at",
			eventDates:    []string{"2023-10-18", "2024-04-29"},
			fallback:      "ignored",
			wantValidAt:   "2023-10-18T00:00:00Z",
			wantInvalidAt: "2024-04-29T00:00:00Z",
		},
		{
			name:          "more than two event_dates uses first and second only",
			eventDates:    []string{"2023-01-01", "2023-06-30", "2024-12-31"},
			fallback:      "fallback-not-used",
			wantValidAt:   "2023-01-01T00:00:00Z",
			wantInvalidAt: "2023-06-30T00:00:00Z",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ExtractedFact{EventDates: c.eventDates}
			gotValid, gotInvalid := f.EdgeValidity(c.fallback)
			if gotValid != c.wantValidAt {
				t.Errorf("valid_at: got %q, want %q", gotValid, c.wantValidAt)
			}
			if gotInvalid != c.wantInvalidAt {
				t.Errorf("invalid_at: got %q, want %q", gotInvalid, c.wantInvalidAt)
			}
		})
	}
}

func TestExtractedFact_EdgeValidity_NilReceiver(t *testing.T) {
	var f *ExtractedFact
	got, gotInv := f.EdgeValidity("fallback-x")
	if got != "fallback-x" || gotInv != "" {
		t.Errorf("nil receiver: got (%q, %q), want (\"fallback-x\", \"\")", got, gotInv)
	}
}
