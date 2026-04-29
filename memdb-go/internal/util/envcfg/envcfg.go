// Package envcfg provides typed, defaulted reads from environment variables.
//
// All functions return def when the variable is unset, empty, or unparseable.
// Range variants additionally return def when the parsed value falls outside [min, max].
// Float rejects NaN and Inf — those always fall back to def.
package envcfg

import (
	"math"
	"os"
	"strconv"
	"time"
)

// Float reads key as float64. Returns def on missing, parse error, NaN, or Inf.
func Float(key string, def float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

// FloatRange reads key as float64 and clamps to [min, max].
// Returns def when the variable is unset, unparseable, NaN, Inf, or out of range.
func FloatRange(key string, def, min, max float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < min || v > max {
		return def
	}
	return v
}

// Int reads key as int. Returns def on missing or parse error.
func Int(key string, def int) int {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// IntRange reads key as int and validates it is in [min, max].
// Returns def when the variable is unset, unparseable, or out of range.
func IntRange(key string, def, min, max int) int {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < min || v > max {
		return def
	}
	return v
}

// Bool reads key as bool using strconv.ParseBool semantics (1/t/true/T/TRUE/…).
// Returns def on missing or parse error.
func Bool(key string, def bool) bool {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

// String reads key as a string. Returns def when the variable is unset or empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Duration reads key as a time.Duration (e.g. "30s", "1m30s").
// Returns def on missing or parse error.
func Duration(key string, def time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return v
}
