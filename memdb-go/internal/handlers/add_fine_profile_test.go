package handlers

// add_fine_profile_test.go — unit tests for the M10 Stream 2 fire-and-forget
// hook (env gate, missing deps). Live-Postgres assertions live in
// add_fine_profile_livepg_test.go behind the `livepg` build tag.

import (
	"log/slog"
	"os"
	"testing"
)

func TestProfileExtractEnabled_DefaultTrue(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "")
	if !profileExtractEnabled() {
		t.Errorf("expected default enabled when MEMDB_PROFILE_EXTRACT is empty")
	}
}

func TestProfileExtractEnabled_FalseDisables(t *testing.T) {
	for _, v := range []string{"false", "0", "FALSE", "False"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(profileExtractEnvVar, v)
			if profileExtractEnabled() {
				t.Errorf("expected disabled when MEMDB_PROFILE_EXTRACT=%q", v)
			}
		})
	}
}

func TestProfileExtractEnabled_TruthyEnables(t *testing.T) {
	for _, v := range []string{"true", "1", "yes", "anything-else"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(profileExtractEnvVar, v)
			if !profileExtractEnabled() {
				t.Errorf("expected enabled when MEMDB_PROFILE_EXTRACT=%q", v)
			}
		})
	}
}

func TestTriggerProfileExtract_MissingDeps(t *testing.T) {
	// No postgres / extractor → must short-circuit and never panic.
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "user1") {
		t.Errorf("expected false when handler has no postgres/llmExtractor")
	}
}

func TestTriggerProfileExtract_DisabledByEnv(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "false")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "user1") {
		t.Errorf("expected false when MEMDB_PROFILE_EXTRACT=false")
	}
}

func TestTriggerProfileExtract_EmptyUserID(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "true")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "") {
		t.Errorf("expected false when user_id is empty")
	}
}
