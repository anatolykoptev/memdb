package search

import (
	"testing"
	"time"
)

func TestDetectTemporalCutoff(t *testing.T) {
	tests := []struct {
		query    string
		wantDays int // expected approximate days ago, 0 = no detection
	}{
		// English — 24h
		{"what happened today", 1},
		{"yesterday's events", 1},
		{"last 24h activity", 1},
		{"last night discussion", 1},

		// English — 7 days
		{"what happened last week", 7},
		{"this week's tasks", 7},
		{"last 7 days", 7},
		{"past week summary", 7},

		// English — 30 days
		{"this month progress", 30},
		{"last 30 days", 30},
		{"past month review", 30},

		// English — 90 days
		{"last quarter results", 90},
		{"past 3 months", 90},
		{"last 90 days", 90},

		// English — generic recent
		{"recently added memories", 30},
		{"show recent changes", 30},

		// Russian — 24h
		{"что было сегодня", 1},
		{"вчера обсуждали", 1},
		{"за последние сутки", 1},

		// Russian — 7 days
		{"на этой неделе", 7},
		{"за последнюю неделю", 7},
		{"на прошлой неделе", 7},
		{"последних 7 дн", 7},

		// Russian — 30 days
		{"в этом месяце", 30},
		{"за последний месяц", 30},
		{"в прошлом месяце", 30},

		// Russian — 90 days
		{"за последний квартал", 90},
		{"за последних 3 месяц", 90},

		// Russian — generic recent
		{"недавно добавленное", 30},
		{"последние изменения", 30},

		// No temporal
		{"tell me about MemDB architecture", 0},
		{"how does search work", 0},
		{"привет мир", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			cutoff := DetectTemporalCutoff(tt.query)
			if tt.wantDays == 0 {
				if !cutoff.IsZero() {
					t.Errorf("expected no temporal detection, got cutoff %v", cutoff)
				}
				return
			}
			if cutoff.IsZero() {
				t.Fatalf("expected %d days ago, got no detection", tt.wantDays)
			}
			// Check cutoff is approximately correct (within 1 day tolerance)
			expected := time.Now().UTC().AddDate(0, 0, -tt.wantDays)
			diff := cutoff.Sub(expected)
			if diff < -time.Hour*24 || diff > time.Hour*24 {
				t.Errorf("expected ~%d days ago, got cutoff %v (diff %v)", tt.wantDays, cutoff, diff)
			}
		})
	}
}
