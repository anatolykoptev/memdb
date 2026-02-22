package search

import (
	"testing"
)

func TestLookupProfile(t *testing.T) {
	t.Run("known profiles", func(t *testing.T) {
		for _, name := range []string{"inject", "default", "deep"} {
			p, err := LookupProfile(name)
			if err != nil {
				t.Errorf("LookupProfile(%q) returned error: %v", name, err)
			}
			_ = p
		}
	})

	t.Run("empty string returns no-op", func(t *testing.T) {
		p, err := LookupProfile("")
		if err != nil {
			t.Fatalf("LookupProfile(\"\") returned error: %v", err)
		}
		// All fields should be nil (zero value).
		if p.TopK != nil || p.Dedup != nil || p.Relativity != nil {
			t.Error("expected zero ProfileOverrides for empty name")
		}
	})

	t.Run("unknown name returns error", func(t *testing.T) {
		_, err := LookupProfile("nonexistent")
		if err == nil {
			t.Error("expected error for unknown profile")
		}
	})
}

func TestApplyProfile(t *testing.T) {
	base := SearchParams{
		TopK:         DefaultTextTopK,
		SkillTopK:    DefaultSkillTopK,
		PrefTopK:     DefaultPrefTopK,
		ToolTopK:     DefaultToolTopK,
		Dedup:        "no",
		Relativity:   0,
		IncludeSkill: true,
		IncludePref:  true,
		IncludeTool:  false,
		NumStages:    0,
	}

	t.Run("inject overrides all fields", func(t *testing.T) {
		prof, _ := LookupProfile("inject")
		result := ApplyProfile(base, prof)

		if result.TopK != 5 {
			t.Errorf("TopK = %d, want 5", result.TopK)
		}
		if result.SkillTopK != 0 {
			t.Errorf("SkillTopK = %d, want 0", result.SkillTopK)
		}
		if result.PrefTopK != 0 {
			t.Errorf("PrefTopK = %d, want 0", result.PrefTopK)
		}
		if result.ToolTopK != 0 {
			t.Errorf("ToolTopK = %d, want 0", result.ToolTopK)
		}
		if result.Dedup != "mmr" {
			t.Errorf("Dedup = %q, want %q", result.Dedup, "mmr")
		}
		if result.Relativity != 0.93 {
			t.Errorf("Relativity = %f, want 0.93", result.Relativity)
		}
		if result.NumStages != 0 {
			t.Errorf("NumStages = %d, want 0", result.NumStages)
		}
		if result.DecayAlpha != 0.01 {
			t.Errorf("DecayAlpha = %f, want 0.01", result.DecayAlpha)
		}
		if result.IncludeSkill {
			t.Error("IncludeSkill = true, want false")
		}
		if result.IncludePref {
			t.Error("IncludePref = true, want false")
		}
		if result.IncludeTool {
			t.Error("IncludeTool = true, want false")
		}
	})

	t.Run("empty profile is no-op", func(t *testing.T) {
		prof, _ := LookupProfile("default")
		result := ApplyProfile(base, prof)

		if result.TopK != base.TopK {
			t.Errorf("TopK = %d, want %d", result.TopK, base.TopK)
		}
		if result.Dedup != base.Dedup {
			t.Errorf("Dedup = %q, want %q", result.Dedup, base.Dedup)
		}
		if result.IncludeSkill != base.IncludeSkill {
			t.Errorf("IncludeSkill = %v, want %v", result.IncludeSkill, base.IncludeSkill)
		}
	})

	t.Run("partial override preserves unset fields", func(t *testing.T) {
		prof, _ := LookupProfile("deep")
		result := ApplyProfile(base, prof)

		if result.TopK != 10 {
			t.Errorf("TopK = %d, want 10", result.TopK)
		}
		// Unset fields should keep base values.
		if result.SkillTopK != base.SkillTopK {
			t.Errorf("SkillTopK = %d, want %d (base)", result.SkillTopK, base.SkillTopK)
		}
		if result.IncludeSkill != base.IncludeSkill {
			t.Errorf("IncludeSkill = %v, want %v (base)", result.IncludeSkill, base.IncludeSkill)
		}
	})
}
