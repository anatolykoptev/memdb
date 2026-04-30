package search

// d10_config.go — operator-facing configuration layer for the D10 answer
// extractor. Consolidates the 5+ env vars that grew organically across PRs
// #249 / #250 / #251 / #252 / #253 into:
//
//   1. A single hardness preset (MEMDB_D10_HARDNESS = tight | balanced | loose)
//      that bundles sensible {threshold, classifier, relativity} triples.
//   2. A prompt-mode selector (MEMDB_D10_PROMPT_MODE = strict | soft |
//      probabilistic) that switches the system prompt template AND decides
//      whether to invoke the embedding classifier at all.
//   3. Per-knob overrides for power users — every individual env var
//      (MEMDB_FACTUAL_CONFIDENCE_THRESHOLD, MEMDB_D10_MIN_RELATIVITY, etc.)
//      continues to work and takes precedence over the hardness preset.
//
// The struct is built once per request via LoadD10Config so every call site
// sees a coherent snapshot — no risk of one accessor reading a fresher env
// than another after a hot SIGHUP-style reload (we don't reload, but the
// snapshot semantics keep that door open).
//
// All env reads stay backward-compatible: an operator running pre-this-PR
// envs will see the same behaviour. The defaults of the new "balanced"
// preset match the post-#252 / #253 hard-coded values exactly.
//
// Placement: this file lives in package search rather than handlers because
// search → handlers is forbidden (would create an import cycle), and the
// post-retrieval prompt + extractor (the heaviest D10 consumer) lives in
// search. Handlers reach in via search.LoadD10Config() — handlers → search
// is the legitimate direction across the codebase.

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// PromptMode selects which D10 system prompt the extractor uses. The three
// variants restore the post-PR-#249 / #251 / #252 baselines so operators can
// A/B between them via env without code changes.
type PromptMode string

const (
	// PromptModeStrict — pre-#249 verbatim. "Use the exact surface form" /
	// "respond with UNKNOWN if memories do not contain the answer". Best
	// LLM-Judge in claude_NS replay (0.35). NO classifier hint append.
	PromptModeStrict PromptMode = "strict"

	// PromptModeSoft — PR #249 verbatim. "Atomic facts may not contain the
	// question's surface form. That is fine — synthesise the answer from
	// the closest grounded fact." NO classifier hint append.
	PromptModeSoft PromptMode = "soft"

	// PromptModeProbabilistic — PR #252 behaviour. Single base prompt with
	// optional classifier-driven hint append (gated by classifier conf >=
	// threshold AND non-open_domain top-1).
	PromptModeProbabilistic PromptMode = "probabilistic"
)

// Hardness selects a preset bundle for the confidence-gating triple. Ordered
// from "answer only when confident" (tight) to "answer aggressively" (loose).
type Hardness string

const (
	HardnessTight    Hardness = "tight"
	HardnessBalanced Hardness = "balanced"
	HardnessLoose    Hardness = "loose"
)

// D10Config carries the resolved D10 configuration for one request lifetime.
// Fields are populated by LoadD10Config in dependency order:
//
//   1. Hardness preset → seeds defaults for ConfidenceThreshold,
//      ClassifierThreshold, MinRelativity.
//   2. Per-knob env overrides → stomp the seeded defaults if set.
//   3. Mode + ConfidenceFormula + ConfidenceWeights + DensityFloor → read
//      from their own env vars (no hardness preset for these — they are
//      shape choices, not strictness choices).
//
// All fields are values, not pointers — D10Config is meant to be passed by
// value. Cheap to copy (~80 bytes) and avoids accidental mutation between
// sites.
type D10Config struct {
	// Hardness is the preset bundle that seeded the confidence/classifier
	// triple. Stored verbatim for observability (debug headers, logs).
	Hardness Hardness

	// Mode is the prompt-template selector. Drives PromptForQuery.
	Mode PromptMode

	// MinRelativity is the per-item relativity floor for D10 candidates.
	// Items below this are excluded from the LLM extractor input.
	// Maps to MEMDB_D10_MIN_RELATIVITY.
	MinRelativity float64

	// ConfidenceThreshold is the gate for routing the factual prompt
	// to the high-confidence variant. Compared against the formula's
	// output (top1 cosine for "top1", combined score for "multifeature").
	// Maps to MEMDB_FACTUAL_CONFIDENCE_THRESHOLD.
	ConfidenceThreshold float64

	// ConfidenceFormula picks how decideFactualPrompt computes the score.
	// "top1" or "multifeature". Maps to MEMDB_FACTUAL_CONFIDENCE_FORMULA.
	ConfidenceFormula string

	// ConfidenceWeights is the raw env-string for multifeature weights;
	// parsing remains in handlers/chat_prompt_confidence.go for the test
	// surface that already exists. Empty → defaults.
	// Maps to MEMDB_FACTUAL_CONFIDENCE_WEIGHTS.
	ConfidenceWeights string

	// DensityFloor is the relativity threshold for "useful hit" counting in
	// the multifeature density term. Maps to MEMDB_FACTUAL_DENSITY_FLOOR.
	DensityFloor float64

	// ClassifierThreshold is the minimum top-1 classifier confidence below
	// which the hint append is suppressed (probabilistic mode only).
	// Maps to MEMDB_D10_CLASSIFIER_THRESHOLD.
	ClassifierThreshold float64
}

// hardnessPreset bundles the strictness-driven defaults. Values picked from
// session work post-#252:
//
//   tight    — "only commit when retrieval is overwhelmingly confident".
//              ConfidenceThreshold=0.50, ClassifierThreshold=0.70, MinRel=0.50.
//   balanced — current default after #252/#253. ConfidenceThreshold=0.30,
//              ClassifierThreshold=0.50, MinRel=0.40.
//   loose    — "answer aggressively, more synthesis". ConfidenceThreshold=0.20,
//              ClassifierThreshold=0.40, MinRel=0.30.
type hardnessPreset struct {
	ConfidenceThreshold float64
	ClassifierThreshold float64
	MinRelativity       float64
}

var hardnessPresets = map[Hardness]hardnessPreset{
	HardnessTight: {
		ConfidenceThreshold: 0.50,
		ClassifierThreshold: 0.70,
		MinRelativity:       0.50,
	},
	HardnessBalanced: {
		// These are the post-#252/#253 defaults preserved verbatim.
		ConfidenceThreshold: defaultFactualConfidenceThresholdPreset,
		ClassifierThreshold: defaultD10ClassifierThreshold,
		MinRelativity:       defaultAnswerEnhanceMinRelativity,
	},
	HardnessLoose: {
		ConfidenceThreshold: 0.20,
		ClassifierThreshold: 0.40,
		MinRelativity:       0.30,
	},
}

// defaultFactualConfidenceThresholdPreset mirrors handlers'
// defaultFactualConfidenceThreshold (0.5) but keeps the value here so the
// search package does not need to import handlers. The two MUST stay in
// sync — covered by TestD10ConfigDefaultsMatchHandlers in handlers tests.
const defaultFactualConfidenceThresholdPreset = 0.5

// LoadD10Config builds a snapshot from the current environment. Cheap (only
// env reads + parse), safe to call once per request.
//
// Resolution order:
//   1. Read MEMDB_D10_HARDNESS → seed preset (invalid → balanced).
//   2. Apply per-knob overrides from individual env vars.
//   3. Read prompt mode + formula/weights/density (no preset interaction).
func LoadD10Config() D10Config {
	hardness := hardnessFromEnv()
	preset := hardnessPresets[hardness]

	cfg := D10Config{
		Hardness:            hardness,
		Mode:                promptModeFromEnv(),
		MinRelativity:       preset.MinRelativity,
		ConfidenceThreshold: preset.ConfidenceThreshold,
		ClassifierThreshold: preset.ClassifierThreshold,
	}

	// Per-knob overrides win over the hardness preset.
	if v, ok := envFloatIfSet("MEMDB_D10_MIN_RELATIVITY", 0, 1); ok {
		cfg.MinRelativity = v
	}
	if v, ok := envFloatIfSet("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", 0, 1); ok {
		cfg.ConfidenceThreshold = v
	}
	if v, ok := envFloatIfSet("MEMDB_D10_CLASSIFIER_THRESHOLD", 0, 1); ok {
		cfg.ClassifierThreshold = v
	}

	// Shape knobs: not influenced by hardness preset. Empty/invalid env →
	// the downstream parser keeps its own default; we just thread the raw
	// string (or empty) through so handlers can call its existing parser.
	cfg.ConfidenceFormula = strings.TrimSpace(os.Getenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA"))
	cfg.ConfidenceWeights = os.Getenv("MEMDB_FACTUAL_CONFIDENCE_WEIGHTS")
	if v, ok := envFloatIfSet("MEMDB_FACTUAL_DENSITY_FLOOR", 0, 1); ok {
		cfg.DensityFloor = v
	} else {
		cfg.DensityFloor = defaultDensityFloorPreset
	}

	return cfg
}

// defaultDensityFloorPreset mirrors handlers' defaultDensityFloor (0.30).
// Same sync-required relationship as defaultFactualConfidenceThresholdPreset.
const defaultDensityFloorPreset = 0.30

// hardnessFromEnv reads MEMDB_D10_HARDNESS. Unknown / unset values fall back
// to balanced (the safest default — matches pre-this-PR behaviour bit for
// bit when no other env is set).
func hardnessFromEnv() Hardness {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MEMDB_D10_HARDNESS"))) {
	case string(HardnessTight):
		return HardnessTight
	case string(HardnessLoose):
		return HardnessLoose
	case string(HardnessBalanced), "":
		return HardnessBalanced
	default:
		return HardnessBalanced
	}
}

// promptModeFromEnv reads MEMDB_D10_PROMPT_MODE. Unknown / unset values fall
// back to probabilistic (the default since PR #252 — preserves shipping
// behaviour for operators who do not opt in to the new selector).
func promptModeFromEnv() PromptMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MEMDB_D10_PROMPT_MODE"))) {
	case string(PromptModeStrict):
		return PromptModeStrict
	case string(PromptModeSoft):
		return PromptModeSoft
	case string(PromptModeProbabilistic), "":
		return PromptModeProbabilistic
	default:
		return PromptModeProbabilistic
	}
}

// envFloatIfSet reads name as a float; returns (value, true) when set AND
// in range, (0, false) when unset, malformed, or out of range. Distinguishes
// "operator did not set" from "operator set 0", which the legacy parseEnvFloat
// path could not — that distinction is what lets per-knob overrides win
// cleanly over a hardness preset's non-zero default.
func envFloatIfSet(name string, lo, hi float64) (float64, bool) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

// PromptForQuery — see d10_config_prompt.go. Kept in a sibling file so the
// loader (env reads + preset table) is decoupled from the routing logic
// (classifier integration). Both belong to the D10Config type, but the two
// concerns evolve independently — preset-tuning sprints touch the loader,
// prompt-rewrites touch the routing.
