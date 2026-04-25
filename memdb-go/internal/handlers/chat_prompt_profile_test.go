package handlers

// chat_prompt_profile_test.go — unit tests for the M10 Stream 3 user-profile
// prompt section (Memobase-style two-section context pack). Validates:
//   - empty rows render as "(none)";
//   - populated rows render in the input (DB-stable) order;
//   - valid_at suffix renders as "[mention YYYY-MM-DD]";
//   - env gate MEMDB_PROFILE_INJECT=false suppresses the section;
//   - oversized profiles trigger the truncation path;
//   - profile section is always BEFORE the existing memory section.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestFormatProfileSection_Empty(t *testing.T) {
	got := formatProfileSection(context.Background(), nil)
	want := "## User Profile\n(none)\n"
	if got != want {
		t.Errorf("empty profile section = %q, want %q", got, want)
	}

	got2 := formatProfileSection(context.Background(), []db.ProfileEntry{})
	if got2 != want {
		t.Errorf("empty slice profile section = %q, want %q", got2, want)
	}
}

func TestFormatProfileSection_StableOrder(t *testing.T) {
	entries := []db.ProfileEntry{
		{Topic: "work", SubTopic: "title", Memo: "software engineer", Confidence: 1.0},
		{Topic: "basic_info", SubTopic: "name", Memo: "alice", Confidence: 1.0},
		{Topic: "interest", SubTopic: "movie", Memo: "Inception, Interstellar", Confidence: 0.9},
		{Topic: "interest", SubTopic: "music", Memo: "jazz", Confidence: 0.8},
		{Topic: "work", SubTopic: "company", Memo: "ACME", Confidence: 1.0},
	}
	got := formatProfileSection(context.Background(), entries)

	if !strings.HasPrefix(got, "## User Profile\n") {
		t.Errorf("section missing header, got prefix %q", got[:min(len(got), 40)])
	}
	// Each entry rendered as "- topic / sub_topic: memo".
	wantLines := []string{
		"- work / title: software engineer",
		"- basic_info / name: alice",
		"- interest / movie: Inception, Interstellar",
		"- interest / music: jazz",
		"- work / company: ACME",
	}
	body := strings.TrimPrefix(got, "## User Profile\n")
	body = strings.TrimSuffix(body, "\n")
	gotLines := strings.Split(body, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("got %d bullet lines, want %d (got=%v)", len(gotLines), len(wantLines), gotLines)
	}
	for i := range wantLines {
		if gotLines[i] != wantLines[i] {
			t.Errorf("line %d = %q, want %q", i, gotLines[i], wantLines[i])
		}
	}
}

func TestFormatProfileSection_ValidAtRendered(t *testing.T) {
	when := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []db.ProfileEntry{
		{Topic: "interest", SubTopic: "movie", Memo: "Inception", Confidence: 1.0, ValidAt: &when},
	}
	got := formatProfileSection(context.Background(), entries)
	if !strings.Contains(got, "[mention 2025-01-01]") {
		t.Errorf("section missing valid_at suffix, got %q", got)
	}
	if !strings.Contains(got, "- interest / movie: Inception [mention 2025-01-01]") {
		t.Errorf("section missing fully-rendered line with mention, got %q", got)
	}
}

func TestFormatProfileSection_TruncatesOnOversize(t *testing.T) {
	// Build entries totalling well above 1000 approximate tokens (~4000 chars).
	// 50 entries × ~120 char memo each → ~6000 chars → ~1500 tokens.
	bigMemo := strings.Repeat("x", 120)
	entries := make([]db.ProfileEntry, 50)
	for i := range entries {
		conf := float32(i) / 100 // ascending: lowest-conf rows are at the front
		entries[i] = db.ProfileEntry{
			Topic:      "topic",
			SubTopic:   "sub",
			Memo:       bigMemo,
			Confidence: conf,
		}
	}

	got := formatProfileSection(context.Background(), entries)
	if approxTokens(got) > profileMaxApproxToken+50 {
		t.Errorf("section not truncated: %d approx tokens (cap %d)", approxTokens(got), profileMaxApproxToken)
	}
	if !strings.HasPrefix(got, "## User Profile\n") {
		t.Errorf("truncated section missing header")
	}
	// Should still contain at least one bullet (not collapse to empty).
	if !strings.Contains(got, "- topic / sub: ") {
		t.Errorf("truncated section dropped all bullets, got %q", got[:min(len(got), 80)])
	}
}

func TestProfileInjectEnabled_DefaultTrue(t *testing.T) {
	t.Setenv("MEMDB_PROFILE_INJECT", "")
	// Setenv with "" still counts as "set"; emulate "unset" via Unsetenv.
	if err := unsetenvForTest(); err != nil {
		t.Fatal(err)
	}
	if !profileInjectEnabled() {
		t.Error("default (unset) MEMDB_PROFILE_INJECT should enable profile injection")
	}
}

func TestProfileInjectEnabled_ExplicitTrue(t *testing.T) {
	t.Setenv("MEMDB_PROFILE_INJECT", "true")
	if !profileInjectEnabled() {
		t.Error("MEMDB_PROFILE_INJECT=true should enable")
	}
}

func TestProfileInjectEnabled_FalseyValues(t *testing.T) {
	for _, v := range []string{"0", "false", "False", "FALSE", "no", "off", " 0 "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("MEMDB_PROFILE_INJECT", v)
			if profileInjectEnabled() {
				t.Errorf("MEMDB_PROFILE_INJECT=%q should disable injection", v)
			}
		})
	}
}

func TestBuildSystemPromptWithProfile_PrependsBeforeMemorySection(t *testing.T) {
	ctx := context.Background()
	entries := []db.ProfileEntry{
		{Topic: "work", SubTopic: "title", Memo: "software engineer", Confidence: 1.0},
	}
	profileSection := formatProfileSection(ctx, entries)

	memories := []map[string]any{{"memory": "User likes Go"}}
	prompt := buildSystemPromptWithProfile(ctx, "Hello", memories, "", "", "", profileSection)

	profileIdx := strings.Index(prompt, "## User Profile")
	memoryIdx := strings.Index(prompt, "# Memory Data")
	if profileIdx < 0 {
		t.Fatalf("prompt missing '## User Profile' header, got %q", truncate(prompt, 200))
	}
	if memoryIdx < 0 {
		t.Fatalf("prompt missing '# Memory Data' header (existing template), got %q", truncate(prompt, 200))
	}
	if profileIdx >= memoryIdx {
		t.Errorf("profile section (%d) must precede memory section (%d)", profileIdx, memoryIdx)
	}
	if !strings.Contains(prompt, "- work / title: software engineer") {
		t.Errorf("rendered prompt missing profile bullet")
	}
}

func TestBuildSystemPromptWithProfile_EmptyProfileSectionSkipped(t *testing.T) {
	// Empty profile section means "do not prepend" — memory-only path is
	// indistinguishable from M9 baseline (additive contract).
	ctx := context.Background()
	memories := []map[string]any{{"memory": "User likes Go"}}
	prompt := buildSystemPromptWithProfile(ctx, "Hello", memories, "", "", "", "")
	if strings.Contains(prompt, "## User Profile") {
		t.Errorf("empty profileSection must not introduce a profile header, got %q", truncate(prompt, 200))
	}
	// Sanity: existing memory rendering still happens.
	if !strings.Contains(prompt, "1. User likes Go") {
		t.Errorf("prompt missing existing memory rendering")
	}
}

func TestBuildSystemPromptWithProfile_BackwardCompatWrapper(t *testing.T) {
	// The thin buildSystemPrompt wrapper preserves M9 callers exactly.
	memories := []map[string]any{{"memory": "fact"}}
	got := buildSystemPrompt("hello", memories, "", "", "")
	if strings.Contains(got, "## User Profile") {
		t.Errorf("buildSystemPrompt (no-profile shim) introduced profile header: %q", truncate(got, 200))
	}
}

func TestBuildSystemPromptWithProfile_PrependsToCustomBase(t *testing.T) {
	// Two-section ordering must hold even when basePrompt wins.
	ctx := context.Background()
	entries := []db.ProfileEntry{
		{Topic: "work", SubTopic: "title", Memo: "engineer", Confidence: 1.0},
	}
	section := formatProfileSection(ctx, entries)
	got := buildSystemPromptWithProfile(ctx, "q", nil, "", "Custom system prompt.", "", section)

	if !strings.HasPrefix(got, "## User Profile\n") {
		t.Errorf("custom-base path lost profile prefix, got %q", truncate(got, 80))
	}
	if !strings.Contains(got, "Custom system prompt.") {
		t.Errorf("custom basePrompt missing from output, got %q", truncate(got, 200))
	}
	if strings.Index(got, "## User Profile") >= strings.Index(got, "Custom system prompt.") {
		t.Errorf("profile section must precede the custom basePrompt")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// unsetenvForTest removes MEMDB_PROFILE_INJECT from the environment for the
// duration of the calling test. t.Setenv("", "") would still mark the var as
// set; this helper uses os.Unsetenv directly. Safe because t.Setenv (called
// before this) registers a Cleanup that restores the original value.
func unsetenvForTest() error {
	return os.Unsetenv("MEMDB_PROFILE_INJECT")
}
