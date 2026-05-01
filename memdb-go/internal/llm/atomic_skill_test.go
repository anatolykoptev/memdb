package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicSkill_EmbeddedDefault(t *testing.T) {
	t.Setenv("MEMDB_ATOMIC_SKILL_PATH", "")
	body := atomicSkill.Body()
	if body == "" {
		t.Fatal("expected non-empty embedded body")
	}
	if !strings.Contains(body, "ROLE") {
		t.Errorf("embedded prompt missing 'ROLE' header — first 80 chars: %.80s", body)
	}
}

func TestAtomicSkill_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.md")
	body := "Custom atomic prompt body for this test."
	if err := os.WriteFile(path, []byte("---\nname: custom\n---\n"+body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("MEMDB_ATOMIC_SKILL_PATH", path)
	got := atomicSkill.Body()
	if got != body {
		t.Errorf("env override not honoured: got %q", got)
	}
}

func TestAtomicSkillDiagnostic_FormatStrings(t *testing.T) {
	t.Setenv("MEMDB_ATOMIC_SKILL_PATH", "")
	if AtomicSkillDiagnostic() != "embedded default" {
		t.Errorf("expected 'embedded default', got %q", AtomicSkillDiagnostic())
	}
	t.Setenv("MEMDB_ATOMIC_SKILL_PATH", "/nonexistent/path")
	got := AtomicSkillDiagnostic()
	if !strings.Contains(got, "UNREADABLE") {
		t.Errorf("expected UNREADABLE in diagnostic, got %q", got)
	}
}
