// Package skillkit is the reference Go implementation of the
// agentskills.io open standard for AI agent skill files (SKILL.md +
// YAML/JSON frontmatter).
//
// Two independent APIs are offered:
//
//   - Embedded — single skill baked into a binary via //go:embed with
//     optional env-path override for hot-reload during prompt
//     iteration. See NewEmbedded.
//
//   - Catalog — multi-skill discovery across tiered sources
//     (filesystem, embed.FS, plugin entries). See NewCatalog,
//     NewDirTier, NewEmbedFSTier, NewPluginTier.
//
// Both APIs share the StripFrontmatter / ParseFrontmatter / Metadata
// primitives. Frontmatter formats: YAML (Markdown standard) and JSON.
//
// Observability: both Embedded and Catalog accept an optional
// Observer to hook runtime events into a caller-supplied metrics
// backend (prometheus, OpenTelemetry, slog, ...). All hooks are
// nil-safe; skillkit core stays stdlib-only. See WithObserver.
//
// Distributed tracing: an optional Tracer hooks span creation into
// the loader hot paths. Embedded.BodyCtx opens a span per body
// resolution; Catalog.WithTracer().LoadCtx opens a span per catalog
// lookup. Both are nil-safe and vendor-neutral; caller adapts to
// OpenTelemetry or any other tracer with a 5-line wrapper. See
// WithTracer (v0.2.2+).
//
// Locale routing: Catalog supports opt-in locale preference via
// WithLocale, enabling multi-language skill catalogs without changing
// existing skill names. See Catalog.WithLocale (v0.2.1+).
//
// Spec conformance: skillkit implements the agentskills.io standard
// adopted by Claude Code, Cursor, GitHub Copilot, JetBrains Junie,
// Gemini CLI, OpenAI Codex, and 35+ other agentic tools. A skill
// authored for skillkit runs unchanged in any conformant agent.
//
// See doc/skill.md for usage examples.
package skillkit
