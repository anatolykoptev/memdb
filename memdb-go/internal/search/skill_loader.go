package search

// skill_loader.go — load D10 system prompt from a Markdown skill file
// instead of the Go-encoded const.
//
// Why externalise:
//   - Iterate on prompts WITHOUT rebuild + redeploy. Edit the .md file,
//     mtime changes, next request reloads — A/B sweeps drop from 4-min
//     dozor cycles to single env-flip + curl /metrics.
//   - Anthropic skill format gives us frontmatter for versioning, locale
//     selection, and the body markdown is naturally structured for LLM
//     consumption (header + Rules + Examples + Output sections).
//   - Skill-writer subagents can generate improved variants directly into
//     the same file path — no Go knowledge required.
//
// Resolution order (first hit wins):
//
//   1. MEMDB_D10_SKILL_PATH env (absolute path) — operator override,
//      typical use is volume-mount in compose for hot-reload.
//   2. Bundled default at /etc/memdb/skills/d10-extractor.md inside the
//      Docker image (COPY in the Dockerfile lands here).
//   3. Repository default at internal/search/skills/d10-extractor.md
//      relative to the running binary's working dir — used in tests +
//      `go run ./...` from the repo root.
//   4. Hardcoded answerEnhanceSystemPrompt const (this file's last-line
//      fallback) — guarantees the path resolution is never load-bearing.
//
// Cache: by file mtime. On every loadSkillPrompt call we stat() the
// resolved path; if mtime matches the cached value we return the cached
// body. No TTL — operator edits the file, next request picks it up.
// Cost: one stat() per D10 enhance call (microseconds).

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// d10SkillCandidatePaths is the static fallback chain consulted when
// MEMDB_D10_SKILL_PATH is unset. Order matters — first existing file
// wins.
var d10SkillCandidatePaths = []string{
	"/etc/memdb/skills/d10-extractor.md",
	"internal/search/skills/d10-extractor.md",
}

// skillCache caches the parsed body keyed by absolute path. mtime is
// stored alongside so we reload when the operator edits the file.
type skillCacheEntry struct {
	mtime int64
	body  string
}

var (
	skillCacheMu sync.Mutex
	skillCache   = map[string]skillCacheEntry{}
)

// loadSkillPrompt returns the D10 system-prompt body. It tries the env
// override, then the candidate fallback chain; on any I/O error or no
// hit it returns the hardcoded const so the caller is never broken.
//
// The returned string is JUST the body — frontmatter (everything between
// the leading `---` fences) is stripped.
func loadSkillPrompt() string {
	if path := strings.TrimSpace(os.Getenv("MEMDB_D10_SKILL_PATH")); path != "" {
		if body, ok := readSkillFile(path); ok {
			return body
		}
		// Fall through — env path explicitly set but unreadable; we do
		// NOT silently swallow this in production. Logged at Debug via
		// the call site (applyAnswerEnhancement) — operator sees a
		// "skill load fallback" message.
	}
	for _, p := range d10SkillCandidatePaths {
		if body, ok := readSkillFile(p); ok {
			return body
		}
	}
	return answerEnhanceSystemPrompt
}

// readSkillFile reads a skill markdown file and returns its body
// (frontmatter stripped). Returns "" + false when the file does not
// exist, is empty, or fails to read. Caches by mtime — a later edit
// invalidates the cache automatically.
func readSkillFile(path string) (string, bool) {
	stat, err := os.Stat(path)
	if err != nil || stat.IsDir() {
		return "", false
	}
	mtime := stat.ModTime().UnixNano()
	skillCacheMu.Lock()
	if cached, ok := skillCache[path]; ok && cached.mtime == mtime {
		skillCacheMu.Unlock()
		return cached.body, true
	}
	skillCacheMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	body := stripFrontmatter(string(raw))
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	skillCacheMu.Lock()
	skillCache[path] = skillCacheEntry{mtime: mtime, body: body}
	skillCacheMu.Unlock()
	return body, true
}

// stripFrontmatter removes a YAML frontmatter block delimited by leading
// `---` lines. A skill file without frontmatter is returned unchanged.
//
// Format expected:
//
//	---
//	name: foo
//	version: 1.0.0
//	---
//	<body>
//
// The first line must be exactly "---" for stripping to engage; any
// other input is returned as-is.
func stripFrontmatter(s string) string {
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !scanner.Scan() {
		return s
	}
	if strings.TrimSpace(scanner.Text()) != "---" {
		return s
	}
	// Collect frontmatter lines until the closing ---.
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "---" {
			break
		}
	}
	// Whatever remains is the body.
	var b strings.Builder
	for scanner.Scan() {
		b.WriteString(scanner.Text())
		b.WriteString("\n")
	}
	return b.String()
}

// resetSkillCache is exported for tests so mtime cache does not bleed
// between cases. Production code never calls this.
func resetSkillCache() {
	skillCacheMu.Lock()
	skillCache = map[string]skillCacheEntry{}
	skillCacheMu.Unlock()
}

// SkillLoadDiagnostic returns a short string describing which path
// loadSkillPrompt resolved (or "fallback const" when none did). Used
// once at startup by cmd/server so the operator sees in stdout which
// prompt source is live. Not a hot-path call.
func SkillLoadDiagnostic() string {
	if path := strings.TrimSpace(os.Getenv("MEMDB_D10_SKILL_PATH")); path != "" {
		if _, ok := readSkillFile(path); ok {
			return fmt.Sprintf("env override %s", path)
		}
		return fmt.Sprintf("env override %s UNREADABLE → falling back", path)
	}
	for _, p := range d10SkillCandidatePaths {
		if _, ok := readSkillFile(p); ok {
			return fmt.Sprintf("default %s", p)
		}
	}
	return "fallback const (no skill file found)"
}
