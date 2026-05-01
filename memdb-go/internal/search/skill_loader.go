package search

// skill_loader.go — D10 system-prompt loader with locale routing.
//
// Backed by github.com/anatolykoptev/skillkit (v0.2.0+). Three embedded
// SKILL.md files (en, ru, zh) are the single source of truth; operators
// may override each variant independently at runtime via its own env var
// for hot-reload during prompt iteration without a rebuild + redeploy cycle.
//
// Locale resolution per call (delegated to skillkit per instance):
//  1. If MEMDB_D10_SKILL_PATH_<LANG> is set + path readable + file ≤1 MiB +
//     body non-empty after frontmatter strip → mtime-cached body.
//  2. If env path becomes transiently unreadable but a prior read
//     populated the cache → cached last-known-good body (operator-
//     friendly during atomic-rename windows).
//  3. Otherwise → embedded default baked into the binary via //go:embed.
//
// Locale routing: callers pass the request locale ("en", "ru", "zh").
// Unknown locales fall back to "en" (current set is the intersection of
// lang.Detect outputs and shipped .md files). Auto-detect from query text
// is performed in buildAnswerEnhanceSystemPrompt when cfg.Locale is empty.
//
// Operator env knobs:
//
//	MEMDB_D10_SKILL_PATH     — override for "en" variant (existing knob, unchanged)
//	MEMDB_D10_SKILL_PATH_RU  — override for "ru" variant (new)
//	MEMDB_D10_SKILL_PATH_ZH  — override for "zh" variant (new)
//
// loadSkillPrompt/SkillLoadDiagnostic are preserved as backward-compat
// shims for existing callers — they delegate to the "en" instance.

import (
	"context"
	_ "embed"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/skillkit"
)

//go:embed skills/d10-extractor.md
var embeddedD10SkillEN string

//go:embed skills/d10-extractor.ru.md
var embeddedD10SkillRU string

//go:embed skills/d10-extractor.zh.md
var embeddedD10SkillZH string

// d10SkillsByLocale routes D10 prompt selection by locale. Each entry
// is a separate skillkit.Embedded instance with its own //go:embed
// raw, env-override path, mtime cache, and Observer wiring. Per-locale
// env override allows operators to hot-reload one variant independently
// of the others.
//
// Skill name attribute per instance is suffixed with the locale for
// separate Prometheus counters (d10-extractor, d10-extractor-ru,
// d10-extractor-zh). The "name" label feeds into observability/metrics.
var d10SkillsByLocale = map[string]*skillkit.Embedded{
	"en": skillkit.NewEmbedded(
		"d10-extractor",
		"MEMDB_D10_SKILL_PATH",
		embeddedD10SkillEN,
		skillkit.WithObserver(observability.SkillkitObserver()),
		skillkit.WithTracer(observability.SkillkitTracer()),
	),
	"ru": skillkit.NewEmbedded(
		"d10-extractor-ru",
		"MEMDB_D10_SKILL_PATH_RU",
		embeddedD10SkillRU,
		skillkit.WithObserver(observability.SkillkitObserver()),
		skillkit.WithTracer(observability.SkillkitTracer()),
	),
	"zh": skillkit.NewEmbedded(
		"d10-extractor-zh",
		"MEMDB_D10_SKILL_PATH_ZH",
		embeddedD10SkillZH,
		skillkit.WithObserver(observability.SkillkitObserver()),
		skillkit.WithTracer(observability.SkillkitTracer()),
	),
}

// loadSkillPrompt returns the EN D10 system-prompt body. Existing
// callers that don't yet thread locale stay locale-unaware — they
// continue to behave identically to pre-locale-routing semantics.
//
// Backward-compat shim. New code paths should prefer
// loadSkillPromptForLocale to enable .ru / .zh selection.
func loadSkillPrompt() string {
	return loadSkillPromptForLocale(context.Background(), "en")
}

// loadSkillPromptForLocale returns the D10 system-prompt body for the
// given locale. Unknown locales (anything outside "en"/"ru"/"zh")
// fall back to "en". Each locale has its own env-override knob:
//
//	MEMDB_D10_SKILL_PATH     — operator override for "en" (existing)
//	MEMDB_D10_SKILL_PATH_RU  — operator override for "ru" (new)
//	MEMDB_D10_SKILL_PATH_ZH  — operator override for "zh" (new)
//
// When a Tracer is configured (via WithTracer), opens a span around
// the BodyCtx call so operators can see skill load latency in Jaeger.
func loadSkillPromptForLocale(ctx context.Context, locale string) string {
	skill, ok := d10SkillsByLocale[locale]
	if !ok {
		skill = d10SkillsByLocale["en"]
	}
	return skill.BodyCtx(ctx)
}

// SkillLoadDiagnostic returns a one-line description of the live D10
// prompt source for the EN variant. Used by cmd/server at startup so
// operators see in stdout whether the env override is active. Reports
// the EN variant (the default) for backward compat. Use
// SkillLoadDiagnosticAll for per-locale visibility.
func SkillLoadDiagnostic() string {
	return d10SkillsByLocale["en"].Diagnostic()
}

// SkillLoadDiagnosticAll returns a semicolon-separated list of per-locale
// diagnostic strings. Used by cmd/server startup log for full visibility.
func SkillLoadDiagnosticAll() string {
	return "en: " + d10SkillsByLocale["en"].Diagnostic() +
		"; ru: " + d10SkillsByLocale["ru"].Diagnostic() +
		"; zh: " + d10SkillsByLocale["zh"].Diagnostic()
}
