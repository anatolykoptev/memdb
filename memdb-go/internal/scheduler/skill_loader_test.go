package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchedulerCatalog_AllSkillsLoad(t *testing.T) {
	for _, name := range SchedulerSkillNames {
		body := schedulerPrompt(context.Background(), name)
		if body == "" {
			t.Errorf("skill %q has empty body", name)
		}
		// Each scheduler prompt should mention "memory" — they're all
		// memory-management roles.
		if !strings.Contains(strings.ToLower(body), "memory") {
			t.Errorf("skill %q body does not mention 'memory' — wrong file? first 80 chars: %.80s", name, body)
		}
	}
}

func TestSchedulerCatalog_UnknownSkill_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on unknown skill name, got nil")
		}
	}()
	_ = schedulerPrompt(context.Background(), "nonexistent-skill")
}

func TestSchedulerSkillDiagnostic_Lists8Skills(t *testing.T) {
	d := SchedulerSkillDiagnostic()
	if !strings.Contains(d, "8 skills loaded") {
		t.Errorf("expected '8 skills loaded' in diagnostic, got %q", d)
	}
}

func TestSchedulerCatalog_WorkspaceOverride(t *testing.T) {
	// Workspace skill replaces builtin one with same name.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "memory-consolidator")
	if err := os.MkdirAll(skillDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workspaceBody := "Workspace-overridden memory-consolidator body."
	content := "---\nname: memory-consolidator\ndescription: workspace override\n---\n" + workspaceBody
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(schedulerSkillsDirEnv, dir)
	cat := buildSchedulerCatalog()
	body, _, ok := cat.Load("memory-consolidator")
	if !ok {
		t.Fatal("expected workspace skill to load")
	}
	if !strings.Contains(body, workspaceBody) {
		t.Errorf("expected workspace body, got %.80q", body)
	}
}

func TestSchedulerCatalog_WorkspaceFallthrough(t *testing.T) {
	// Workspace dir set but skill not present → fall through to builtin.
	dir := t.TempDir()
	t.Setenv(schedulerSkillsDirEnv, dir)
	cat := buildSchedulerCatalog()
	body, _, ok := cat.Load("memory-consolidator")
	if !ok {
		t.Fatal("expected fallthrough to builtin")
	}
	// Builtin body mentions "memory" — verifies we fell through.
	if !strings.Contains(strings.ToLower(body), "memory") {
		t.Errorf("expected builtin body, got %.80q", body)
	}
}

func TestSchedulerCatalog_NoWorkspace_BuiltinOnly(t *testing.T) {
	// Env unset → single-tier embedded behavior.
	t.Setenv(schedulerSkillsDirEnv, "")
	cat := buildSchedulerCatalog()
	body, _, ok := cat.Load("memory-consolidator")
	if !ok {
		t.Fatal("expected builtin skill to load")
	}
	if body == "" {
		t.Error("expected non-empty builtin body")
	}
}

func TestSchedulerSkillDiagnostic_WithWorkspace(t *testing.T) {
	// Note: SchedulerSkillDiagnostic uses the package-level singleton
	// schedulerCatalog, not buildSchedulerCatalog(). The singleton was
	// constructed at process init time — its tier list is frozen from
	// that moment. However, SchedulerSkillDiagnostic reads the env var
	// directly to format the diagnostic string, so this test verifies
	// the string formatting path even though the underlying catalog
	// tiers still reflect the init-time snapshot.
	dir := t.TempDir()
	t.Setenv(schedulerSkillsDirEnv, dir)
	d := SchedulerSkillDiagnostic()
	if !strings.Contains(d, "workspace=") || !strings.Contains(d, dir) {
		t.Errorf("expected diagnostic to mention workspace dir, got %q", d)
	}
}
