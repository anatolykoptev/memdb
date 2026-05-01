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
// Spec conformance: skillkit implements the agentskills.io standard
// adopted by Claude Code, Cursor, GitHub Copilot, JetBrains Junie,
// Gemini CLI, OpenAI Codex, and 35+ other agentic tools. A skill
// authored for skillkit runs unchanged in any conformant agent.
//
// See doc/skill.md for usage examples.
package skillkit
