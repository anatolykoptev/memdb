# skillkit

<!-- [![Go Reference](https://pkg.go.dev/badge/github.com/anatolykoptev/skillkit.svg)](https://pkg.go.dev/github.com/anatolykoptev/skillkit) -->
<!-- [![CI](https://github.com/anatolykoptev/skillkit/actions/workflows/ci.yml/badge.svg)](https://github.com/anatolykoptev/skillkit/actions/workflows/ci.yml) -->

> The reference Go toolkit for the [agentskills.io open standard](https://agentskills.io).
> Parse `SKILL.md` files, validate names, discover skills from filesystem
> or `embed.FS`, integrate with Claude Code, Cursor, GitHub Copilot,
> JetBrains Junie, Gemini CLI, OpenAI Codex, and 35+ other agentic tools
> without lock-in.

> **Status:** pre-1.0, public API may change. v0.1.0 release pending final review.
> See `docs/plans/` for the work plan.

## What it does

Two independent APIs in one zero-dependency package:

- **`skillkit.Embedded`** — single skill baked into a binary via `//go:embed`,
  with optional env-path override for hot-reload during prompt iteration.
  Designed for services that ship one skill per binary (e.g. an answer
  extractor in a search pipeline).

- **`skillkit.Catalog`** — multi-skill discovery across tiered sources
  (filesystem directories, `embed.FS`, plugin entries). Designed for agent
  CLIs that load many skills and inject summaries into a system prompt.

Both APIs share the same frontmatter primitives (`StripFrontmatter`,
`ParseFrontmatter`, `ParseMetadata`).

- **Optional `Observer` hooks** for prometheus / OpenTelemetry / slog
  observability without skillkit-side dep. Wire to whichever backend the
  consuming service already uses.

- **Optional locale routing** for multi-language skill catalogs: `cat.WithLocale("ru")` prefers `locale: ru` variants, falls back to neutral, then any match (v0.2.1+).

## Why

Until now, every Go service that loaded skills hand-rolled its own loader:
four near-identical implementations across MemDB, dozor, and vaelor; a
fifth in pre-release form on a feature branch; three different YAML
frontmatter parsers between them. Each drifted from the others over time
(MemDB shipped a Go const + `.md` file with diverging text in production).

The Agent Skills open standard published in late 2025 turned this from a
duplication problem into a portability problem: a skill written for one
agent should work in any other without modification. `skillkit` implements
the spec strictly so that skills authored against it run unchanged in
Cursor, Claude Code, Junie, and the rest of the ecosystem.

## Install

```bash
go get github.com/anatolykoptev/skillkit
```

Requires Go 1.26+. Zero non-stdlib runtime dependencies.

## Quickstart — Pattern A (Embedded)

```go
package myservice

import (
    _ "embed"

    "github.com/anatolykoptev/skillkit"
)

//go:embed skills/answer-extractor/SKILL.md
var rawSkill string

var answerExtractor = skillkit.NewEmbedded(
    "answer-extractor",
    "MYSERVICE_SKILL_PATH", // env override path for hot-reload (optional)
    rawSkill,
)

func systemPrompt() string {
    return answerExtractor.Body()
}
```

## Quickstart — Pattern B (Catalog)

```go
package mycli

import "github.com/anatolykoptev/skillkit"

func newCatalog(workspaceDir, builtinDir string) *skillkit.Catalog {
    return skillkit.NewCatalog(
        skillkit.NewDirTier("workspace", workspaceDir),
        skillkit.NewDirTier("builtin",   builtinDir),
    )
}

func loadDocReview(c *skillkit.Catalog) (string, bool) {
    body, _, ok := c.Load("doc-review")
    return body, ok
}

func systemPromptSummary(c *skillkit.Catalog) string {
    return c.BuildSummary(skillkit.SummaryXML)
}
```

## Conformance

skillkit implements the [agentskills.io](https://agentskills.io) open standard.
A skill authored for skillkit runs unchanged in any conformant agent — no
modifications to `SKILL.md` files are needed when switching tools.

Adopting tools include (partial list):

- Claude Code (Anthropic)
- Cursor
- GitHub Copilot
- JetBrains Junie
- Gemini CLI (Google)
- OpenAI Codex
- Goose (Block)
- OpenHands (All Hands AI)
- Letta

...and 35+ others. See the [agentskills.io](https://agentskills.io) ecosystem page for
the full list.

For the field-by-field spec mapping, see
[`docs/ARCHITECTURE.md` — Spec conformance](docs/ARCHITECTURE.md#spec-conformance).

## Migration

If you have a hand-rolled skill loader, replacing it with skillkit is a
mechanical change. Existing `SKILL.md` files work unchanged.

| Source pattern | skillkit equivalent | LoC reduction |
|---|---|---|
| Hand-rolled `//go:embed` + env override + mtime cache | `skillkit.NewEmbedded(name, envVar, raw)` | ~150 → 3 |
| Hand-rolled tiered FS loader (workspace + builtin) | `skillkit.NewCatalog(NewDirTier(...), NewDirTier(...))` | ~200 → 5 |
| Hand-rolled plugin-aware loader | `Catalog` + `NewPluginTier(name, entries)` | ~250 → 8 |

See [`doc/skill.md` — Migration notes](doc/skill.md#11-migration-notes-from-hand-rolled-loaders)
for annotated before/after examples. Per-repo projected deltas are in
[`docs/plans/2026-04-30-init.md`](docs/plans/2026-04-30-init.md).

## License

MIT — see [LICENSE](LICENSE).

## Related

- [agentskills.io](https://agentskills.io) — the open standard this package implements
- [agentskills/agentskills](https://github.com/agentskills/agentskills) — spec repo + `skills-ref` CLI validator
- [Anthropic Agent Skills docs](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview) — Claude Code extensions
