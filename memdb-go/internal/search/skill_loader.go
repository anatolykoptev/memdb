package search

// skill_loader.go — D10 system-prompt loader.
//
// Backed by github.com/anatolykoptev/skillkit (v0.1.0+). The embedded
// SKILL.md file is the single source of truth; operators may override
// at runtime via MEMDB_D10_SKILL_PATH for hot-reload during prompt
// iteration without a rebuild + redeploy cycle.
//
// Resolution per call (delegated to skillkit):
//  1. If MEMDB_D10_SKILL_PATH is set + path readable + file ≤1 MiB +
//     body non-empty after frontmatter strip → mtime-cached body.
//  2. If env path becomes transiently unreadable but a prior read
//     populated the cache → cached last-known-good body (operator-
//     friendly during atomic-rename windows).
//  3. Otherwise → embedded default (skills/d10-extractor.md, baked
//     into the binary via //go:embed).
//
// loadSkillPrompt always returns a non-empty string. The package
// initialization panics if the embedded .md file has an empty body
// after frontmatter strip (build-time invariant).

import (
	_ "embed"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/skillkit"
)

//go:embed skills/d10-extractor.md
var embeddedD10SkillRaw string

// d10Skill is the singleton skillkit.Embedded for the D10 answer-extractor
// system prompt. Constructed once at package init.
var d10Skill = skillkit.NewEmbedded("d10-extractor", "MEMDB_D10_SKILL_PATH", embeddedD10SkillRaw,
	skillkit.WithObserver(observability.SkillkitObserver()),
)

// loadSkillPrompt returns the D10 system-prompt body. See package doc
// for the resolution chain. Always non-empty.
func loadSkillPrompt() string {
	return d10Skill.Body()
}

// SkillLoadDiagnostic returns a one-line description of the live D10
// prompt source. Used by cmd/server at startup so operators see in
// stdout whether the env override is active. Not a hot-path call.
func SkillLoadDiagnostic() string {
	return d10Skill.Diagnostic()
}
