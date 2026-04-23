# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.0] — 2026-04-23

### Highlights

**Versioned schema migration runner takes over from Python `schema.py`** —
memdb-go now owns PostgreSQL DDL management end-to-end. Closes Phase 4.13 of the
Go migration roadmap and unblocks Phase 5 (Python deprecation) from the
schema-management angle.

### Added — Schema management

- **`internal/db/RunMigrations`** — versioned SQL runner:
  - `pg_advisory_lock` on a pinned `*pgxpool.Conn` serializes concurrent
    startups across replicas
  - Per-migration transaction (body + `schema_migrations` insert commit
    atomically; half-apply impossible)
  - sha256 drift detection — edited-after-apply files get a Warn log and an
    OTel counter bump (no re-apply)
  - Baseline step marks `0001` applied without executing it when a pre-runner
    schema is detected (production transition path)
  - Fresh-DB bootstrap via `bootstrapGraphIfNeeded` — installs `age`, `vector`,
    `pg_trgm` extensions + `create_graph('memos_graph')` before any other DDL
  - Fail-fast: any error returns from `NewPostgres`, crashing startup so ops
    are notified (unlike `Ensure*Table` best-effort Warn)
- **`migrations/` embed FS** — versioned SQL files, applied in lex order:
  - `0001_phase2_user_cube_split.sql` — cubes table + memory user_id backfill
  - `0002_tsvector_fulltext.sql` — Chinese tsvector column + trigger + GIN
  - `0003_extensions_and_graph.sql` — extensions + AGE graph bootstrap
  - `0004_memory_embedding.sql` — `vector(1024)` column + HNSW halfvec index
- **Fresh-DB integration test** — `scripts/test-migrations-fresh-db.sh` +
  `cmd/migration-test`. Ephemeral Postgres, 8 psql assertions, idempotency
  check. `make test-migrations-fresh-db`. No new Go dependencies.

### Added — Observability

- **`memdb.migration.checksum_drift{name=...}` OTel counter** — dashboards can
  alert on `increase(...[5m]) > 0` instead of log-mining. Registered on first
  drift event.
- **Prometheus metrics exporter** — OTel Prometheus exporter on `/metrics`
  endpoint (pattern: `PROM_PORT = MCP_PORT + 1000`, so memdb-go at `9080`).
- **Domain metrics** for feedback pipeline, LLM client, embedder backends,
  scheduler workers, and add pipeline (requests / duration histograms /
  operations by type).

### Added — Search

- **Pre-migration cross-encoder enhancements** — `APIKey` Bearer auth,
  `MaxCharsPerDoc` cap, `gte-multi-rerank` default model. Prep for
  full go-kit/rerank migration.
- **go-kit/rerank migration** — cross-encoder rerank pipeline moved to shared
  `github.com/anatolykoptev/go-kit/rerank` package for reuse across services.

### Fixed

- **LLM JSON fence strip** (critical runtime): extract+dedup `json.Unmarshal`
  was failing on LLM responses wrapped in ` ```json ... ``` ` markdown code
  fences, producing `buffer flusher: flush failed` error spam every ~10s on
  prod. `StripJSONFence` helper in `internal/llm/jsonfence.go` (7 test cases:
  LF/CRLF, with/without language tag, bare fences, control). Post-deploy
  verified 0 errors/30s.
- **NewPostgres startup ordering**: `RunMigrations` now runs BEFORE the four
  `Ensure*Table` calls. On fresh DB, Ensure* used to fail-Warn because
  `memos_graph` schema didn't exist yet — service ran with missing
  `memory_edges`/`entity_nodes`/`entity_edges`/`user_configs` until second
  startup. Now self-heals on first boot.
- **AGE 1.7 agtype operator compatibility** — Memory table queries cast
  `agtype::text::jsonb` before `->>` to avoid `agtype ->> agtype` ambiguous
  resolution. Applied to `ListCubesByTag` containment check and inside
  `0001_phase2_user_cube_split.sql` (three latent bugs discovered by
  fresh-DB integration test).
- **OTel tracer** skips setup when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset
  instead of failing hard.

### Changed — Deprecations

- **`graph_dbs/polardb/schema.py`** marked DEAD CODE. Audit showed all call
  sites in `connection.py:87-101` were already commented out before Phase
  4.13 started. Module and `SchemaMixin` class docstrings updated; file
  retained as historical reference only.

### Removed

- **Dead endpoints**: `/product/chat/stream/playground`,
  `/product/suggestions`, `/product/suggestions/{user_id}`,
  `control_memory_scheduler` MCP tool. Callers survey: 0 external users.

### Dependencies

- `go-kit` bumped `v0.9.0` → `v0.21.0` → `v0.24.0` → `v0.24.1` (rerank
  package + cache Redis DB routing fix)

### Internal

- 42 commits across 10 PRs (#3 through #10, plus direct T1–T5 commits on
  `main` prior to the updated branch-only git hygiene rule).
- Prod state after release: `schema_migrations` table has 4 applied rows
  (`0001` baselined, `0002`/`0003`/`0004` executed). Restart is a clean
  idempotent no-op.

---

## [1.0.4] — 2026-04-18

### Added

- **Cross-encoder rerank** (#2): BGE-reranker-v2-m3 via embed-server
  `/v1/rerank` as search step 6.05. Expected +3-5 LoCoMo points.

### Security

- **11 advisories closed** (#1): 2 CRITICAL (`pgx` memory-safety, `grpc`
  authz bypass), 4 HIGH (`mcp-sdk` ×3, `otel` PATH hijacking), 5 MEDIUM.
- Dependency bumps: `pgx/v5 5.9.1`, `grpc 1.80.0`, `mcp-sdk 1.5.0`,
  `otel 1.43.0`.

### Artifacts

- goreleaser workflow attaches linux/darwin amd64/arm64 binaries for
  `memdb-mcp` and `mcp-stdio-proxy`.

---

## [1.0.0] — 2026-03-02

Initial public release. Baseline for changelog. See
[ROADMAP-GO-MIGRATION.md](ROADMAP-GO-MIGRATION.md) for the detailed history
of Python → Go migration phases 1–4.5 that preceded this tag.

[Unreleased]: https://github.com/anatolykoptev/memdb/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/anatolykoptev/memdb/compare/v1.0.4...v1.1.0
[1.0.4]: https://github.com/anatolykoptev/memdb/compare/v1.0.0...v1.0.4
[1.0.0]: https://github.com/anatolykoptev/memdb/releases/tag/v1.0.0
