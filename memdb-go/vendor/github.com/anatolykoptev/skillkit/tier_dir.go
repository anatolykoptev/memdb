// tier_dir.go — DirTier: filesystem-backed skill tier with optional mtime cache.
package skillkit

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

func (d *dirTier) Find(name string) (SkillInfo, string, bool) {
	skillDir := filepath.Join(d.dir, name)
	skillFile := filepath.Join(skillDir, d.skillFilename)

	// Guard: the skill sub-directory itself must not be a symlink.
	lfi, err := os.Lstat(skillDir)
	if err != nil {
		return SkillInfo{}, "", false
	}
	if lfi.Mode()&os.ModeSymlink != 0 {
		slog.Warn("skillkit.DirTier: skill directory is a symlink, skipping",
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
	// Symlink traversal guard on the SKILL.md itself.
	lfi, err := os.Lstat(skillFile)
	if err != nil {
		return SkillInfo{}, "", false
	}
	if lfi.Mode()&os.ModeSymlink != 0 {
		resolved, resolveErr := filepath.EvalSymlinks(skillFile)
		if resolveErr != nil {
			slog.Warn("skillkit.DirTier: cannot resolve symlink, skipping",
				"path", skillFile, "err", resolveErr)
			return SkillInfo{}, "", false
		}
		rel, relErr := filepath.Rel(d.dir, resolved)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			slog.Warn("skillkit.DirTier: symlink escapes tier root, skipping",
				"path", skillFile, "resolved", resolved)
			return SkillInfo{}, "", false
		}
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
		slog.Warn("skillkit.DirTier: skill has empty body after frontmatter strip, skipping",
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
			slog.Warn("skillkit.DirTier: skill directory is a symlink, skipping",
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
