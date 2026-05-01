// locale.go — locale-aware tier lookup helpers (v0.2.1).
package skillkit

import (
	"log/slog"
	"strings"
)

// bodyByInfoResolver is the optional interface resolvers implement to support
// locale-aware body lookup. Built-in DirTier, EmbedFSTier, and PluginTier all
// implement it. Custom external resolvers can opt in by adding a ResolveByInfo
// method; without it, locale routing is best-effort (returns first name match
// per Resolver.Find, which may be a different locale variant).
type bodyByInfoResolver interface {
	Resolver
	// ResolveByInfo returns the skill body for the given SkillInfo.
	// ok=false when the body cannot be read (e.g. file deleted between Walk
	// and this call). Callers treat ok=false as a transient miss and fall back
	// to Resolver.Find(name).
	ResolveByInfo(SkillInfo) (body string, ok bool)
}

// skillMatchesName reports whether a SkillInfo is a candidate for the given
// lookup name. We match on:
//  1. Metadata.Name (frontmatter name field) — primary: locale variants live
//     in differently-named subdirs (e.g. "d10-extractor-ru") but declare
//     the same frontmatter name ("d10-extractor").
//  2. SkillInfo.Name (subdir / SkillName) — fallback: when frontmatter name
//     is absent or identical to the subdir name (common for non-locale skills).
//
// Matching on Metadata.Name first makes locale variants addressable by their
// canonical name even when the subdir name includes a locale suffix.
func skillMatchesName(info SkillInfo, name string) bool {
	if info.Metadata.Name != "" && info.Metadata.Name == name {
		return true
	}
	return info.Name == name
}

// findInTierByLocale walks the tier collecting all skills whose name (by
// frontmatter or subdir name) matches the lookup name, then returns the
// best locale match per the resolution chain:
//
//  1. Exact locale match (case-insensitive)
//  2. Locale-neutral (Metadata.Locale == "")
//  3. Any name match (first found — deterministic given Walk iteration order)
//
// When locale is "" the function short-circuits to plain Resolver.Find.
//
// Body retrieval prefers the bodyByInfoResolver optional interface; falls back
// to Resolver.Find when the resolver does not implement it (emits slog.Debug).
func findInTierByLocale(t Tier, name, locale string) (SkillInfo, string, bool) {
	if locale == "" {
		return t.Resolver.Find(name)
	}

	var (
		exactLocale   *SkillInfo
		neutralLocale *SkillInfo
		anyMatch      *SkillInfo
	)

	t.Resolver.Walk(func(info SkillInfo) {
		if !skillMatchesName(info, name) {
			return
		}
		if strings.EqualFold(info.Locale, locale) {
			if exactLocale == nil {
				cp := info
				exactLocale = &cp
			}
			return
		}
		if info.Locale == "" {
			if neutralLocale == nil {
				cp := info
				neutralLocale = &cp
			}
			return
		}
		if anyMatch == nil {
			cp := info
			anyMatch = &cp
		}
	})

	var picked *SkillInfo
	switch {
	case exactLocale != nil:
		picked = exactLocale
	case neutralLocale != nil:
		picked = neutralLocale
	case anyMatch != nil:
		picked = anyMatch
	default:
		return SkillInfo{}, "", false
	}

	// Retrieve body via optional bodyByInfoResolver, falling back to Find.
	if r, ok := t.Resolver.(bodyByInfoResolver); ok {
		if body, found := r.ResolveByInfo(*picked); found {
			return *picked, body, true
		}
		// ResolveByInfo missed (e.g. file deleted between Walk and now);
		// fall through to Find for best-effort recovery.
	} else {
		slog.Debug("skillkit: resolver does not implement bodyByInfoResolver; locale routing is best-effort",
			"tier", t.Name)
	}

	// Fallback: plain Find — may return a different locale variant when the
	// resolver does not implement bodyByInfoResolver. Callers that require
	// strict locale routing must use a built-in tier or implement the interface.
	si, body, found := t.Resolver.Find(name)
	return si, body, found
}
