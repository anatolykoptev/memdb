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
	tracer   *Tracer
	locale   string // BCP-47 language tag; "" = no locale filter
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

// WithTracer returns the catalog wrapped with the given tracer.
// Subsequent LoadCtx calls open a span via Tracer.StartCatalogLoad.
// Nil tracer is a no-op (returns receiver unchanged).
//
// Thread-safety: same configure-once contract as WithObserver.
func (c *Catalog) WithTracer(tr *Tracer) *Catalog {
	if tr == nil {
		return c
	}
	c.tracer = tr
	return c
}

// WithLocale returns the catalog wrapped with a locale preference.
// Subsequent Load / LoadCtx calls prefer skills whose Metadata.Locale
// matches the supplied locale string; misses fall back to Metadata.Locale=""
// (locale-neutral) or to any other matching skill name.
//
// Locale matching is exact-string equality (case-insensitive). The most
// common values are BCP-47 language tags ("en", "ru", "zh", "pt-BR")
// but skillkit does not validate the format — caller decides the
// vocabulary. Hyphen vs underscore normalization (e.g. "pt-BR" vs
// "pt_BR") is also the caller's responsibility.
//
// Empty locale string is a no-op (returns the receiver unchanged); it
// does NOT reset a previously configured locale. Per the "configure
// once at startup" contract, callers wishing to disable filtering
// after a prior WithLocale call should construct a new Catalog instead.
//
// Mutates and returns the receiver for chained construction:
//
//	cat := skillkit.NewCatalog(...).WithObserver(obs).WithLocale("ru")
//
// Thread-safety: configure once at startup before any concurrent
// Load/LoadCtx callers — same contract as WithObserver.
//
// Resolution order (per Load(name) call):
//  1. Walk tiers in priority order (existing behavior).
//  2. Within each tier, prefer skill whose Metadata.Locale equals the
//     configured locale (case-insensitive).
//  3. If no locale match, fall back to skill with empty Metadata.Locale.
//  4. If no neutral skill, fall back to any name match (first found).
//  5. If no name match in any tier, return ok=false (existing behavior).
//
// SkillInfo.Source label is unaffected by locale routing.
func (c *Catalog) WithLocale(locale string) *Catalog {
	if locale == "" {
		return c
	}
	c.locale = locale
	return c
}

// Locale returns the catalog's currently configured locale.
// Returns "" when WithLocale was never called or was called with "".
func (c *Catalog) Locale() string {
	return c.locale
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
		var si SkillInfo
		var b string
		var found bool
		if c.locale != "" {
			si, b, found = findInTierByLocale(t, name, c.locale)
		} else {
			si, b, found = t.Resolver.Find(name)
		}
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
// When a locale is configured (via WithLocale), locale routing applies
// to non-ContextResolver tiers; ContextResolver tiers still use FindCtx
// (locale filtering is best-effort for network-backed sources).
//
// When a Tracer is configured via WithTracer, opens a span via
// Tracer.StartCatalogLoad for the duration of the call. The outcome
// label ("hit" or "miss") is attached when the span ends.
func (c *Catalog) LoadCtx(ctx context.Context, name string) (body string, info SkillInfo, ok bool, err error) {
	var outcome string
	if c.tracer != nil && c.tracer.StartCatalogLoad != nil {
		var end func(string)
		ctx, end = c.tracer.StartCatalogLoad(ctx, name)
		defer func() { end(outcome) }()
	}

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
			outcome = "hit"
			c.fireCatalogLoad(name, "hit")
			return b, si, true, nil
		}
		var si SkillInfo
		var b string
		var found bool
		if c.locale != "" {
			si, b, found = findInTierByLocale(t, name, c.locale)
		} else {
			si, b, found = t.Resolver.Find(name)
		}
		if !found {
			continue
		}
		si = c.tagSource(t, si)
		outcome = "hit"
		c.fireCatalogLoad(name, "hit")
		return b, si, true, nil
	}
	outcome = "miss"
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
