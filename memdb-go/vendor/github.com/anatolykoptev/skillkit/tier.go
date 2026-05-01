// Package skillkit provides the core Tier infrastructure for skill discovery.
// This file defines shared types, interfaces, and the parseSkill helper.
package skillkit

import "context"

// SkillInfo describes a discovered skill.
type SkillInfo struct {
	Name   string
	Path   string // resolver-defined; "" for plugin sources
	Dir    string // skill directory (SKILL.md's parent); populated by all tiers
	Source string // tier name; "<tier>:<plugin>" for plugin tier
	Metadata
}

// Resolver is the source-agnostic lookup interface.
type Resolver interface {
	Find(name string) (info SkillInfo, body string, ok bool)
	Walk(yield func(SkillInfo))
}

// ContextResolver extends Resolver for network-backed sources.
type ContextResolver interface {
	Resolver
	FindCtx(ctx context.Context, name string) (SkillInfo, string, bool, error)
}

// Tier wraps a Resolver with a label used in summaries and SkillInfo.Source.
// First-tier match wins in Catalog.
//
// Source-tagging convention:
//   - DirTier and EmbedFSTier leave SkillInfo.Source empty; the
//     Catalog wrapper tags it with Tier.Name.
//   - PluginTier populates SkillInfo.Source as "<tier>:<plugin>" itself
//     because the plugin name lives on PluginEntry, not on the Tier.
//     Catalog detects the colon and skips the override.
//
// New Resolver implementations should leave SkillInfo.Source empty and
// let Catalog tag, unless they have per-entry namespacing like PluginTier.
type Tier struct {
	Name     string
	Resolver Resolver
}

// parseSkill parses a raw skill file (frontmatter + body). Returns the
// stripped body and parsed metadata. ok=false when body is empty after
// frontmatter strip — the caller should treat the skill as not-found
// and log a slog.Warn at its own discretion.
func parseSkill(raw string) (body string, meta Metadata, ok bool) {
	body = StripFrontmatter(raw)
	if body == "" {
		return "", Metadata{}, false
	}
	return body, ParseMetadata(raw), true
}
