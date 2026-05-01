package scheduler

// skill_loader.go — system-prompt catalog for the scheduler package.
//
// Eight skill bodies live in skills/<name>/SKILL.md, embedded into the
// binary via //go:embed. The Catalog provides byte-identical access by
// skill name. Operators can override individual skills at runtime by
// dropping a file at /etc/memdb/scheduler-skills/<name>/SKILL.md and
// configuring a workspace-tier DirTier (deferred — env-override path
// per-skill is the v0.2 use case once at least one operator asks).
//
// This is the first Pattern B (Catalog + EmbedFSTier) consumer in
// MemDB; D10 and atomic use Pattern A (Embedded). The Catalog gives
// us multi-skill lookup without one Embedded instance per prompt.

import (
	"embed"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/skillkit"
)

//go:embed skills/*/SKILL.md
var schedulerSkillsFS embed.FS

// schedulerCatalog is the singleton catalog of all 8 scheduler system
// prompts, parsed once at package init from the embedded FS.
var schedulerCatalog = skillkit.NewCatalog(
	skillkit.NewEmbedFSTier("scheduler-builtin", schedulerSkillsFS, "skills"),
).WithObserver(observability.SkillkitObserver())

// SchedulerSkillNames is the canonical list of scheduler skill names,
// in the order they were declared in the legacy prompts.go const block.
// Used by SchedulerSkillDiagnostic for startup logging and as the
// authoritative set of names schedulerPrompt accepts.
var SchedulerSkillNames = []string{
	"memory-consolidator",
	"feedback-memory-curator",
	"preference-extractor",
	"session-compactor",
	"episodic-tier-archivist",
	"semantic-tier-abstractor",
	"relation-detector",
	"wm-enhancement-extractor",
}

// schedulerPrompt returns the body of a scheduler skill by name. Panics
// if the name is unknown — these are compile-time-defined skill names
// shipped with the binary, not user input. A miss means the embed.FS
// did not include the skill or the skill name was misspelled by a
// caller; both are programmer errors that must surface immediately
// rather than silently degrade prompt content.
func schedulerPrompt(name string) string {
	body, _, ok := schedulerCatalog.Load(name)
	if !ok {
		panic(fmt.Sprintf("scheduler.schedulerPrompt: unknown skill %q (known: %v)", name, SchedulerSkillNames))
	}
	return body
}

// SchedulerSkillDiagnostic returns a one-line description of the
// scheduler skill catalog for the startup log. Lists how many skills
// loaded; primarily a sanity check that //go:embed picked up all 8
// SKILL.md files.
func SchedulerSkillDiagnostic() string {
	loaded := schedulerCatalog.List()
	return fmt.Sprintf("scheduler-builtin tier: %d skills loaded (%v)", len(loaded), namesOf(loaded))
}

func namesOf(infos []skillkit.SkillInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}
