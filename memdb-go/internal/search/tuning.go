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
	"regexp"
	"strconv"
	"strings"
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

const (
	defaultAnswerEnhanceMinRelativity = 0.4
	// defaultD10ClassifierEnabled — default OFF (cefix7 baseline). Operators
	// opt-in via MEMDB_D10_CLASSIFIER_ENABLED=true once LLM-Judge lift is
	// confirmed. Code, comment, and production .env now agree.
	defaultD10ClassifierEnabled = false
	// defaultD10HardRoutingThreshold — top-1 softmax probability above which
	// the soft-routing distribution block is replaced by a category-specific
	// full system prompt. 0.97 is "near-verbatim anchor match" territory;
	// false-positive risk is empirically near zero.
	defaultD10HardRoutingThreshold = 0.97
	// defaultD10SoftTopN — how many categories to surface in the
	// distribution block. 5 == all of them; lowering trims tokens at the
	// cost of hiding the long tail (which a calibrated classifier puts at
	// ~1-3% each anyway).
	defaultD10SoftTopN = 5
	// defaultD10SoftmaxTemperature — inverse-temperature applied to cosine
	// similarities before softmax. T=10 keeps top-1 in [0.6, 0.95] for
	// confident anchor matches and [0.25, 0.45] for ambiguous queries on
	// the multilingual-e5-large embedder. Lower → flatter distribution
	// (less hard-routing); higher → sharper (more hard-routing).
	defaultD10SoftmaxTemperature = 10.0
)

// answerEnhanceMinRelativity returns the minimum relativity threshold below
// which candidate memories are excluded from D10 answer enhancement.
// Env: MEMDB_D10_MIN_RELATIVITY in [0, 1].
func answerEnhanceMinRelativity() float64 {
	return parseEnvFloat("MEMDB_D10_MIN_RELATIVITY", 0, 1, defaultAnswerEnhanceMinRelativity)
}

// d10ClassifierEnabled reports whether the embedding-based query classifier
// should run at all. Default OFF (cefix7 baseline); operators opt-in via
// MEMDB_D10_CLASSIFIER_ENABLED=true once the LLM-Judge lift is confirmed.
//
// Env: MEMDB_D10_CLASSIFIER_ENABLED — accepts 1/true/yes (on) or
// 0/false/no (off). Anything else falls back to the default.
func d10ClassifierEnabled() bool {
	return parseEnvBool("MEMDB_D10_CLASSIFIER_ENABLED", defaultD10ClassifierEnabled)
}

// d10HardRoutingThreshold returns the top-1 softmax probability above which
// the soft-routing distribution block is replaced by a category-specific
// full system prompt. At ≥0.97 the classifier has matched the query to an
// anchor near-verbatim and false-positive risk is negligible.
//
// Env: MEMDB_D10_HARD_ROUTING_THRESHOLD in [0, 1]. Set to 1.0 to disable
// hard routing entirely (always soft); set to 0.0 to always hard-route on
// top-1 (legacy PR #250 behaviour, not recommended).
func d10HardRoutingThreshold() float64 {
	return parseEnvFloat("MEMDB_D10_HARD_ROUTING_THRESHOLD", 0, 1, defaultD10HardRoutingThreshold)
}

// d10SoftTopN returns how many categories to surface in the soft-routing
// distribution block. Lower trims tokens at the cost of hiding the long
// tail (which a calibrated classifier puts at ~1-3% each anyway).
//
// Env: MEMDB_D10_SOFT_TOP_N in [1, 5]. Default 5 (all categories).
func d10SoftTopN() int {
	return parseEnvInt("MEMDB_D10_SOFT_TOP_N", 1, 5, defaultD10SoftTopN)
}

// d10SoftmaxTemperature returns the inverse-temperature applied to cosine
// similarities before softmax in classifyAndDistribute. Higher = sharper
// distribution (more hard-routing); lower = flatter (less hard-routing).
//
// Env: MEMDB_D10_SOFTMAX_TEMPERATURE in [1, 50]. Default 10.0 (calibrated
// against multilingual-e5-large + the LoCoMo anchor set).
func d10SoftmaxTemperature() float64 {
	return parseEnvFloat("MEMDB_D10_SOFTMAX_TEMPERATURE", 1, 50, defaultD10SoftmaxTemperature)
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

// ---- F9 — cat-2 recall budget -----------------------------------------------

// defaultCat2Threshold is the similarity threshold applied to cat-2 queries
// (temporal multi-hop: "When...", "How long...", etc.).  Lowered from 0.2
// to 0.05 to let weak bridging hops survive — they would otherwise be
// filtered out before reaching the reranker.
const defaultCat2Threshold = 0.05

// ---- F2 — reflection-loop deep search --------------------------------------

const (
	defaultReflectionEnabled        = false
	defaultReflectionOnComplexOnly  = true
)

// reflectionEnabled reports whether the F2 reflection-loop stage should run
// at all. Default OFF (opt-in for measurement).
//
// Env: MEMDB_F2_REFLECTION = "1" | "true" | "yes" enables; anything else (or
// unset) disables.
func reflectionEnabled() bool {
	return parseEnvBool("MEMDB_F2_REFLECTION", defaultReflectionEnabled)
}

// reflectionOnComplexOnly reports whether the F2 reflection stage should
// gate itself on the complexity heuristic (isCat2Query OR isCat3Query).
// Default ON — running reflection on every query blows the latency budget.
//
// Env: MEMDB_REFLECTION_ON_COMPLEX_ONLY = "0" | "false" | "no" disables the
// gate (run on every query); default and any other value keeps it ON.
func reflectionOnComplexOnly() bool {
	return parseEnvBool("MEMDB_REFLECTION_ON_COMPLEX_ONLY", defaultReflectionOnComplexOnly)
}

// parseEnvBool parses an env var as a boolean. "1"/"true"/"yes" (case
// insensitive) → true; "0"/"false"/"no" → false; anything else → def.
// Records the override only when the value diverges from the default.
func parseEnvBool(name string, def bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if raw == "" {
		return def
	}
	var v bool
	switch raw {
	case "1", "true", "yes", "on":
		v = true
	case "0", "false", "no", "off":
		v = false
	default:
		return def
	}
	if v != def {
		recordOverride(name, raw)
	}
	return v
}

// cat2Threshold reads MEMDB_CAT2_THRESHOLD and returns the similarity
// threshold used for cat-2 (temporal multi-hop) queries detected by
// isCat2Query.  Falls back to the default 0.05 when the env is unset,
// unparseable, or out of range [0, 0.5].  Default 0.05 is mem0's recommended
// cat-2 floor, lowered from the standard 0.2 to let weak bridging hops
// survive the similarity filter before the reranker.
//
// Note: MEMDB_CAT2_THRESHOLD=0 disables the cat-2 adjustment entirely because
// applyCat2Threshold only lowers a non-zero Relativity (zero = no filter).
func cat2Threshold() float64 {
	return parseEnvFloat("MEMDB_CAT2_THRESHOLD", 0, 0.5, defaultCat2Threshold)
}

// ---- F7 — cat-4 temporal-extent heuristic ----------------------------------

// cat4QueryRe matches the LoCoMo category-4 question shapes the F7 temporal
// augmentation stage targets — questions that ask about time elapsed,
// historical year, or temporal extent. These queries benefit most from the
// event_dates GIN index because the right answer is usually pinned to a
// specific day or year that the user explicitly references.
//
// Patterns:
//
//	"How long ago ...", "How many years/months/weeks/days ago ...",
//	"What year ...", "What month ...", "When did ...",
//	"In what year ...", "Since when ...".
//
// Deliberately overlaps with cat2QueryRe — F7 boosts a strict superset of
// cat-2 plus pure-cat-4 phrasings ("Since when", "How long ago"). False
// positives (non-temporal "When did the meeting end?") still trigger the
// augmentation but the DB query returns empty matches and the boost is a
// no-op — bounded extra latency, no correctness impact.
var cat4QueryRe = regexp.MustCompile(
	`(?i)\b(how long ago|how many (years|months|weeks|days) ago|what (year|month|day|date)|when did|in what year|since when)\b`,
)

// isCat4Query returns true when q matches the cat-4 temporal-extent heuristic.
// Used by F7 metrics tagging and by future cat-4-only tuning paths.
func isCat4Query(q string) bool {
	return cat4QueryRe.MatchString(strings.TrimSpace(q))
}
