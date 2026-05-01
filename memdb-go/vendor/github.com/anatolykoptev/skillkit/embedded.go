package skillkit

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// maxEnvOverrideSize is the maximum size (in bytes) for an env-override
// skill file. Files larger than this are rejected at stat time.
const maxEnvOverrideSize = 1 << 20 // 1 MiB

// EmbeddedOption configures an Embedded at construction time.
type EmbeddedOption func(*Embedded)

// WithObserver attaches an Observer to the Embedded. Subsequent Body()
// and Diagnostic() calls fire the observer's hooks. Nil observer is a
// no-op (same as not calling WithObserver).
//
// Thread-safety: configure once at NewEmbedded construction; this option
// is not safe to apply after concurrent Body()/Diagnostic() callers
// have started. The observer field is read on every Body() without
// synchronization (intentional — zero hot-path cost).
func WithObserver(obs *Observer) EmbeddedOption {
	return func(e *Embedded) {
		e.observer = obs
	}
}

// Embedded resolves a single skill body. Use when a binary ships with
// exactly one named skill and operators may want to override it at
// runtime via an env path.
//
// Resolution per Body() call:
//  1. If EnvVar is set + path readable + file ≤1 MiB + body non-empty
//     → mtime-cached body.
//  2. Embedded default (parsed once at construction).
//
// Body() always returns non-empty. NewEmbedded panics if the embedded
// raw has empty body after frontmatter strip — build-time invariant.
//
// Body() provides eventual consistency: any call after a successful
// file edit returns the new body; concurrent callers during a rewrite
// see one consistent body version (not a torn read). The per-instance
// mutex covers the stat+read+cache-update sequence.
//
// Symlinks via env override: not validated. Operator owns the env
// value, so a symlink to anywhere is the operator's call.
//
// Embedded is safe for concurrent use.
type Embedded struct {
	name     string
	envVar   string
	body     string   // embedded default body, parsed once at construction
	metadata Metadata // embedded metadata, parsed once at construction
	observer *Observer

	mu          sync.Mutex
	cachedBody  string
	cachedMtime time.Time
}

// NewEmbedded creates an Embedded loader for a single named skill.
// embeddedRaw is the raw skill file content (frontmatter + body).
// envVar is the name of the environment variable operators may set to
// a file path that overrides the embedded default.
// opts are optional configuration options (e.g. WithObserver).
//
// Panics if name is invalid (via ValidateName) or if embeddedRaw has
// an empty body after frontmatter strip (build-time invariant).
//
// Existing callers using the 3-argument form continue to compile
// unchanged — the variadic opts parameter accepts zero values.
func NewEmbedded(name, envVar, embeddedRaw string, opts ...EmbeddedOption) *Embedded {
	if err := ValidateName(name, ""); err != nil {
		panic(fmt.Sprintf("skillkit.NewEmbedded: %v", err))
	}

	body := StripFrontmatter(embeddedRaw)
	if body == "" {
		panic(fmt.Sprintf("skillkit.NewEmbedded(%q): embedded raw has empty body after frontmatter strip", name))
	}

	meta := ParseMetadata(embeddedRaw)

	e := &Embedded{
		name:     name,
		envVar:   envVar,
		body:     body,
		metadata: meta,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// fireBodyCall invokes BodyCall and BodyBytes if the observer is set.
func (e *Embedded) fireBodyCall(source string, body string) {
	if e.observer == nil {
		return
	}
	if e.observer.BodyCall != nil {
		e.observer.BodyCall(e.name, source)
	}
	if e.observer.BodyBytes != nil {
		e.observer.BodyBytes(e.name, len(body))
	}
}

// fireEnvFallback invokes EnvFallback if the observer is set.
func (e *Embedded) fireEnvFallback(reason string) {
	if e.observer != nil && e.observer.EnvFallback != nil {
		e.observer.EnvFallback(e.name, reason)
	}
}

// Body returns the resolved skill body. Resolution order:
//  1. If envVar is set, the file at that path is ≤1 MiB, readable, and
//     non-empty after frontmatter strip — returns mtime-cached body.
//  2. If env path becomes transiently unreadable (e.g. atomic-rename
//     window where the writer renames a .tmp over the target), but a
//     prior successful read populated the cache — returns the cached
//     last-known-good body. Operator-friendly: a brief stat-fail does
//     not blip the binary back to the embedded default.
//  3. Otherwise returns the embedded default.
//
// I/O errors are logged via slog.Debug and never returned. Body() is
// best-effort — a service must never break on a hot-reload typo.
func (e *Embedded) Body() string {
	path := os.Getenv(e.envVar)
	if path == "" {
		e.fireBodyCall("embedded", e.body)
		return e.body
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	return e.resolveEnvBody(path)
}

// resolveEnvBody performs stat→cache-check→read under the instance mutex.
// Called only when path is non-empty.
func (e *Embedded) resolveEnvBody(path string) string {
	info, err := os.Stat(path) //nolint:gosec // G703: path from operator env; intentional
	if err != nil {
		slog.Debug("skillkit.Embedded: stat failed", "name", e.name, "path", path, "err", err) //nolint:gosec // G706: path is operator-controlled; not user input
		// Return last-known-good cache if available, else embedded default.
		e.fireEnvFallback("unreadable")
		if e.cachedBody != "" {
			e.fireBodyCall("last_known_good", e.cachedBody)
			return e.cachedBody
		}
		e.fireBodyCall("embedded", e.body)
		return e.body
	}

	if info.Size() > maxEnvOverrideSize {
		slog.Debug("skillkit.Embedded: file too large", "name", e.name, "path", path, "size", info.Size()) //nolint:gosec // G706: path is operator-controlled; not user input
		e.fireEnvFallback("too_large")
		e.fireBodyCall("embedded", e.body)
		return e.body
	}

	mtime := info.ModTime()
	if !e.cachedMtime.IsZero() && mtime.Equal(e.cachedMtime) && e.cachedBody != "" {
		e.fireBodyCall("cache_hit", e.cachedBody)
		return e.cachedBody
	}

	raw, err := os.ReadFile(path) //nolint:gosec // G304,G703: path from operator env; intentional
	if err != nil {
		slog.Debug("skillkit.Embedded: read failed", "name", e.name, "path", path, "err", err) //nolint:gosec // G706: path is operator-controlled; not user input
		// os.ReadFile failure treated as "unreadable" (same label as stat failure)
		// to keep reason cardinality low in v0.2.0. Both indicate the env path
		// is currently inaccessible regardless of root cause.
		e.fireEnvFallback("unreadable")
		if e.cachedBody != "" {
			e.fireBodyCall("last_known_good", e.cachedBody)
			return e.cachedBody
		}
		e.fireBodyCall("embedded", e.body)
		return e.body
	}

	stripped := StripFrontmatter(string(raw))
	if stripped == "" {
		slog.Debug("skillkit.Embedded: env file has empty body after strip", "name", e.name, "path", path) //nolint:gosec // G706: path is operator-controlled; not user input
		e.fireEnvFallback("empty_body")
		e.fireBodyCall("embedded", e.body)
		return e.body
	}

	e.cachedBody = stripped
	e.cachedMtime = mtime
	e.fireBodyCall("env", e.cachedBody)
	return e.cachedBody
}

// Metadata returns the parsed metadata from the embedded raw (not from
// any env-override file). This is stable across all calls.
func (e *Embedded) Metadata() Metadata {
	return e.metadata
}

// Diagnostic returns a human-readable string describing the current
// resolution state, suitable for startup logs. It re-stats the env
// path on every call (no caching) to reflect the current file state.
//
// Possible return values:
//   - "embedded default"
//   - "env override <path>"
//   - "env override <path> UNREADABLE → embedded default"
//   - "env override <path> TOO_LARGE → embedded default"
func (e *Embedded) Diagnostic() string {
	path := os.Getenv(e.envVar)
	if path == "" {
		return "embedded default"
	}

	info, err := os.Stat(path) //nolint:gosec // G703: path from operator env; intentional
	if err != nil {
		return "env override " + path + " UNREADABLE → embedded default"
	}

	if info.Size() > maxEnvOverrideSize {
		return "env override " + path + " TOO_LARGE → embedded default"
	}

	return "env override " + path
}

// ResetCache clears the mtime cache so the next Body() call re-reads
// the env-override file regardless of mtime. Intended for tests only;
// production code should never call this.
func (e *Embedded) ResetCache() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cachedBody = ""
	e.cachedMtime = time.Time{}
}
