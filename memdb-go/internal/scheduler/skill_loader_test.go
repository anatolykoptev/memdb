package scheduler

import (
	"strings"
	"testing"
)

func TestSchedulerCatalog_AllSkillsLoad(t *testing.T) {
	for _, name := range SchedulerSkillNames {
		body := schedulerPrompt(name)
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
	_ = schedulerPrompt("nonexistent-skill")
}

func TestSchedulerSkillDiagnostic_Lists8Skills(t *testing.T) {
	d := SchedulerSkillDiagnostic()
	if !strings.Contains(d, "8 skills loaded") {
		t.Errorf("expected '8 skills loaded' in diagnostic, got %q", d)
	}
}
