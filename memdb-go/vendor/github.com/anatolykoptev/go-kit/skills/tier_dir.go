// tier_dir.go — DirTier: filesystem-backed skill tier with optional mtime cache.
package skills

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type dirTier struct {
	name          string
	dir           string
	skillFilename string
	useMtimeCache bool

	mu    sync.Mutex
	cache map[string]dirCacheEntry
}

type dirCacheEntry struct {
	mtime int64 // UnixNano
	body  string
	info  SkillInfo
}

// DirTierOption configures a DirTier.
type DirTierOption func(*dirTier)

// WithSkillFilename overrides the default "SKILL.md" filename.
func WithSkillFilename(filename string) DirTierOption {
	return func(d *dirTier) {
		d.skillFilename = filename
	}
}

// WithMtimeCache enables an opt-in mtime-based body cache.
func WithMtimeCache() DirTierOption {
	return func(d *dirTier) {
		d.useMtimeCache = true
	}
}

// NewDirTier creates a filesystem tier that scans one level deep inside dir.
// Each sub-directory that contains a SKILL.md (or the configured filename)
// is treated as a skill. Symlinks are skipped with a slog.Warn.
func NewDirTier(name, dir string, opts ...DirTierOption) Tier {
	d := &dirTier{
		name:          name,
		dir:           dir,
		skillFilename: "SKILL.md",
	}
	for _, o := range opts {
		o(d)
	}
	return Tier{Name: name, Resolver: d}
}

// Find loads the skill called name. name must satisfy the agentskills.io name
// rule (ValidateName): that rules out empty names, ".", "..", path
// separators and absolute paths before any path is built, so a lookup can
// never address anything but a direct child of the tier root. loadFile
// re-checks containment on the resolved path as defence in depth.
func (d *dirTier) Find(name string) (SkillInfo, string, bool) {
	if err := ValidateName(name, ""); err != nil {
		slog.Warn("skills.DirTier: invalid skill name, skipping",
			"tier", d.name, "name", name, "err", err)
		return SkillInfo{}, "", false
	}
	skillDir := filepath.Join(d.dir, name)
	skillFile := filepath.Join(skillDir, d.skillFilename)

	// Guard: the skill sub-directory itself must not be a symlink.
	lfi, err := os.Lstat(skillDir)
	if err != nil {
		return SkillInfo{}, "", false
	}
	if lfi.Mode()&os.ModeSymlink != 0 {
		slog.Warn("skills.DirTier: skill directory is a symlink, skipping",
			"tier", d.name, "path", skillDir)
		return SkillInfo{}, "", false
	}

	info, body, ok := d.loadFile(skillFile)
	if !ok {
		return SkillInfo{}, "", false
	}
	return info, body, true
}

// loadFile reads, validates, and (optionally) caches the contents of skillFile.
func (d *dirTier) loadFile(skillFile string) (SkillInfo, string, bool) {
	// A missing file is the ordinary "not a skill dir" case: stay quiet.
	if _, err := os.Lstat(skillFile); err != nil {
		return SkillInfo{}, "", false
	}
	// Containment guard: whatever the path looks like, the file it resolves
	// to (symlinks followed, on both sides) must live inside the tier root.
	if !d.insideRoot(skillFile) {
		slog.Warn("skills.DirTier: skill file resolves outside tier root, skipping",
			"path", skillFile)
		return SkillInfo{}, "", false
	}

	if d.useMtimeCache {
		if info, body, hit := d.cacheGet(skillFile); hit {
			return info, body, true
		}
	}

	raw, err := os.ReadFile(skillFile) //nolint:gosec // G304: path constructed from tier root
	if err != nil {
		return SkillInfo{}, "", false
	}

	body, meta, ok := parseSkill(string(raw))
	if !ok {
		slog.Warn("skills.DirTier: skill has empty body after frontmatter strip, skipping",
			"path", skillFile)
		return SkillInfo{}, "", false
	}

	skillDir := filepath.Dir(skillFile)
	skillName := filepath.Base(skillDir)

	si := SkillInfo{
		Name:     skillName,
		Path:     skillFile,
		Dir:      skillDir,
		Metadata: meta,
	}

	if d.useMtimeCache {
		fi, statErr := os.Stat(skillFile) //nolint:gosec // G304
		if statErr == nil {
			d.cachePut(skillFile, fi.ModTime().UnixNano(), body, si)
		}
	}

	return si, body, true
}

// insideRoot reports whether path, once symlinks are resolved on both the
// path and the tier root, is strictly inside the root. Resolving the root
// matters: a root reached through a symlink (or a symlinked parent such as
// /tmp on macOS) would otherwise never be a prefix of a resolved path.
func (d *dirTier) insideRoot(path string) bool {
	root, err := filepath.EvalSymlinks(d.dir)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	// ".." as a whole path element only: a directory legitimately named
	// "..data" must not be mistaken for an escape.
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// cacheGet returns (info, body, true) when the cache has a fresh entry.
func (d *dirTier) cacheGet(skillFile string) (SkillInfo, string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cache == nil {
		return SkillInfo{}, "", false
	}
	entry, ok := d.cache[skillFile]
	if !ok {
		return SkillInfo{}, "", false
	}

	fi, err := os.Stat(skillFile) //nolint:gosec // G304
	if err != nil || fi.ModTime().UnixNano() != entry.mtime {
		return SkillInfo{}, "", false
	}
	return entry.info, entry.body, true
}

// cachePut stores or replaces a cache entry.
func (d *dirTier) cachePut(skillFile string, mtime int64, body string, info SkillInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cache == nil {
		d.cache = make(map[string]dirCacheEntry)
	}
	d.cache[skillFile] = dirCacheEntry{mtime: mtime, body: body, info: info}
}

// ResolveByInfo implements bodyByInfoResolver. It re-reads (or serves from
// cache) the skill file at info.Path so that locale-aware lookup can retrieve
// the correct body after Walk has selected the best SkillInfo candidate.
func (d *dirTier) ResolveByInfo(info SkillInfo) (string, bool) {
	if info.Path == "" {
		return "", false
	}
	_, body, ok := d.loadFile(info.Path)
	return body, ok
}

func (d *dirTier) Walk(yield func(SkillInfo)) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			slog.Warn("skills.DirTier: skill directory is a symlink, skipping",
				"tier", d.name, "name", e.Name())
			continue
		}
		skillFile := filepath.Join(d.dir, e.Name(), d.skillFilename)
		si, _, ok := d.loadFile(skillFile)
		if !ok {
			continue
		}
		yield(si)
	}
}
