package llm

// profile_personas_test.go — unit tests for MEMDB_PROFILE_TAXONOMY persona switch.

import (
	"strings"
	"testing"
)

func TestTaxonomyForPersona_Default(t *testing.T) {
	got := TaxonomyForPersona(PersonaDefault)
	// The default is the M10 8-topic list; spot-check a few canonical topics.
	for _, want := range []string{"basic_info", "contact_info", "education", "demographics", "work", "interest", "psychological", "life_event"} {
		if !strings.Contains(got, want) {
			t.Errorf("default taxonomy missing topic %q", want)
		}
	}
}

func TestTaxonomyForPersona_Locomo(t *testing.T) {
	got := TaxonomyForPersona(PersonaLocomo)
	for _, want := range []string{"personal_narrative", "life_circumstances", "personal_growth", "plans", "basic_info"} {
		if !strings.Contains(got, want) {
			t.Errorf("locomo taxonomy missing topic %q", want)
		}
	}
	// LoCoMo must NOT contain default-only topics like "contact_info" or "demographics".
	for _, absent := range []string{"contact_info", "demographics", "psychological", "life_event"} {
		if strings.Contains(got, absent) {
			t.Errorf("locomo taxonomy unexpectedly contains default topic %q", absent)
		}
	}
}

func TestTaxonomyForPersona_Unknown_FallsBackToDefault(t *testing.T) {
	def := TaxonomyForPersona(PersonaDefault)
	got := TaxonomyForPersona("bogus_persona_that_does_not_exist")
	if got != def {
		t.Errorf("unknown persona should fall back to default; got %q", got[:min(80, len(got))])
	}
}

func TestTaxonomyForPersona_AllFive(t *testing.T) {
	personas := []string{
		PersonaDefault,
		PersonaLocomo,
		PersonaAssistant,
		PersonaCompanion,
		PersonaEducation,
	}

	seen := make(map[string]bool)
	for _, p := range personas {
		got := TaxonomyForPersona(p)
		if got == "" {
			t.Errorf("persona %q returned empty taxonomy", p)
		}
		if seen[got] {
			t.Errorf("persona %q returned duplicate taxonomy (same as an earlier persona)", p)
		}
		seen[got] = true
	}
}

// TestCountTopics verifies the top-level topic counter handles all five
// shipped taxonomies plus the edge cases (empty / no leading dash).
//
// The expected counts come from the verbatim ports above:
//   - default:   8 (basic_info, contact_info, education, demographics, work,
//                interest, psychological, life_event)
//   - locomo:    5 (basic_info, personal_narrative, life_circumstances,
//                personal_growth, plans)
//   - assistant: 6 (basic_info, schedule_prefs, task_management,
//                productivity_settings, lifestyle_prefs, communication_style)
//   - companion: 4 (basic_info, companion_preferences, interaction_history,
//                personalization)
//   - education: 5 (basic_info, academic_profile, learning_preferences,
//                progress_tracking, engagement_metrics)
func TestCountTopics(t *testing.T) {
	cases := []struct {
		persona string
		want    int
	}{
		{PersonaDefault, 8},
		{PersonaLocomo, 5},
		{PersonaAssistant, 6},
		{PersonaCompanion, 4},
		{PersonaEducation, 5},
	}
	for _, tc := range cases {
		got := countTopics(TaxonomyForPersona(tc.persona))
		if got != tc.want {
			t.Errorf("countTopics(%q) = %d; want %d", tc.persona, got, tc.want)
		}
	}
}

// TestCountTopics_EdgeCases covers empty, no-prefix, and indented-only inputs.
func TestCountTopics_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"no prefix", "basic_info\nwork\n", 0},
		{"indented only", "  - sub1\n  - sub2\n", 0},
		{"single", "- only\n", 1},
		{"mixed", "- a\n  - x\n- b\n  - y\n  - z\n- c\n", 3},
	}
	for _, tc := range cases {
		if got := countTopics(tc.in); got != tc.want {
			t.Errorf("countTopics(%s) = %d; want %d", tc.name, got, tc.want)
		}
	}
}

// TestBuildProfileFactRetrievalPromptWith_InjectsGuidelines verifies that
// the persona guidelines block is injected into the prompt body correctly.
func TestBuildProfileFactRetrievalPromptWith_InjectsGuidelines(t *testing.T) {
	marker := "unique_test_topic_marker_xyz"
	guidelines := "- " + marker + "\n  - sub1\n"
	prompt := buildProfileFactRetrievalPromptWith(guidelines)
	if !strings.Contains(prompt, marker) {
		t.Errorf("prompt does not contain injected guidelines marker %q", marker)
	}
	// The static Memobase fragments must still be present regardless of persona.
	for _, want := range []string{
		"You are a professional psychologist.",
		"#### Topics Guidelines",
		"You'll be given some user-relatedtopics and subtopics",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing required static fragment: %q", want)
		}
	}
}

