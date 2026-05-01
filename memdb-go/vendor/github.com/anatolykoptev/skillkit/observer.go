package skillkit

// Observer hooks runtime events into a caller-supplied recorder. All
// fields are optional; a nil hook is a no-op. Caller wires to its
// preferred metrics backend (prometheus, OpenTelemetry, slog, etc.) —
// skillkit core stays stdlib-only.
//
// Backwards compatibility: future skillkit versions may add new hook
// fields. Adding a nil-able field is non-breaking; existing Observers
// continue to work. Removing a field is a breaking change reserved for
// v2.0.0.
type Observer struct {
	// BodyCall fires on every Embedded.Body() and (when wired) Catalog
	// lookup that resolves a skill body. The source label distinguishes
	// resolution paths:
	//   - "embedded"        — embedded default served (env unset, or env
	//                         path failed all gates and cache was empty)
	//   - "env"             — env override path read from disk this call
	//   - "cache_hit"       — env path mtime matched cached entry
	//   - "last_known_good" — env path stat-failed, cached body returned
	BodyCall func(name, source string)

	// EnvFallback fires when an env override is set but rejected at one
	// of the gate stages. The reason label distinguishes:
	//   - "unreadable" — os.Stat returned err (path missing / no permission)
	//                    or os.ReadFile returned err (e.g. EISDIR); both
	//                    treated as "unreadable" to keep label cardinality
	//                    low in v0.2.0.
	//   - "too_large"  — file size > maxEnvOverrideSize (1 MiB)
	//   - "empty_body" — body empty after frontmatter strip
	EnvFallback func(name, reason string)

	// BodyBytes records the byte length of the body returned by Body().
	// Use as a histogram observation by the caller. Fires on every
	// successful Body() call regardless of source.
	BodyBytes func(name string, bytes int)

	// CatalogLoad fires on every Catalog.Load / LoadCtx call. The
	// outcome label distinguishes:
	//   - "hit"  — a tier resolved the name
	//   - "miss" — no tier had the name
	CatalogLoad func(name, outcome string)

	// CatalogSize fires once per tier at Catalog construction with the
	// count of skills the tier knows about. Useful as a gauge sanity
	// check that //go:embed picked up all expected files.
	CatalogSize func(tier string, count int)
}
