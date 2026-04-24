// Package search — tuning.go: M4 runtime-readable hyperparameters.
//
// Phase D parameters (D1/D2/D5/D10) were hard-coded as package-level
// const. For tuning grid-runs we need to sweep them via .env without
// rebuilding memdb-go. This file exposes them as env-readable accessors,
// each with bounded validation and a silent fallback to the compile-time
// default on invalid input.
//
// Pattern per param:
//
//	const defaultXxx = <literal>
//	func xxx() T { return parseEnv<T>("MEMDB_D?_XXX", lo, hi, defaultXxx) }
//
// Call sites within the package change from `xxx` (const) to `xxx()` (func).
// Default behaviour is unchanged when no env is set.
//
// Ops visibility: on first call to any accessor a single log line lists
// every override that diverged from default (gated by sync.Once).
package search

import (
	"log/slog"
	"math"
	"os"
	"strconv"
	"sync"
)

// envOverrides collects {env → parsed-value} for overrides that diverged
// from their defaults, then logs them once on first access.
var (
	envOverrideLogOnce sync.Once
	envOverrideMu      sync.Mutex
	envOverrides       = map[string]string{}
)

// recordOverride is called from each parseEnv* helper when an env var was
// set to a valid, in-bounds value that differs from the default. Safe for
// concurrent use.
func recordOverride(name, value string) {
	envOverrideMu.Lock()
	envOverrides[name] = value
	envOverrideMu.Unlock()
}

// LogTuningOverrides writes a single slog.Info line listing every
// MEMDB_D* hyperparameter override picked up from the environment.
// Idempotent — subsequent calls are no-ops.
//
// Callers: main() after flag parsing, or lazily inside first accessor call.
// Safe to call from multiple goroutines.
func LogTuningOverrides(logger *slog.Logger) {
	envOverrideLogOnce.Do(func() {
		if logger == nil {
			return
		}
		envOverrideMu.Lock()
		defer envOverrideMu.Unlock()
		if len(envOverrides) == 0 {
			return
		}
		attrs := make([]any, 0, 2*len(envOverrides))
		for k, v := range envOverrides {
			attrs = append(attrs, slog.String(k, v))
		}
		logger.Info("search: tuning env overrides active", attrs...)
	})
}

// parseEnvFloat reads env var `name`, parses as float64, and returns it
// if in [lo, hi]. Otherwise returns `def`. Silent on all errors.
func parseEnvFloat(name string, lo, hi, def float64) float64 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < lo || v > hi {
		return def
	}
	if v != def {
		recordOverride(name, raw)
	}
	return v
}

// parseEnvInt reads env var `name`, parses as int, and returns it if
// in [lo, hi]. Otherwise returns `def`. Silent on all errors.
func parseEnvInt(name string, lo, hi, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return def
	}
	if v != def {
		recordOverride(name, raw)
	}
	return v
}

// ---- D10 — answer_enhance --------------------------------------------------

const defaultAnswerEnhanceMinRelativity = 0.4

// answerEnhanceMinRelativity returns the minimum relativity threshold below
// which candidate memories are excluded from D10 answer enhancement.
// Env: MEMDB_D10_MIN_RELATIVITY in [0, 1].
func answerEnhanceMinRelativity() float64 {
	return parseEnvFloat("MEMDB_D10_MIN_RELATIVITY", 0, 1, defaultAnswerEnhanceMinRelativity)
}

// ---- D5 — staged_retrieval -------------------------------------------------

const (
	defaultStagedShortlistSize = 10
	defaultStagedMaxInputSize  = 50
)

// stagedShortlistSize returns the cap on Stage-2 shortlist size.
// Env: MEMDB_D5_SHORTLIST_SIZE in [1, 100].
func stagedShortlistSize() int {
	return parseEnvInt("MEMDB_D5_SHORTLIST_SIZE", 1, 100, defaultStagedShortlistSize)
}

// stagedMaxInputSize returns the max number of candidates passed into
// Stage 2. Prevents wasteful LLM token spend above this cap.
// Env: MEMDB_D5_MAX_INPUT_SIZE in [1, 500].
func stagedMaxInputSize() int {
	return parseEnvInt("MEMDB_D5_MAX_INPUT_SIZE", 1, 500, defaultStagedMaxInputSize)
}

// ---- D2 — service_multihop -------------------------------------------------

const (
	defaultMultihopMaxDepth = 2
	defaultMultihopDecay    = 0.8
)

// multihopMaxDepth returns the BFS depth for graph expansion.
// Env: MEMDB_D2_MAX_HOP in [1, 5].
func multihopMaxDepth() int {
	return parseEnvInt("MEMDB_D2_MAX_HOP", 1, 5, defaultMultihopMaxDepth)
}

// multihopDecay returns the per-hop score decay multiplier.
// Env: MEMDB_D2_HOP_DECAY in [0, 1].
func multihopDecay() float64 {
	return parseEnvFloat("MEMDB_D2_HOP_DECAY", 0, 1, defaultMultihopDecay)
}

// ---- D1 — rerank -----------------------------------------------------------

const (
	defaultD1BoostSemantic = 1.15
	defaultD1BoostEpisodic = 1.08
	defaultD1HalfLifeDays  = 180
)

// d1BoostSemantic returns the hierarchy boost multiplier for semantic-level
// memories. Env: MEMDB_D1_BOOST_SEMANTIC in [1, 2].
func d1BoostSemantic() float64 {
	return parseEnvFloat("MEMDB_D1_BOOST_SEMANTIC", 1, 2, defaultD1BoostSemantic)
}

// d1BoostEpisodic returns the hierarchy boost multiplier for episodic-level
// memories. Env: MEMDB_D1_BOOST_EPISODIC in [1, 2].
func d1BoostEpisodic() float64 {
	return parseEnvFloat("MEMDB_D1_BOOST_EPISODIC", 1, 2, defaultD1BoostEpisodic)
}

// d1HalfLifeDays returns the temporal-decay half-life in days (used for
// recency = exp(-alpha*days) with alpha = ln(2)/halfLifeDays).
// Env: MEMDB_D1_HALF_LIFE_DAYS in [1, 3650].
func d1HalfLifeDays() int {
	return parseEnvInt("MEMDB_D1_HALF_LIFE_DAYS", 1, 3650, defaultD1HalfLifeDays)
}

// d1DecayAlpha derives the per-day exponential decay rate from the
// configured half-life (alpha = ln(2)/halfLifeDays). When the env is
// unset this collapses to ~0.00385, matching DefaultDecayAlpha=0.0039
// within floating-point tolerance. Used by service_postprocess.go in
// place of the raw DefaultDecayAlpha const whenever the half-life env
// is set — default path (env unset) still goes through DefaultDecayAlpha.
func d1DecayAlpha() float64 {
	return math.Ln2 / float64(d1HalfLifeDays())
}
