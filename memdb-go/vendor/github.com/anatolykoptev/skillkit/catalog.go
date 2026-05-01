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
	tiers    []Tier
	observer *Observer
}

// NewCatalog creates a Catalog from the given tiers, searched in order.
func NewCatalog(tiers ...Tier) *Catalog {
	return &Catalog{tiers: tiers}
}

// WithObserver returns a new Catalog wrapping the receiver with the
// given observer. Method receiver pattern (not constructor option)
// because tiers are passed at construction; observer attaches after.
//
// On first attach, CatalogSize is fired once per tier with the count
// of skills that tier exposes via Walk. Tiers that expose zero skills
// (e.g. empty DirTier) still fire with count=0.
//
// Nil observer is a no-op; method returns the receiver unchanged.
//
// Thread-safety: configure once at startup before any concurrent
// Load/LoadCtx callers — this method is not safe to call after the
// catalog is in use. The observer field is read on every Load without
// synchronization (intentional — zero hot-path cost). Future v0.3.0
// may wrap observer in atomic.Pointer if a runtime swap use case
// emerges.
func (c *Catalog) WithObserver(obs *Observer) *Catalog {
	if obs == nil {
		return c
	}
	c.observer = obs
	if obs.CatalogSize != nil {
		for _, t := range c.tiers {
			count := 0
			t.Resolver.Walk(func(_ SkillInfo) { count++ })
			obs.CatalogSize(t.Name, count)
		}
	}
	return c
}

// fireCatalogLoad invokes CatalogLoad if the observer is set.
func (c *Catalog) fireCatalogLoad(name, outcome string) {
	if c.observer != nil && c.observer.CatalogLoad != nil {
		c.observer.CatalogLoad(name, outcome)
	}
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
		c.fireCatalogLoad(name, "hit")
		return b, si, true
	}
	c.fireCatalogLoad(name, "miss")
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
			c.fireCatalogLoad(name, "hit")
			return b, si, true, nil
		}
		si, b, found := t.Resolver.Find(name)
		if !found {
			continue
		}
		si = c.tagSource(t, si)
		c.fireCatalogLoad(name, "hit")
		return b, si, true, nil
	}
	c.fireCatalogLoad(name, "miss")
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
