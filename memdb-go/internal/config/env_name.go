package config

import (
	"log/slog"
	"strings"
)

// Deployment environment names reported by /health as data.env.
//
// MEMDB_ENV is the single switch that tells evaluation harnesses whether an
// instance is safe to write benchmark data into. Anything that is not an
// explicit "eval" is treated as production, so a forgotten or mistyped value
// fails closed.
const (
	EnvProd = "prod"
	EnvEval = "eval"
)

// NormalizeEnv maps a raw MEMDB_ENV value to EnvProd or EnvEval. An empty
// value is prod; an unrecognised value is prod too, with a warning.
func NormalizeEnv(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case EnvEval:
		return EnvEval
	case "", EnvProd:
		return EnvProd
	default:
		slog.Warn("MEMDB_ENV has unrecognised value, treating as prod", slog.String("value", raw))
		return EnvProd
	}
}
