package config

import (
	"os"
	"testing"
	"time"
)

// setEnv sets an env var for the duration of a test and restores it after.
func setEnv(t *testing.T, key, val string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// unsetEnv clears an env var for the duration of a test and restores it after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		}
	})
}

// TestLoad_CrossEncoderDefaults verifies default values when no env vars are set.
func TestLoad_CrossEncoderDefaults(t *testing.T) {
	unsetEnv(t, "CROSS_ENCODER_URL")
	unsetEnv(t, "CROSS_ENCODER_MODEL")
	unsetEnv(t, "CROSS_ENCODER_TIMEOUT_MS")
	unsetEnv(t, "CROSS_ENCODER_MAX_DOCS")

	cfg := Load()
	if cfg.CrossEncoderURL != "http://embed-server:8082" {
		t.Errorf("default URL mismatch: %q", cfg.CrossEncoderURL)
	}
	if cfg.CrossEncoderModel != "gte-multi-rerank" {
		t.Errorf("default model mismatch: %q", cfg.CrossEncoderModel)
	}
	if cfg.CrossEncoderTimeout != 2000*time.Millisecond {
		t.Errorf("default timeout mismatch: %v", cfg.CrossEncoderTimeout)
	}
	if cfg.CrossEncoderMaxDocs != 50 {
		t.Errorf("default max docs mismatch: %d", cfg.CrossEncoderMaxDocs)
	}
}

// TestLoad_CrossEncoderOverrides verifies env vars override defaults.
func TestLoad_CrossEncoderOverrides(t *testing.T) {
	setEnv(t, "CROSS_ENCODER_URL", "http://custom:9000")
	setEnv(t, "CROSS_ENCODER_MODEL", "custom-model")
	setEnv(t, "CROSS_ENCODER_TIMEOUT_MS", "5000")
	setEnv(t, "CROSS_ENCODER_MAX_DOCS", "25")

	cfg := Load()
	if cfg.CrossEncoderURL != "http://custom:9000" {
		t.Errorf("URL override mismatch: %q", cfg.CrossEncoderURL)
	}
	if cfg.CrossEncoderModel != "custom-model" {
		t.Errorf("model override mismatch: %q", cfg.CrossEncoderModel)
	}
	if cfg.CrossEncoderTimeout != 5000*time.Millisecond {
		t.Errorf("timeout override mismatch: %v", cfg.CrossEncoderTimeout)
	}
	if cfg.CrossEncoderMaxDocs != 25 {
		t.Errorf("max docs override mismatch: %d", cfg.CrossEncoderMaxDocs)
	}
}

// TestLoad_CrossEncoderEmptyURLDisables verifies that setting URL to empty
// string disables the feature (zero-value behavior).
func TestLoad_CrossEncoderEmptyURLDisables(t *testing.T) {
	setEnv(t, "CROSS_ENCODER_URL", "")
	unsetEnv(t, "CROSS_ENCODER_MODEL")

	cfg := Load()
	// Empty value in env falls back to default — documenting this explicitly.
	// Operators who want to disable must unset the var entirely.
	if cfg.CrossEncoderURL != "http://embed-server:8082" {
		t.Errorf("empty env should fall back to default, got %q", cfg.CrossEncoderURL)
	}
}

// ── MEMDB_DEFAULT_ANSWER_STYLE tests ──────────────────────────────────────────

func TestLoad_DefaultAnswerStyle_Unset(t *testing.T) {
	unsetEnv(t, "MEMDB_DEFAULT_ANSWER_STYLE")
	cfg := Load()
	if cfg.DefaultAnswerStyle != "" {
		t.Errorf("unset env should produce empty default, got %q", cfg.DefaultAnswerStyle)
	}
}

func TestLoad_DefaultAnswerStyle_Conversational(t *testing.T) {
	setEnv(t, "MEMDB_DEFAULT_ANSWER_STYLE", "conversational")
	cfg := Load()
	if cfg.DefaultAnswerStyle != "conversational" {
		t.Errorf("expected conversational, got %q", cfg.DefaultAnswerStyle)
	}
}

func TestLoad_DefaultAnswerStyle_Factual(t *testing.T) {
	setEnv(t, "MEMDB_DEFAULT_ANSWER_STYLE", "factual")
	cfg := Load()
	if cfg.DefaultAnswerStyle != "factual" {
		t.Errorf("expected factual, got %q", cfg.DefaultAnswerStyle)
	}
}

func TestLoad_DefaultAnswerStyle_BadValueFallback(t *testing.T) {
	setEnv(t, "MEMDB_DEFAULT_ANSWER_STYLE", "loud")
	cfg := Load()
	// Bad value must fall back to conversational (no crash, warn + fallback).
	if cfg.DefaultAnswerStyle != "conversational" {
		t.Errorf("bad value should fall back to conversational, got %q", cfg.DefaultAnswerStyle)
	}
}

// ── MEMDB_FACTUAL_CANARY_PCT tests ────────────────────────────────────────────

func TestLoad_FactualCanaryPct_Unset(t *testing.T) {
	unsetEnv(t, "MEMDB_FACTUAL_CANARY_PCT")
	cfg := Load()
	if cfg.FactualCanaryPct != 0 {
		t.Errorf("unset env should produce 0 (canary off), got %d", cfg.FactualCanaryPct)
	}
}

func TestLoad_FactualCanaryPct_ValidTen(t *testing.T) {
	setEnv(t, "MEMDB_FACTUAL_CANARY_PCT", "10")
	cfg := Load()
	if cfg.FactualCanaryPct != 10 {
		t.Errorf("expected 10, got %d", cfg.FactualCanaryPct)
	}
}

func TestLoad_FactualCanaryPct_ValidHundred(t *testing.T) {
	setEnv(t, "MEMDB_FACTUAL_CANARY_PCT", "100")
	cfg := Load()
	if cfg.FactualCanaryPct != 100 {
		t.Errorf("expected 100, got %d", cfg.FactualCanaryPct)
	}
}

func TestLoad_FactualCanaryPct_ClampAbove100(t *testing.T) {
	setEnv(t, "MEMDB_FACTUAL_CANARY_PCT", "150")
	cfg := Load()
	if cfg.FactualCanaryPct != 100 {
		t.Errorf("value > 100 should clamp to 100, got %d", cfg.FactualCanaryPct)
	}
}

func TestLoad_FactualCanaryPct_ClampBelow0(t *testing.T) {
	setEnv(t, "MEMDB_FACTUAL_CANARY_PCT", "-5")
	cfg := Load()
	if cfg.FactualCanaryPct != 0 {
		t.Errorf("value < 0 should clamp to 0, got %d", cfg.FactualCanaryPct)
	}
}

// ── D11 CoT decomposer env vars ──────────────────────────────────────────────

func TestLoad_CoTDecompose_DefaultsOff(t *testing.T) {
	unsetEnv(t, "MEMDB_COT_DECOMPOSE")
	unsetEnv(t, "MEMDB_COT_MAX_SUBQUERIES")
	unsetEnv(t, "MEMDB_COT_TIMEOUT_MS")
	cfg := Load()
	if cfg.CoTDecompose {
		t.Errorf("expected default false, got %v", cfg.CoTDecompose)
	}
	if cfg.CoTMaxSubqueries != 3 {
		t.Errorf("expected default 3, got %d", cfg.CoTMaxSubqueries)
	}
	if cfg.CoTTimeoutMS != 2000 {
		t.Errorf("expected default 2000, got %d", cfg.CoTTimeoutMS)
	}
}

func TestLoad_CoTDecompose_Override(t *testing.T) {
	setEnv(t, "MEMDB_COT_DECOMPOSE", "true")
	setEnv(t, "MEMDB_COT_MAX_SUBQUERIES", "4")
	setEnv(t, "MEMDB_COT_TIMEOUT_MS", "5000")
	cfg := Load()
	if !cfg.CoTDecompose {
		t.Errorf("expected true")
	}
	if cfg.CoTMaxSubqueries != 4 {
		t.Errorf("got %d, want 4", cfg.CoTMaxSubqueries)
	}
	if cfg.CoTTimeoutMS != 5000 {
		t.Errorf("got %d, want 5000", cfg.CoTTimeoutMS)
	}
}

func TestLoad_CoTDecompose_ClampMaxSubqueries(t *testing.T) {
	setEnv(t, "MEMDB_COT_MAX_SUBQUERIES", "99")
	cfg := Load()
	if cfg.CoTMaxSubqueries != 5 {
		t.Errorf("expected clamp to 5, got %d", cfg.CoTMaxSubqueries)
	}
	setEnv(t, "MEMDB_COT_MAX_SUBQUERIES", "0")
	cfg = Load()
	if cfg.CoTMaxSubqueries != 1 {
		t.Errorf("expected clamp to 1, got %d", cfg.CoTMaxSubqueries)
	}
}

func TestLoad_CoTDecompose_ClampTimeout(t *testing.T) {
	setEnv(t, "MEMDB_COT_TIMEOUT_MS", "100")
	cfg := Load()
	if cfg.CoTTimeoutMS != 500 {
		t.Errorf("expected clamp to 500, got %d", cfg.CoTTimeoutMS)
	}
	setEnv(t, "MEMDB_COT_TIMEOUT_MS", "60000")
	cfg = Load()
	if cfg.CoTTimeoutMS != 10000 {
		t.Errorf("expected clamp to 10000, got %d", cfg.CoTTimeoutMS)
	}
}
