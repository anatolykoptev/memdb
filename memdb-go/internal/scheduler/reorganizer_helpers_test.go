package scheduler

import (
	"testing"
	"time"
)

func TestValidObservationDate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"valid", "2026-07-18", true},
		{"valid_edge", "2000-01-01", true},
		{"invalid_month", "2026-13-01", false},
		{"invalid_day", "2026-07-45", false},
		{"not_a_date", "today", false},
		{"not_a_date2", "2026-07-18T15:30:00Z", false}, // RFC3339 not accepted
		{"partial", "2026-07", false},
		{"garbage", "not-a-date", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validObservationDate(tt.in); got != tt.want {
				t.Errorf("validObservationDate(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestGoBounded(t *testing.T) {
	r := &Reorganizer{bgSem: make(chan struct{}, 2)}
	done := make(chan struct{}, 5)
	for i := 0; i < 5; i++ {
		r.goBounded(func() {
			time.Sleep(10 * time.Millisecond)
			done <- struct{}{}
		})
	}
	got := 0
	timer := time.After(2 * time.Second)
	for got < 5 {
		select {
		case <-done:
			got++
		case <-timer:
			t.Fatalf("only %d/5 goroutines completed", got)
		}
	}
}
