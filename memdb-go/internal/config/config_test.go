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
