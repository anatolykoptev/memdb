package llm

// profile_personas.go — persona-based taxonomy switch for profile extraction.
//
// MEMDB_PROFILE_TAXONOMY selects the topic/sub-topic list injected into the
// Memobase profile-extraction prompt. Valid values:
//
//	default   — M10 verbatim 8-topic list from user_profile_topics.py (default)
//	locomo    — 5-topic list from the LoCoMo benchmark fixture
//	           (docs/experiments/locomo-benchmark/src/memobase_client/config.yaml)
//	assistant — 6-topic list from example_config/profile_for_assistant/config.yaml
//	companion — 4-topic list from example_config/profile_for_companion/config.yaml
//	education — 5-topic list from example_config/profile_for_education/config.yaml
//
// Unknown values silently fall back to PersonaDefault and log a warning.
//
// Source: compete-research/memobase (Apache-2.0).
// Taxonomies are ported verbatim from the upstream YAML files; no paraphrasing.

import (
	"log/slog"
	"strings"
	"sync"
)

// Persona names for the profile-extraction taxonomy.
const (
	PersonaDefault   = "default"
	PersonaLocomo    = "locomo"
	PersonaAssistant = "assistant"
	PersonaCompanion = "companion"
	PersonaEducation = "education"
)

// profileTopicGuidelinesLocomo — verbatim port of the LoCoMo benchmark fixture.
// Source: docs/experiments/locomo-benchmark/src/memobase_client/config.yaml
const profileTopicGuidelinesLocomo = `- basic_info
  - gender
  - name
  - birth_date
  - location
- personal_narrative
  - identity_journey
  - self_acceptance
  - emotional_states
  - life_milestones
- life_circumstances
  - career
  - education
  - family_status
  - living_situation
- personal_growth
  - hobbies
  - creative_pursuits
  - mental_health
  - self_care_activities
- plans
  - career_goals
  - personal_aspirations
  - family_planning
  - life_goals
`

// profileTopicGuidelinesAssistant — verbatim port.
// Source: example_config/profile_for_assistant/config.yaml
const profileTopicGuidelinesAssistant = `- basic_info
  - name
  - age
  - gender
  - occupation
  - location
  - timezone
  - languages
  - contact_info
- schedule_prefs
  - work_hours
  - sleep_schedule
  - meeting_prefs
  - break_times
  - focus_hours
  - reminder_freq
- task_management
  - priority_rules
  - task_categories
  - deadline_buffer
  - delegation_prefs
  - recurring_tasks
  - task_format
- productivity_settings
  - focus_mode
  - notification_prefs
  - automation_rules
  - report_frequency
  - tracking_metrics
  - tool_integrations
- lifestyle_prefs
  - diet_restr
  - exercise_routine
  - shopping_lists
  - travel_prefs
  - entertainment
  - budget_tracking
- communication_style
  - tone_pref
  - response_format
  - urgency_levels
  - follow_up_freq
  - comm_channels
  - availability
`

// profileTopicGuidelinesCompanion — verbatim port.
// Source: example_config/profile_for_companion/config.yaml
const profileTopicGuidelinesCompanion = `- basic_info
  - name
  - age
  - gender
  - occupation
  - location
  - languages
  - timezone
- companion_preferences
  - companion_type
  - interaction_style
  - communication_freq
  - interest_topics
  - learning_goals
  - privacy_prefs
- interaction_history
  - convo_count
  - favorite_topics
  - active_projects
  - saved_convos
  - feedback_hist
- personalization
  - humor_pref
  - response_len
  - tech_depth
  - learn_style
`

// profileTopicGuidelinesEducation — verbatim port.
// Source: example_config/profile_for_education/config.yaml
const profileTopicGuidelinesEducation = `- basic_info
  - name
  - age
  - gender
  - grade_level
  - school_type
  - location
  - primary_language
  - learning_languages
- academic_profile
  - major_subjects
  - weak_subjects
  - strong_subjects
  - study_hours
  - academic_goals
  - exam_schedule
- learning_preferences
  - learn_style
  - study_mode
  - content_format
  - session_length
  - difficulty_pref
  - review_frequency
- progress_tracking
  - completed_courses
  - current_courses
  - course_progress
  - test_scores
  - practice_stats
  - study_streaks
- engagement_metrics
  - active_time
  - completion_rate
  - interaction_freq
  - preferred_times
  - reward_points
  - social_learning
`

// profileTopicGuidelinesByPersona maps persona name → topic list.
var profileTopicGuidelinesByPersona = map[string]string{
	PersonaDefault:   profileTopicGuidelines,
	PersonaLocomo:    profileTopicGuidelinesLocomo,
	PersonaAssistant: profileTopicGuidelinesAssistant,
	PersonaCompanion: profileTopicGuidelinesCompanion,
	PersonaEducation: profileTopicGuidelinesEducation,
}

// TaxonomyForPersona returns the topic guidelines for the given persona name.
// Unknown personas fall back silently to PersonaDefault.
func TaxonomyForPersona(name string) string {
	if g, ok := profileTopicGuidelinesByPersona[name]; ok {
		return g
	}
	return profileTopicGuidelinesByPersona[PersonaDefault]
}

// countTopics counts the number of top-level topic lines (lines starting
// with "- " and not indented) in a guidelines block.
func countTopics(guidelines string) int {
	n := 0
	for _, line := range strings.Split(guidelines, "\n") {
		if strings.HasPrefix(line, "- ") {
			n++
		}
	}
	return n
}

// personaLogOnce ensures we log the chosen persona only once per process.
var personaLogOnce sync.Once

// logPersonaOnce emits a single slog.Info line at startup (first call).
// guidelines is the already-resolved taxonomy block — passing it in keeps us
// from doing a second TaxonomyForPersona map lookup per call (the caller
// always has it on hand).
func logPersonaOnce(persona, guidelines string) {
	personaLogOnce.Do(func() {
		topicCount := countTopics(guidelines)
		slog.Info("profile_taxonomy", "persona", persona, "topic_count", topicCount)
	})
}
