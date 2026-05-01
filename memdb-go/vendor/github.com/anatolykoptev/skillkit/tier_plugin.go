// tier_plugin.go — PluginTier: pre-resolved skill tier from a plugin discovery layer.
package skillkit

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// PluginEntry is a pre-resolved skill entry from a plugin discovery layer.
//
// Body and Path semantics:
//   - Body wins over Path (no fs read needed for the body).
//   - Path is used to populate SkillInfo.Dir = filepath.Dir(Path) so
//     callers can resolve sibling files (references/, scripts/, assets/).
//   - When ONLY Body is set (no Path), SkillInfo.Dir is empty. Callers
//     using Dir to locate sibling files MUST check it before use; a
//     pure-Body entry has no on-disk sibling layout.
type PluginEntry struct {
	PluginName string
	SkillName  string
	Path       string // optional: path to skill file
	Body       string // optional: pre-loaded body (preferred over Path)
}

// pluginRecord is an entry pre-parsed at construction time.
type pluginRecord struct {
	info SkillInfo
	body string
}

type pluginTier struct {
	name   string
	bare   map[string]pluginRecord // bare name → first-seen entry
	full   map[string]pluginRecord // "plugin:skill" → entry
	byInfo map[string]pluginRecord // "tierName:pluginName:skillName" → entry (for ResolveByInfo)
}

// NewPluginTier creates a tier from a slice of pre-resolved entries.
// All entries are parsed at construction time (Body or Path → parseSkill).
// Both bare ("foo") and namespaced ("plugin:foo") lookups are supported.
// When multiple entries share the same (PluginName, SkillName), the first
// wins and a slog.Warn is emitted listing duplicates.
func NewPluginTier(name string, entries []PluginEntry) Tier {
	p := &pluginTier{
		name:   name,
		bare:   make(map[string]pluginRecord, len(entries)),
		full:   make(map[string]pluginRecord, len(entries)),
		byInfo: make(map[string]pluginRecord, len(entries)),
	}

	type dupKey struct{ plugin, skill string }
	seen := make(map[dupKey]bool)

	for _, e := range entries {
		fullKey := e.PluginName + ":" + e.SkillName
		dk := dupKey{e.PluginName, e.SkillName}
		if seen[dk] {
			slog.Warn("skillkit.PluginTier: duplicate (PluginName, SkillName) entry, skipping",
				"tier", name, "plugin", e.PluginName, "skill", e.SkillName)
			continue
		}
		seen[dk] = true

		rec, ok := parsePluginEntry(name, e)
		if !ok {
			// Entry had neither Body nor Path, or parsing yielded empty body.
			continue
		}

		p.full[fullKey] = rec
		// byInfo key: "<tierName>:<pluginName>:<skillName>" mirrors the Source
		// field ("tierName:pluginName") plus the skill name — uniquely identifies
		// each record for ResolveByInfo without re-parsing Source.
		p.byInfo[rec.info.Source+":"+rec.info.Name] = rec

		// First entry for a bare name wins.
		if _, conflict := p.bare[e.SkillName]; !conflict {
			p.bare[e.SkillName] = rec
		} else {
			slog.Warn("skillkit.PluginTier: bare-name conflict, first entry wins",
				"tier", name, "skill", e.SkillName, "plugin", e.PluginName)
		}
	}

	return Tier{Name: name, Resolver: p}
}

// parsePluginEntry parses a single PluginEntry at construction time.
// Returns the pre-populated pluginRecord and ok=true on success.
// ok=false when Body+Path are both empty, or the file cannot be read,
// or the body is empty after frontmatter strip.
func parsePluginEntry(tierName string, e PluginEntry) (pluginRecord, bool) {
	si := SkillInfo{
		Name:     e.SkillName,
		Path:     e.Path,
		Source:   tierName + ":" + e.PluginName,
		Metadata: Metadata{Name: e.SkillName},
	}

	// Path is always used for Dir when set, regardless of whether Body wins.
	if e.Path != "" {
		si.Dir = filepath.Dir(e.Path)
	}

	if e.Body != "" {
		// Pre-loaded body: no disk read needed.
		body, meta, ok := parseSkill(e.Body)
		if !ok {
			slog.Warn("skillkit.PluginTier: entry Body is empty after frontmatter strip, skipping",
				"tier", tierName, "plugin", e.PluginName, "skill", e.SkillName)
			return pluginRecord{}, false
		}
		if meta.Name != "" {
			si.Metadata = meta
		}
		return pluginRecord{info: si, body: body}, true
	}

	if e.Path != "" {
		raw, err := os.ReadFile(e.Path) //nolint:gosec // G304: plugin-supplied path
		if err != nil {
			slog.Warn("skillkit.PluginTier: cannot read skill file",
				"path", e.Path, "err", err)
			return pluginRecord{}, false
		}
		body, meta, ok := parseSkill(string(raw))
		if !ok {
			slog.Warn("skillkit.PluginTier: skill file has empty body after frontmatter strip, skipping",
				"path", e.Path)
			return pluginRecord{}, false
		}
		if meta.Name != "" {
			si.Metadata = meta
		}
		return pluginRecord{info: si, body: body}, true
	}

	// Neither Body nor Path provided.
	slog.Warn("skillkit.PluginTier: entry has neither Body nor Path, skipping",
		"tier", tierName, "plugin", e.PluginName, "skill", e.SkillName)
	return pluginRecord{}, false
}

func (p *pluginTier) Find(name string) (SkillInfo, string, bool) {
	rec, ok := p.resolveRecord(name)
	if !ok {
		return SkillInfo{}, "", false
	}
	return rec.info, rec.body, true
}

// resolveRecord maps a bare or namespaced name to a pre-parsed pluginRecord.
func (p *pluginTier) resolveRecord(name string) (pluginRecord, bool) {
	if strings.Contains(name, ":") {
		// Namespaced: "pluginName:skillName" — look up in full map.
		rec, ok := p.full[name]
		return rec, ok
	}
	// Bare: look up in the bare index.
	rec, ok := p.bare[name]
	return rec, ok
}

// ResolveByInfo implements bodyByInfoResolver. It uses the pre-built byInfo
// index (keyed by "tierName:pluginName:skillName") to return the body for the
// exact SkillInfo Walk selected, enabling correct locale routing when multiple
// plugin entries share the same bare SkillName but differ in locale.
func (p *pluginTier) ResolveByInfo(info SkillInfo) (string, bool) {
	key := info.Source + ":" + info.Name
	rec, ok := p.byInfo[key]
	if !ok {
		return "", false
	}
	return rec.body, true
}

func (p *pluginTier) Walk(yield func(SkillInfo)) {
	for _, rec := range p.full {
		yield(rec.info)
	}
}
