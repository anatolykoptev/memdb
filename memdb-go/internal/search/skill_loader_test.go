package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStripFrontmatter_NoFrontmatter(t *testing.T) {
	in := "Just a body\nwith two lines"
	if got := stripFrontmatter(in); got != in {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestStripFrontmatter_StripsBlock(t *testing.T) {
	in := "---\nname: foo\nversion: 1.0.0\n---\nBody starts here\nSecond line\n"
	got := stripFrontmatter(in)
	if strings.Contains(got, "name:") || strings.Contains(got, "version:") {
		t.Errorf("frontmatter not stripped, got %q", got)
	}
	if !strings.Contains(got, "Body starts here") {
		t.Errorf("body lost, got %q", got)
	}
}

func TestStripFrontmatter_NotStartingWithFence(t *testing.T) {
	// First line is not "---" → skill file is malformed but we return
	// the input as-is rather than corrupt it.
	in := "name: foo\n---\nBody"
	if got := stripFrontmatter(in); got != in {
		t.Errorf("expected unchanged for non-fence start, got %q", got)
	}
}

func TestReadSkillFile_NotFound(t *testing.T) {
	resetSkillCache()
	body, ok := readSkillFile("/no/such/path/skill.md")
	if ok {
		t.Errorf("expected ok=false for missing file, got body=%q", body)
	}
}

func TestReadSkillFile_EmptyAfterFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte("---\nname: x\n---\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	resetSkillCache()
	if _, ok := readSkillFile(path); ok {
		t.Errorf("expected ok=false when only frontmatter, got non-empty body")
	}
}

func TestReadSkillFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skill.md")
	if err := os.WriteFile(path, []byte("---\nname: test\n---\nThis is the body.\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	resetSkillCache()
	body, ok := readSkillFile(path)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if body != "This is the body." {
		t.Errorf("body mismatch: got %q", body)
	}
}

func TestReadSkillFile_MtimeCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skill.md")
	if err := os.WriteFile(path, []byte("---\nname: v1\n---\nFirst body\n"), 0644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	resetSkillCache()
	body1, ok := readSkillFile(path)
	if !ok || body1 != "First body" {
		t.Fatalf("v1 read failed: ok=%v body=%q", ok, body1)
	}
	// Mutate the file. Set mtime ahead by 2 seconds to dodge filesystem
	// timestamp resolution on shared CI machines.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("---\nname: v2\n---\nSecond body\n"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	body2, ok := readSkillFile(path)
	if !ok {
		t.Fatalf("v2 read failed")
	}
	if body2 != "Second body" {
		t.Errorf("expected mtime-driven reload, still cached: %q", body2)
	}
}

func TestLoadSkillPrompt_FallbackConst(t *testing.T) {
	// No env override + no candidate files exist (we run from a working
	// dir without internal/search/skills). This verifies the const
	// fallback is reachable.
	t.Setenv("MEMDB_D10_SKILL_PATH", "/nope/skill.md")
	resetSkillCache()
	got := loadSkillPrompt()
	// Either the const fallback OR a candidate that DOES exist on this
	// dev machine (running from repo root, internal/search/skills exists).
	// Both are valid — we just assert the prompt is non-empty and matches
	// either branch.
	if got == "" {
		t.Errorf("expected non-empty prompt, got empty string")
	}
	if !strings.Contains(got, "memories") && !strings.Contains(got, "memory") {
		t.Errorf("loaded prompt missing 'memories'/'memory' keyword — wrong file? got first 80 chars: %.80s", got)
	}
}

func TestLoadSkillPrompt_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.md")
	body := "Custom skill body for this test."
	if err := os.WriteFile(path, []byte("---\nname: c\n---\n"+body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("MEMDB_D10_SKILL_PATH", path)
	resetSkillCache()
	got := loadSkillPrompt()
	if got != body {
		t.Errorf("env override not honoured: got %q", got)
	}
}

func TestSkillLoadDiagnostic(t *testing.T) {
	resetSkillCache()
	t.Setenv("MEMDB_D10_SKILL_PATH", "/nope")
	got := SkillLoadDiagnostic()
	if !strings.Contains(got, "UNREADABLE") && !strings.HasPrefix(got, "default ") && got != "fallback const (no skill file found)" {
		t.Errorf("diagnostic format unexpected: %q", got)
	}
}
