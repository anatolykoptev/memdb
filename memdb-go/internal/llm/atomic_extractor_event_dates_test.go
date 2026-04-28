package llm

import (
	"context"
	"reflect"
	"testing"
)

// TestValidateISODates pins the F7 ISO-8601 validation contract: only
// strict YYYY-MM-DD strings survive, everything else is dropped silently
// (the LLM occasionally emits "circa 2010" or "summer 2023" style values
// that we never want to persist as searchable dates).
func TestValidateISODates(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"all_valid", []string{"2024-01-15", "2025-12-31"}, []string{"2024-01-15", "2025-12-31"}},
		{"trims_whitespace", []string{"  2024-03-01  "}, []string{"2024-03-01"}},
		{"drops_year_only", []string{"2024"}, nil},
		{"drops_month_year", []string{"2024-03"}, nil},
		{"drops_freeform", []string{"summer 2023", "circa 2010"}, nil},
		{"drops_iso_with_time", []string{"2024-03-15T12:00:00Z"}, nil},
		{"mixed", []string{"2024-03-15", "summer 2023", "2025-01-01"}, []string{"2024-03-15", "2025-01-01"}},
		{"drops_invalid_calendar", []string{"2024-13-01", "2024-02-30"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateISODates(context.Background(), c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("validateISODates(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
