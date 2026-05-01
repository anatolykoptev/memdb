package skillkit

import (
	"context"
	"log/slog"
	"sort"
	"strings"
)

// Catalog aggregates multiple Tiers into a unified skill registry.
// Tiers are searched in order; the first match wins. Duplicate skill
// names across tiers are deduplicated so the higher-priority tier wins.
type Catalog struct {
	tiers []Tier
}

// NewCatalog creates a Catalog from the given tiers, searched in order.
func NewCatalog(tiers ...Tier) *Catalog {
	return &Catalog{tiers: tiers}
}

// List returns all unique skills across all tiers, sorted by Name.
// When the same name appears in multiple tiers the first tier wins.
func (c *Catalog) List() []SkillInfo {
	seen := make(map[string]bool)
	var result []SkillInfo
	for _, t := range c.tiers {
		t.Resolver.Walk(func(info SkillInfo) {
			if seen[info.Name] {
				return
			}
			seen[info.Name] = true
			info = c.tagSource(t, info)
			result = append(result, info)
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Load searches tiers in order and returns the first match for name.
func (c *Catalog) Load(name string) (body string, info SkillInfo, ok bool) {
	for _, t := range c.tiers {
		si, b, found := t.Resolver.Find(name)
		if !found {
			continue
		}
		si = c.tagSource(t, si)
		return b, si, true
	}
	return "", SkillInfo{}, false
}

// LoadCtx searches tiers in order using context-aware lookup when the
// tier implements ContextResolver. Falls back to Find otherwise.
func (c *Catalog) LoadCtx(ctx context.Context, name string) (body string, info SkillInfo, ok bool, err error) {
	for _, t := range c.tiers {
		if cr, isCtx := t.Resolver.(ContextResolver); isCtx {
			si, b, found, findErr := cr.FindCtx(ctx, name)
			if findErr != nil {
				return "", SkillInfo{}, false, findErr
			}
			if !found {
				continue
			}
			si = c.tagSource(t, si)
			return b, si, true, nil
		}
		si, b, found := t.Resolver.Find(name)
		if !found {
			continue
		}
		si = c.tagSource(t, si)
		return b, si, true, nil
	}
	return "", SkillInfo{}, false, nil
}

// LoadMany concatenates skill bodies for system-prompt injection.
// Format per skill: "### Skill: <name>\n\n<body>". Skills joined with
// "\n\n---\n\n". Missing skills logged at slog.Debug and skipped.
// Returns "" when no skills resolve.
//
// LoadMany is a context-less convenience wrapper around LoadManyCtx
// using context.Background(); they share the format contract.
func (c *Catalog) LoadMany(names []string) string {
	return c.LoadManyCtx(context.Background(), names)
}

// LoadManyCtx is the context-aware variant of LoadMany.
func (c *Catalog) LoadManyCtx(ctx context.Context, names []string) string {
	if len(names) == 0 {
		return ""
	}
	var parts []string
	for _, name := range names {
		body, _, ok, err := c.LoadCtx(ctx, name)
		if err != nil {
			slog.Debug("skillkit.Catalog.LoadManyCtx: skill load error, skipping",
				"name", name, "err", err)
			continue
		}
		if !ok {
			slog.Debug("skillkit.Catalog.LoadManyCtx: skill not found, skipping", "name", name)
			continue
		}
		parts = append(parts, "### Skill: "+name+"\n\n"+body)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// isPluginSource reports whether info.Source uses the PluginTier
// "<tier>:<plugin>" namespacing format. Catalog.tagSource skips the
// override when this returns true; summary.go uses it to group
// XML output under <skill-group plugin="...">.
//
// The check is intentionally lenient (presence of a colon) — it matches
// any future Resolver that adopts the same namespacing convention.
// Stricter validation would couple this helper to the PluginTier
// implementation and break the open-extension goal of the Resolver
// interface.
func isPluginSource(source string) bool {
	return strings.Contains(source, ":")
}

// tagSource sets info.Source = tier.Name for non-plugin resolvers.
// PluginTier already populates Source as "<tier>:<plugin>" (detected
// by the presence of ":"), so those are returned unchanged.
func (c *Catalog) tagSource(t Tier, info SkillInfo) SkillInfo {
	if isPluginSource(info.Source) {
		// PluginTier already set source.
		return info
	}
	info.Source = t.Name
	return info
}
