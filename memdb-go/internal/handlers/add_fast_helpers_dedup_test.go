package handlers

import "testing"

func TestFastDedupThreshold(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want float64
	}{
		{"empty_default", "", 0.92},
		{"valid_085", "0.85", 0.85},
		{"valid_095", "0.95", 0.95},
		{"valid_100", "1.0", 1.0},
		{"invalid_negative", "-1", 0.92},
		{"invalid_zero", "0", 0.92},
		{"invalid_above1", "2", 0.92},
		{"invalid_nonnum", "abc", 0.92},
		{"invalid_empty_spaces", "   ", 0.92},
		{"whitespace_padded", " 0.85 ", 0.85},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MEMDB_FAST_DEDUP_THRESHOLD", tt.env)
			if got := fastDedupThreshold(); got != tt.want {
				t.Errorf("fastDedupThreshold() env=%q = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

func TestFastDedupSessionAware(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{"empty_default", "", true},
		{"zero", "0", false},
		{"false", "false", false},
		{"off", "off", false},
		{"no", "no", false},
		{"one", "1", true},
		{"true", "true", true},
		{"random", "yes", true},
		{"FALSE_upper", "FALSE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MEMDB_FAST_DEDUP_SESSION_AWARE", tt.env)
			if got := fastDedupSessionAware(); got != tt.want {
				t.Errorf("fastDedupSessionAware() env=%q = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}
