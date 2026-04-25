# Versioning Policy

MemDB follows semver (major.minor.patch). We are currently in **0.x phase** —
APIs may change between minor versions. Breaking changes will be flagged in
release notes; patch releases are bug-only.

## Why v0.22.0 (the reset)

Earlier internal versions reached v2.2.0 before public launch. To honestly
signal pre-1.0 status to new adopters, we re-versioned to **v0.22.0** for the
first public release.

- v0.22.0 = first public release (2026-04-26)
- v0.x phase: expect minor breaking changes; pin exact versions in production
- v1.0.0 will signal stable API commitment (target: when user_profiles layer +
  M10 features land + API surface stabilizes for 60+ days without breaking
  change)

The v1.x and v2.x tags remain in git history as pre-public internal iterations.
Don't pin against them — `^v2` does not have a future.

## Migration from v2.x to v0.22.0

If you somehow built against an internal v2.x release: the on-the-wire API is
backward-compatible. Just update your `image: anatolykoptev/memdb:v0.22.0` tag.
Database schema migration is automatic (postgres_migrations.go versioned runner).

## Breaking change policy in 0.x

- Minor (0.X.0) MAY break public API (handlers, MCP tools, env vars, schema)
- Patch (0.X.Y) bug-fix only, never breaks
- Each minor release notes a "BREAKING:" section if applicable

## After v1.0

- Standard semver: minor adds features non-breakingly, major for breaks
- LTS branch policy TBD when first major lands
