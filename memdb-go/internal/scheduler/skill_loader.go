package scheduler

// skill_loader.go — system-prompt catalog for the scheduler package.
//
// Eight skill bodies live in skills/<name>/SKILL.md, embedded into the
// binary via //go:embed. The Catalog provides byte-identical access by
// skill name.
//
// Operators can activate hot-reload by setting MEMDB_SCHEDULER_SKILLS_DIR
// to an absolute path (e.g. /host-skills/scheduler). A DirTier is then
// prepended to the catalog with higher priority; the first tier that finds
// a <dir>/<skill-name>/SKILL.md wins. WithMtimeCache() means edits are
// picked up on the next request without a container restart. Skills not
// present in the workspace dir transparently fall through to the embedded
// builtin tier.
//
// This mirrors MEMDB_D10_SKILL_PATH and MEMDB_ATOMIC_SKILL_PATH (Pattern A,
// single Embedded). Scheduler uses Pattern B (Catalog + EmbedFSTier) because
// it manages 8 skills; the workspace tier extends that to conditional dual-tier.

import (
	"context"
	"embed"
	"fmt"
	"os"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/skillkit"
)

//go:embed skills/*/SKILL.md
var schedulerSkillsFS embed.FS

// schedulerSkillsDirEnv names the env var that opts an operator into a
// workspace-tier override directory for scheduler skills. Unset (default)
// = embedded-only; the binary ships baked-in scheduler prompts and
// operators rebuild the image to iterate.
//
// Set the env to an absolute path (e.g. /host-skills/scheduler) to
// activate hot-reload: drop a file at <dir>/<skill-name>/SKILL.md and
// the next request reads it through the mtime cache. The workspace tier
// has higher priority than the embedded default — first hit wins per
// skillkit Catalog semantics. Skill names not present in the workspace
// dir transparently fall back to the embedded version.
//
// Mirrors MEMDB_D10_SKILL_PATH and MEMDB_ATOMIC_SKILL_PATH UX (env-path
// override) but for the multi-skill Catalog pattern instead of single
// Embedded.
const schedulerSkillsDirEnv = "MEMDB_SCHEDULER_SKILLS_DIR"

// schedulerCatalog is the singleton catalog. Tier composition depends
// on the runtime env: when MEMDB_SCHEDULER_SKILLS_DIR is set, a
// workspace DirTier is prepended (higher priority); otherwise the
// catalog is single-tier (embedded only).
var schedulerCatalog = buildSchedulerCatalog()

func buildSchedulerCatalog() *skillkit.Catalog {
	tiers := []skillkit.Tier{}
	if dir := strings.TrimSpace(os.Getenv(schedulerSkillsDirEnv)); dir != "" {
		tiers = append(tiers, skillkit.NewDirTier(
			"scheduler-workspace",
			dir,
			skillkit.WithMtimeCache(),
		))
	}
	tiers = append(tiers, skillkit.NewEmbedFSTier(
		"scheduler-builtin",
		schedulerSkillsFS,
		"skills",
	))
	return skillkit.NewCatalog(tiers...).
		WithObserver(observability.SkillkitObserver()).
		WithTracer(observability.SkillkitTracer())
}

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
//
// ctx is threaded to LoadCtx so the Tracer (if configured) can open a
// span around the catalog lookup. For embedded-only catalogs the lookup
// is synchronous and the span adds negligible overhead.
func schedulerPrompt(ctx context.Context, name string) string {
	body, _, ok, err := schedulerCatalog.LoadCtx(ctx, name)
	if err != nil || !ok {
		panic(fmt.Sprintf("scheduler.schedulerPrompt: unknown skill %q (known: %v)", name, SchedulerSkillNames))
	}
	return body
}

// SchedulerSkillDiagnostic returns a one-line description of the
// scheduler skill catalog for the startup log. Lists how many skills
// loaded; primarily a sanity check that //go:embed picked up all 8
// SKILL.md files. When MEMDB_SCHEDULER_SKILLS_DIR is set the message
// includes the workspace path to surface which override directory is
// active.
func SchedulerSkillDiagnostic() string {
	loaded := schedulerCatalog.List()
	workspaceDir := strings.TrimSpace(os.Getenv(schedulerSkillsDirEnv))
	if workspaceDir == "" {
		return fmt.Sprintf("scheduler-builtin tier: %d skills loaded (%v)",
			len(loaded), namesOf(loaded))
	}
	return fmt.Sprintf("scheduler tiers: workspace=%s + builtin: %d effective skills (%v)",
		workspaceDir, len(loaded), namesOf(loaded))
}

func namesOf(infos []skillkit.SkillInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}
