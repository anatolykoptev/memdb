package handlers

// d10_config_test.go — verifies the operator-facing D10 configuration layer
// works as documented:
//
//   1. Defaults (no env set) match the post-#252/#253 shipping behaviour.
//   2. Hardness presets (tight/balanced/loose) seed the right values.
//   3. Per-knob env overrides win over the hardness preset.
//   4. Invalid env values fall back to the documented defaults.
//   5. Strict / Soft prompt modes do NOT call the classifier (zero embed
//      cost on A/B sweeps).
//   6. Probabilistic mode keeps calling the classifier as before.
//
// The tests live in the handlers package so they verify the cross-package
// contract: handlers reads search.LoadD10Config for confidence threshold
// and the search package owns the actual loader. The legacy direct env
// reads (factualConfidenceThreshold etc.) MUST keep returning the same
// values now that they delegate to LoadD10Config — that is the
// backward-compatibility property this PR claims.

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// clearD10Env wipes every env var the loader inspects. Called at the top of
// each test so `t.Setenv("X", "")` semantics (which Go test framework treats
// as "unset for the duration of the test") apply uniformly.
func clearD10Env(t *testing.T) {
	t.Helper()
	envs := []string{
		"MEMDB_D10_HARDNESS",
		"MEMDB_D10_PROMPT_MODE",
		"MEMDB_D10_MIN_RELATIVITY",
		"MEMDB_D10_CLASSIFIER_THRESHOLD",
		"MEMDB_FACTUAL_CONFIDENCE_THRESHOLD",
		"MEMDB_FACTUAL_CONFIDENCE_FORMULA",
		"MEMDB_FACTUAL_CONFIDENCE_WEIGHTS",
		"MEMDB_FACTUAL_DENSITY_FLOOR",
	}
	for _, e := range envs {
		t.Setenv(e, "")
	}
}

// TestLoadD10Config_Defaults — with no env set, the loader returns the
// balanced preset and probabilistic mode. These values are the public
// contract: any change here is a behaviour change for every operator who
// has not opted in to the new selectors.
func TestLoadD10Config_Defaults(t *testing.T) {
	clearD10Env(t)
	c := search.LoadD10Config()

	if c.Hardness != search.HardnessBalanced {
		t.Errorf("Hardness = %q, want balanced", c.Hardness)
	}
	if c.Mode != search.PromptModeProbabilistic {
		t.Errorf("Mode = %q, want probabilistic", c.Mode)
	}
	// Balanced preset values — these MUST equal the pre-this-PR shipping
	// defaults bit for bit, otherwise we silently changed prod behaviour.
	if math.Abs(c.ConfidenceThreshold-0.5) > 1e-9 {
		t.Errorf("ConfidenceThreshold = %v, want 0.5 (balanced)", c.ConfidenceThreshold)
	}
	if math.Abs(c.ClassifierThreshold-0.5) > 1e-9 {
		t.Errorf("ClassifierThreshold = %v, want 0.5 (balanced)", c.ClassifierThreshold)
	}
	if math.Abs(c.MinRelativity-0.4) > 1e-9 {
		t.Errorf("MinRelativity = %v, want 0.4 (balanced)", c.MinRelativity)
	}
	if math.Abs(c.DensityFloor-0.30) > 1e-9 {
		t.Errorf("DensityFloor = %v, want 0.30 (default)", c.DensityFloor)
	}
}

// TestLoadD10Config_HardnessTight — setting MEMDB_D10_HARDNESS=tight must
// pick the tight preset's values for every gating field. Validates the
// preset table is wired correctly and not silently dropped.
func TestLoadD10Config_HardnessTight(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_HARDNESS", "tight")
	c := search.LoadD10Config()

	if c.Hardness != search.HardnessTight {
		t.Errorf("Hardness = %q, want tight", c.Hardness)
	}
	if math.Abs(c.ConfidenceThreshold-0.50) > 1e-9 {
		t.Errorf("ConfidenceThreshold = %v, want 0.50 (tight)", c.ConfidenceThreshold)
	}
	if math.Abs(c.ClassifierThreshold-0.70) > 1e-9 {
		t.Errorf("ClassifierThreshold = %v, want 0.70 (tight)", c.ClassifierThreshold)
	}
	if math.Abs(c.MinRelativity-0.50) > 1e-9 {
		t.Errorf("MinRelativity = %v, want 0.50 (tight)", c.MinRelativity)
	}
}

// TestLoadD10Config_HardnessLoose — symmetric to tight; the loose preset
// values are the ones operators flip to when they want maximum synthesis
// at the cost of some hallucination risk on ambiguous retrieval.
func TestLoadD10Config_HardnessLoose(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_HARDNESS", "loose")
	c := search.LoadD10Config()

	if c.Hardness != search.HardnessLoose {
		t.Errorf("Hardness = %q, want loose", c.Hardness)
	}
	if math.Abs(c.ConfidenceThreshold-0.20) > 1e-9 {
		t.Errorf("ConfidenceThreshold = %v, want 0.20 (loose)", c.ConfidenceThreshold)
	}
	if math.Abs(c.ClassifierThreshold-0.40) > 1e-9 {
		t.Errorf("ClassifierThreshold = %v, want 0.40 (loose)", c.ClassifierThreshold)
	}
	if math.Abs(c.MinRelativity-0.30) > 1e-9 {
		t.Errorf("MinRelativity = %v, want 0.30 (loose)", c.MinRelativity)
	}
}

// TestLoadD10Config_PerKnobOverrides — the spec contract: "hardness preset
// is applied first, then individual env knobs override its values". So
// `MEMDB_D10_HARDNESS=tight + MEMDB_FACTUAL_CONFIDENCE_THRESHOLD=0.2`
// produces tight for ClassifierThreshold/MinRelativity but threshold=0.2.
//
// This is the single most important behavioural test in the file — it
// guarantees operators can pin one knob without losing the rest of the
// preset bundle.
func TestLoadD10Config_PerKnobOverrides(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_HARDNESS", "tight")
	t.Setenv("MEMDB_FACTUAL_CONFIDENCE_THRESHOLD", "0.2")
	c := search.LoadD10Config()

	if c.Hardness != search.HardnessTight {
		t.Errorf("Hardness = %q, want tight (override does NOT change preset name)", c.Hardness)
	}
	// Override wins on its own field.
	if math.Abs(c.ConfidenceThreshold-0.2) > 1e-9 {
		t.Errorf("ConfidenceThreshold = %v, want 0.2 (override of tight=0.50)", c.ConfidenceThreshold)
	}
	// Other tight-preset values are intact — that is the bundle property.
	if math.Abs(c.ClassifierThreshold-0.70) > 1e-9 {
		t.Errorf("ClassifierThreshold = %v, want 0.70 (tight, untouched)", c.ClassifierThreshold)
	}
	if math.Abs(c.MinRelativity-0.50) > 1e-9 {
		t.Errorf("MinRelativity = %v, want 0.50 (tight, untouched)", c.MinRelativity)
	}
}

// TestLoadD10Config_InvalidMode_FallsBackToProbabilistic — silent fallback
// is the right behaviour for prompt mode: an operator typo (e.g.
// PROMPT_MODE=extractive copied from a draft spec) should NOT kill the
// request, it should keep the default. Loud failure here would punish
// operator experimentation. Casing is accepted (the loader normalises via
// ToLower) so "STRICT" / "Strict" map to strict — that is documented
// behaviour, not a typo.
func TestLoadD10Config_InvalidMode_FallsBackToProbabilistic(t *testing.T) {
	clearD10Env(t)
	cases := []string{"extractive", "fast", "1", "  ", "factual", "off"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("MEMDB_D10_PROMPT_MODE", raw)
			c := search.LoadD10Config()
			if c.Mode != search.PromptModeProbabilistic {
				t.Errorf("invalid mode %q: got %q, want probabilistic", raw, c.Mode)
			}
		})
	}
}

// TestLoadD10Config_InvalidHardness_FallsBackToBalanced — same shape as the
// mode test. Note that an unknown hardness MUST keep the balanced preset's
// values, NOT zero them — silently zero-ing the gates would let a typo
// strip every confidence safeguard.
func TestLoadD10Config_InvalidHardness_FallsBackToBalanced(t *testing.T) {
	clearD10Env(t)
	cases := []string{"extra-tight", "heavy", "tightt", "looose", "default"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("MEMDB_D10_HARDNESS", raw)
			c := search.LoadD10Config()
			if c.Hardness != search.HardnessBalanced {
				t.Errorf("invalid hardness %q: got %q, want balanced", raw, c.Hardness)
			}
			if math.Abs(c.ConfidenceThreshold-0.5) > 1e-9 {
				t.Errorf("invalid hardness %q: ConfidenceThreshold = %v, want 0.5 (balanced)", raw, c.ConfidenceThreshold)
			}
		})
	}
}

// TestLoadD10Config_HardnessCaseInsensitive — locks the documented
// "ToLower normalisation" behaviour for hardness env. Operators sometimes
// SHOUT_CASE their YAML; this MUST be tolerated.
func TestLoadD10Config_HardnessCaseInsensitive(t *testing.T) {
	cases := []struct {
		raw  string
		want search.Hardness
	}{
		{"TIGHT", search.HardnessTight},
		{"Tight", search.HardnessTight},
		{"LOOSE", search.HardnessLoose},
		{"  Balanced  ", search.HardnessBalanced},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			clearD10Env(t)
			t.Setenv("MEMDB_D10_HARDNESS", c.raw)
			got := search.LoadD10Config().Hardness
			if got != c.want {
				t.Errorf("raw=%q: got %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// TestLoadD10Config_PromptMode_AllVariants — happy-path table for the
// three documented modes. Locks the env-string ↔ enum mapping that the
// .env.example claims.
func TestLoadD10Config_PromptMode_AllVariants(t *testing.T) {
	cases := []struct {
		raw  string
		want search.PromptMode
	}{
		{"strict", search.PromptModeStrict},
		{"soft", search.PromptModeSoft},
		{"probabilistic", search.PromptModeProbabilistic},
		// Casing tolerance — operators yaml/env files vary.
		{"  strict ", search.PromptModeStrict},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			clearD10Env(t)
			t.Setenv("MEMDB_D10_PROMPT_MODE", c.raw)
			got := search.LoadD10Config().Mode
			if got != c.want {
				t.Errorf("mode raw=%q: got %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// countingEmbedder lets us assert the classifier was (or was not) called.
// Each Embed call increments calls atomically; tests inspect the count
// after PromptForQuery returns.
type countingEmbedder struct {
	calls int64
	err   error
	dim   int
}

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	atomic.AddInt64(&c.calls, 1)
	if c.err != nil {
		return nil, c.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, c.dim)
		// Non-zero so normalise() does not divide by zero.
		out[i][0] = 1
	}
	return out, nil
}

// TestPromptForQuery_StrictSkipsClassifier — embed cost is the dominant
// per-request cost when MEMDB_D10_PROMPT_MODE=probabilistic. The strict
// mode promise is "zero classifier calls", verified here. If this test
// fails the cost story breaks for every operator running A/B sweeps.
func TestPromptForQuery_StrictSkipsClassifier(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_PROMPT_MODE", "strict")
	cfg := search.LoadD10Config()
	emb := &countingEmbedder{dim: 8}

	prompt, hinted := cfg.PromptForQuery(context.Background(), "what is X's job?", emb)

	if got := atomic.LoadInt64(&emb.calls); got != 0 {
		t.Errorf("strict mode called the embedder %d time(s); want 0", got)
	}
	if hinted {
		t.Error("strict mode reported hinted=true; want false (no classifier ⇒ no hint)")
	}
	// Sanity: prompt must look like the strict variant — pre-#249 wording.
	if !strings.Contains(prompt, "Use the exact surface form from the memories") {
		t.Errorf("strict prompt missing pre-#249 marker; got prefix %q", trim(prompt, 120))
	}
	if !strings.Contains(prompt, "respond with \"UNKNOWN\"") {
		t.Errorf("strict prompt missing 'respond with UNKNOWN' rule")
	}
}

// TestPromptForQuery_SoftSkipsClassifier — symmetric to strict. The PR #249
// soft prompt also does not need the classifier — only probabilistic does.
func TestPromptForQuery_SoftSkipsClassifier(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_PROMPT_MODE", "soft")
	cfg := search.LoadD10Config()
	emb := &countingEmbedder{dim: 8}

	prompt, hinted := cfg.PromptForQuery(context.Background(), "what is X's identity?", emb)

	if got := atomic.LoadInt64(&emb.calls); got != 0 {
		t.Errorf("soft mode called the embedder %d time(s); want 0", got)
	}
	if hinted {
		t.Error("soft mode reported hinted=true; want false")
	}
	if !strings.Contains(prompt, "synthesise the answer from the closest grounded fact") {
		t.Errorf("soft prompt missing PR #249 synthesis-permission marker; got %q", trim(prompt, 200))
	}
}

// TestPromptForQuery_ProbabilisticCallsClassifier — the inverse of the
// strict/soft tests. Probabilistic mode MUST call the embedder when one is
// available, otherwise the classifier-driven hint cannot fire.
func TestPromptForQuery_ProbabilisticCallsClassifier(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_PROMPT_MODE", "probabilistic")
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "true")
	cfg := search.LoadD10Config()
	emb := &countingEmbedder{dim: 8}

	cfg.PromptForQuery(context.Background(), "what is X's job?", emb)

	if got := atomic.LoadInt64(&emb.calls); got == 0 {
		t.Error("probabilistic mode did NOT call the embedder; classifier path is broken")
	}
}

// TestPromptForQuery_ProbabilisticNilEmbedder — the existing post-#252
// rollout-safety property: nil embedder collapses to the base prompt
// without touching the classifier cache. We re-test it here so anyone
// changing PromptForQuery sees the contract called out at the new layer.
func TestPromptForQuery_ProbabilisticNilEmbedder(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_PROMPT_MODE", "probabilistic")
	cfg := search.LoadD10Config()

	prompt, hinted := cfg.PromptForQuery(context.Background(), "anything", nil)

	if hinted {
		t.Error("nil embedder reported hinted=true; want false")
	}
	if !strings.Contains(prompt, "SHORTEST surface form") {
		t.Errorf("probabilistic base prompt missing canonical marker; got %q", trim(prompt, 120))
	}
}

// TestPromptForQuery_ProbabilisticEmbedderError — the classifier path must
// degrade gracefully on any embedder error: no panic, hinted=false, base
// prompt returned. This protects the request from a transient embed-server
// outage.
func TestPromptForQuery_ProbabilisticEmbedderError(t *testing.T) {
	clearD10Env(t)
	t.Setenv("MEMDB_D10_PROMPT_MODE", "probabilistic")
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "true")
	cfg := search.LoadD10Config()
	emb := &countingEmbedder{dim: 8, err: errors.New("embed-server down")}

	prompt, hinted := cfg.PromptForQuery(context.Background(), "anything", emb)

	if hinted {
		t.Error("embedder error reported hinted=true; want false (graceful fallback)")
	}
	if !strings.Contains(prompt, "SHORTEST surface form") {
		t.Errorf("base prompt missing on embedder error; got %q", trim(prompt, 120))
	}
}

// TestD10ConfigDefaultsMatchHandlers — guards the cross-package sync
// invariant called out in defaultFactualConfidenceThreshold's doc-comment
// (chat_prompt_softening.go). If someone bumps one default without the
// other, dashboards drift silently.
func TestD10ConfigDefaultsMatchHandlers(t *testing.T) {
	clearD10Env(t)
	c := search.LoadD10Config()
	if math.Abs(c.ConfidenceThreshold-defaultFactualConfidenceThreshold) > 1e-9 {
		t.Errorf("balanced ConfidenceThreshold (%v) does not match handlers' defaultFactualConfidenceThreshold (%v) — defaults drifted",
			c.ConfidenceThreshold, defaultFactualConfidenceThreshold)
	}
}

// trim returns up to n bytes of s for compact error messages.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
