// Package featureflags centralises env-var names + parsers for runtime
// feature toggles that span more than one package.  Two-or-more-readers
// is the threshold to live here; single-reader flags stay local to that
// package.
//
// Initial inhabitants:
//   - F3 events (MEMDB_F3_EVENTS) — read by handlers (write path) AND
//     search (read path).  Both must agree, else the extractor writes
//     rows that the injector ignores (or vice versa).
//
// The package has no other dependencies — safe to import from anywhere.
package featureflags

import (
	"os"
	"strings"
)

const (
	// EnvF3Events is the canonical env var name gating M11 F3 (Memobase
	// event extractor) on the write side AND the search-time event-inject
	// stage on the read side.  Default-on; "false"/"0" disables.
	EnvF3Events = "MEMDB_F3_EVENTS"
)

// boolEnabled returns true unless the env value is the literal "false" or "0"
// (case-insensitive, whitespace-trimmed).  Empty / unset → true.  Used by
// the F3 toggle so callers stay byte-for-byte identical to the previous
// per-package implementations.
func boolEnabled(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "false", "0":
		return false
	default:
		return true
	}
}

// F3EventsEnabled reports whether the F3 event extractor + injector should
// run.  Reads MEMDB_F3_EVENTS each call — supports test-time toggling via
// t.Setenv without process restart.
func F3EventsEnabled() bool {
	return boolEnabled(EnvF3Events)
}
