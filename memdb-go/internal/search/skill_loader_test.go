package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillPrompt_EmbeddedDefault(t *testing.T) {
	t.Setenv("MEMDB_D10_SKILL_PATH", "")
	body := loadSkillPrompt()
	if body == "" {
		t.Fatal("expected non-empty embedded body")
	}
	if !strings.Contains(body, "memories") && !strings.Contains(body, "memory") {
		t.Errorf("embedded prompt missing 'memories' keyword — first 80 chars: %.80s", body)
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
	// Skillkit caches by mtime; new instance ensures clean state, but the
	// production singleton is what loadSkillPrompt uses. Test through the
	// shim end-to-end, accepting that this asserts the live d10Skill
	// instance honours the override.
	got := loadSkillPrompt()
	if got != body {
		t.Errorf("env override not honoured: got %q", got)
	}
}

func TestSkillLoadDiagnostic_FormatStrings(t *testing.T) {
	t.Setenv("MEMDB_D10_SKILL_PATH", "")
	if SkillLoadDiagnostic() != "embedded default" {
		t.Errorf("expected 'embedded default', got %q", SkillLoadDiagnostic())
	}
	t.Setenv("MEMDB_D10_SKILL_PATH", "/nonexistent/path")
	got := SkillLoadDiagnostic()
	if !strings.Contains(got, "UNREADABLE") {
		t.Errorf("expected UNREADABLE in diagnostic, got %q", got)
	}
}
