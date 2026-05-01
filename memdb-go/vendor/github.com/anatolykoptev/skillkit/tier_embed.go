// tier_embed.go — EmbedFSTier: embed.FS-backed skill tier, parsed once at construction.
package skillkit

import (
	"embed"
	"io/fs"
	"log/slog"
	"path"
	"strings"
)

type embedFSTier struct {
	name   string
	skills map[string]embedEntry // keyed by skill name (subdir name)
	byPath map[string]embedEntry // keyed by SkillInfo.Path for ResolveByInfo
}

type embedEntry struct {
	info SkillInfo
	body string
}

// embedFSConfig holds construction-time configuration for an EmbedFSTier.
type embedFSConfig struct {
	skillFilename string
}

// EmbedFSOption configures an EmbedFSTier.
type EmbedFSOption func(*embedFSConfig)

// WithEmbedSkillFilename overrides the default "SKILL.md" filename.
func WithEmbedSkillFilename(filename string) EmbedFSOption {
	return func(cfg *embedFSConfig) {
		cfg.skillFilename = filename
	}
}

// NewEmbedFSTier creates a tier that reads skills from an embed.FS.
// All skill files are parsed once at construction. root is the prefix
// directory inside the embed.FS (e.g. "tier_testdata/embedfs").
//
// Dir uses forward-slash path.Join (not filepath.Join) because embed.FS
// always uses forward slashes.
func NewEmbedFSTier(name string, fsys embed.FS, root string, opts ...EmbedFSOption) Tier {
	cfg := embedFSConfig{skillFilename: "SKILL.md"}
	for _, o := range opts {
		o(&cfg)
	}
	e := &embedFSTier{
		name:   name,
		skills: make(map[string]embedEntry),
		byPath: make(map[string]embedEntry),
	}
	buildEmbedFS(e, fsys, root, cfg.skillFilename)
	return Tier{Name: name, Resolver: e}
}

// buildEmbedFS walks the embed.FS and populates the tier's skills map.
func buildEmbedFS(e *embedFSTier, fsys embed.FS, root, skillFilename string) {
	_ = fs.WalkDir(fsys, root, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Match only <root>/<skillName>/<skillFilename>.
		rel := strings.TrimPrefix(fpath, root+"/")
		parts := strings.SplitN(rel, "/", 2) //nolint:mnd // 2 parts: skillName/filename
		if len(parts) != 2 || parts[1] != skillFilename {
			return nil
		}
		skillName := parts[0]

		raw, readErr := fsys.ReadFile(fpath)
		if readErr != nil {
			return nil
		}

		body, meta, ok := parseSkill(string(raw))
		if !ok {
			slog.Warn("skillkit.EmbedFSTier: skill has empty body, skipping",
				"path", fpath)
			return nil
		}

		si := SkillInfo{
			Name:     skillName,
			Path:     fpath,
			Dir:      path.Join(root, skillName),
			Metadata: meta,
		}
		entry := embedEntry{info: si, body: body}
		e.skills[skillName] = entry
		e.byPath[fpath] = entry
		return nil
	})
}

func (e *embedFSTier) Find(name string) (SkillInfo, string, bool) {
	entry, ok := e.skills[name]
	if !ok {
		return SkillInfo{}, "", false
	}
	return entry.info, entry.body, true
}

// ResolveByInfo implements bodyByInfoResolver. It looks up the pre-parsed
// entry by SkillInfo.Path (the embed.FS file path) so that locale-aware lookup
// can retrieve the exact body for the SkillInfo Walk selected.
func (e *embedFSTier) ResolveByInfo(info SkillInfo) (string, bool) {
	entry, ok := e.byPath[info.Path]
	if !ok {
		return "", false
	}
	return entry.body, true
}

func (e *embedFSTier) Walk(yield func(SkillInfo)) {
	for _, entry := range e.skills {
		yield(entry.info)
	}
}
