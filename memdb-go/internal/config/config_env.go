package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// env helpers — read typed values from environment variables with fallbacks.

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envCSV(key string, def []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// clampCanaryPct clamps v to [0, 100]. Values outside the range are clamped and a warning is logged.
func clampCanaryPct(v int) int {
	if v < 0 {
		slog.Warn("MEMDB_FACTUAL_CANARY_PCT is below 0, clamping to 0", slog.Int("value", v))
		return 0
	}
	if v > 100 {
		slog.Warn("MEMDB_FACTUAL_CANARY_PCT is above 100, clamping to 100", slog.Int("value", v))
		return 100
	}
	return v
}

// validatedAnswerStyle returns v if it is a recognised answer_style value,
// otherwise logs a warning and returns "" (no default applied — conversational is
// the handler-level fallback). Recognised values: "" (no override), "conversational", "factual".
func validatedAnswerStyle(v string) string {
	switch v {
	case "", "conversational", "factual":
		return v
	default:
		slog.Warn("MEMDB_DEFAULT_ANSWER_STYLE has unrecognised value, falling back to conversational",
			slog.String("value", v))
		return "conversational"
	}
}
