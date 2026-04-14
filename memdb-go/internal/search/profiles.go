package search

import "fmt"

// Profile-specific defaults. Named to give context to the numeric values.
const (
	profileInjectTopK       = 5     // inject profile: tight retrieval window
	profileInjectRelativity = 0.93  // inject profile: high relevance threshold
	profileInjectDecayAlpha = 0.01  // inject profile: slow decay (0.01/cycle)
	profileDeepTopK         = 10    // deep profile: wider retrieval window
	profileDeepRelativity   = 0.85  // deep profile: moderate relevance threshold
	profileDeepNumStages    = 2     // deep profile: 2 iterative expansion stages (was 3; saves one LLM+vector round-trip)
	profileDeepDecayAlpha   = 0.002 // deep profile: very slow decay (0.002/cycle)
)

// ProfileName is a named search configuration preset.
type ProfileName = string

// ProfileOverrides holds optional overrides for SearchParams.
// Nil fields keep the base default; non-nil fields override.
type ProfileOverrides struct {
	TopK      *int
	SkillTopK *int
	PrefTopK  *int
	ToolTopK  *int
	NumStages *int
	LLMRerank *bool

	MMRLambda  *float64
	DecayAlpha *float64
	Relativity *float64

	IncludeSkill *bool
	IncludePref  *bool
	IncludeTool  *bool

	Dedup *string
}

// profiles maps profile names to their overrides.
var profiles = map[ProfileName]ProfileOverrides{
	"inject": {
		TopK:         intPtr(profileInjectTopK),
		SkillTopK:    intPtr(0),
		PrefTopK:     intPtr(0),
		ToolTopK:     intPtr(0),
		Dedup:        stringPtr(DedupModeMMR),
		Relativity:   float64Ptr(profileInjectRelativity),
		DecayAlpha:   float64Ptr(profileInjectDecayAlpha),
		IncludeSkill: boolPtr(false),
		IncludePref:  boolPtr(false),
		IncludeTool:  boolPtr(false),
	},
	"default": {},
	"deep": {
		TopK:       intPtr(profileDeepTopK),
		Dedup:      stringPtr(DedupModeMMR),
		Relativity: float64Ptr(profileDeepRelativity),
		NumStages:  intPtr(profileDeepNumStages),
		LLMRerank:  boolPtr(true),
		DecayAlpha: float64Ptr(profileDeepDecayAlpha),
	},
}

// LookupProfile returns the overrides for a named profile.
// Empty name returns a zero ProfileOverrides (no-op). Unknown name returns an error.
func LookupProfile(name string) (ProfileOverrides, error) {
	if name == "" {
		return ProfileOverrides{}, nil
	}
	p, ok := profiles[name]
	if !ok {
		return ProfileOverrides{}, fmt.Errorf("unknown search profile: %q", name)
	}
	return p, nil
}

// ApplyProfile applies non-nil profile overrides to a copy of base params.
func ApplyProfile(base SearchParams, prof ProfileOverrides) SearchParams {
	if prof.TopK != nil {
		base.TopK = *prof.TopK
	}
	if prof.SkillTopK != nil {
		base.SkillTopK = *prof.SkillTopK
	}
	if prof.PrefTopK != nil {
		base.PrefTopK = *prof.PrefTopK
	}
	if prof.ToolTopK != nil {
		base.ToolTopK = *prof.ToolTopK
	}
	if prof.NumStages != nil {
		base.NumStages = *prof.NumStages
	}
	if prof.LLMRerank != nil {
		base.LLMRerank = *prof.LLMRerank
	}
	if prof.MMRLambda != nil {
		base.MMRLambda = *prof.MMRLambda
	}
	if prof.DecayAlpha != nil {
		base.DecayAlpha = *prof.DecayAlpha
	}
	if prof.Relativity != nil {
		base.Relativity = *prof.Relativity
	}
	if prof.IncludeSkill != nil {
		base.IncludeSkill = *prof.IncludeSkill
	}
	if prof.IncludePref != nil {
		base.IncludePref = *prof.IncludePref
	}
	if prof.IncludeTool != nil {
		base.IncludeTool = *prof.IncludeTool
	}
	if prof.Dedup != nil {
		base.Dedup = *prof.Dedup
	}
	return base
}

func intPtr(v int) *int          { return &v }
func float64Ptr(v float64) *float64 { return &v }
func boolPtr(v bool) *bool       { return &v }
func stringPtr(v string) *string { return &v }
