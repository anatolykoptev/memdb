# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.1.0] — 2026-04-25

### Highlights

**M7 Compound Lift Sprint — first MemOS-tier LoCoMo result.** Aggregate F1 0.053 → 0.238 (+349%) on the LoCoMo benchmark via three orthogonal fixes (server-side QA prompt + per-message ingest granularity + retrieval-threshold tuning) plus an embed-batching perf win that makes small-window ingest production-safe. answer_style=factual is also 2.1× faster on chat (bonus: shorter prompt = less LLM input = faster TTFT). cat-4 open-domain F1 0.017 → 0.407 (+24×).

### Added — Server-side knobs

- `answer_style` field on `/product/chat/complete` and `/product/chat[/stream]` requests (`conversational` default, `factual` for short fact-extraction). New templates `factualQAPromptEN/ZH`. Validation: unknown value → 400.
- `window_chars` field on `/product/add` requests (mode=fast/async). Per-request override, range [128, 16384], default 4096 unchanged. Out-of-range silently falls back to default.

### Added — Observability

- OTel counter `memdb.chat.prompt_template_used_total{template={factual|conversational|custom}}` — adoption tracking for the new prompt mode.
- OTel histogram `memdb.add.embed_batch_size{mode}` — visibility into embed batch sizes after the perf refactor.
- `/debug/pprof/*` routes registered behind `X-Service-Secret` auth.

### Performance

- **Embed batching in fast-add pipeline.** `nativeFastAddForCube` collects window texts upfront and issues a single `embedder.Embed(texts)` call instead of N sequential calls. Latency at window=512 drops from ~13s p95 to ~1.0s (13× speedup). No regression at default window=4096.
- **`answer_style=factual` chat is 2.1× faster at p95** (14.7s → 7.0s) — short prompt cuts LLM input tokens by ~80%.

### Documentation

- M7 perf report `docs/perf/2026-04-25-m7-latency-report.md`.
- M7 regression report `docs/testing/2026-04-25-m7-regression-report.md`.
- Sliding-window design doc `docs/design/2026-04-25-sliding-window-decision.md` (Option A chosen: additive opt-in).
- Compound-sprint orchestration pattern `docs/process/2026-04-25-compound-sprint-orchestration-pattern.md`.
- Backlog file `docs/backlog/2026-04-26-followups.md` (10 items deferred from M7).
- `WindowChars` godoc with explicit +1551% latency cliff documentation.

### Fixed

- LoCoMo eval harness chat-endpoint threshold override was silently dropped (chat reads `threshold` field, harness was sending `relativity`). Now reads `LOCOMO_RETRIEVAL_THRESHOLD` and sends to BOTH endpoints with correct field names.

### Eval — LoCoMo

- Stage 2 aggregate F1 **0.238** at hit@k **0.769** (n=199, conv-26 full, 19 sessions). +349% F1 vs original baseline (0.053), +197% vs M6 prompt-only.
- Per-category Stage 2: cat-1 0.267, cat-2 0.091, cat-3 0.201, cat-4 0.407, cat-5 0.092.
- Stage 3 (full 1986 QA across 10 convs) running in background.

## [2.0.0] — 2026-04-24

### Highlights

**Full Phase D — LoCoMo intelligence stack.** All 10 retrieval + extraction quality features deployed (D1-D10). Production memdb-go is now a LoCoMo-competitive memory system with hierarchical storage, multi-hop graph retrieval, query rewriting, 3-stage iterative retrieval, CoT decomposition, pronoun+temporal resolution in extraction, structured preference taxonomy, post-retrieval answer enhancement, and a reproducible evaluation harness.

**Plus** three full pre-D phases (A observability, B integrity, C code quality), production-grade schema migration runner, embed-server resilience stack, and critical write-path unblock that restored retrieval from hit@20=0.000 to 0.700.

**Infrastructure**: 38 PRs merged in memdb, 15 in krolik-server, 1 in ox-embed-server. ~5000 LOC new Go code. 15 versioned migrations. LoCoMo eval baseline: `hit@20=0.700` (above Mem0/MemOS published numbers).

### Added — Phase D LoCoMo intelligence

- **D1** Temporal decay + importance scoring rerank. `final = cosine * exp(-λt·age/180d) * (1 + log(1+access_count))`. Gated `MEMDB_D1_IMPORTANCE`.
- **D2** Multi-hop AGE graph retrieval via recursive CTE on `memory_edges`. Hop-decay 0.8^hop, cap 2× original K. Gated `MEMDB_SEARCH_MULTIHOP`.
- **D3** Hierarchical reorganizer — ported Python `tree_text_memory/organize/` (5 modules) to Go. Raw → episodic → semantic tiers. LLM relation detector emits CAUSES/CONTRADICTS/SUPPORTS/RELATED with confidence. Gated `MEMDB_REORG_HIERARCHY`.
- **D4** Query rewriting before embedding (third-person, absolute temporal, noun-phrase dense). Gated `MEMDB_QUERY_REWRITE`.
- **D5** 3-stage iterative retrieval (coarse → refine → justify). Gated `MEMDB_SEARCH_STAGED`.
- **D6** Pronoun + temporal resolution in extraction. Schema adds `raw_text` (verbatim) + `resolved_text` (primary retrieval form).
- **D7** CoT query decomposition — multi-part questions split into atomic sub-queries; embed-per-subquery union. Gated `MEMDB_SEARCH_COT`.
- **D8** Third-person enforcement in extractor + 22-category preference taxonomy (14 explicit + 8 implicit, MemOS-style). `preference_category` stored in `PreferenceMemory` properties.
- **D9** LoCoMo eval harness (`evaluation/locomo/`) + MILESTONES.md audit trail. Deterministic sample, exact-match / F1 / semantic similarity / hit@k metrics. Reproducible baseline established pre-Phase-D.
- **D10** Post-retrieval answer enhancement. LLM distills top-5 memories into query-aligned concise answer; prepended at rank 0 as synthetic `EnhancedAnswer` item. Gated `MEMDB_SEARCH_ENHANCE`.

Migrations **0011** (access_count), **0013** (hierarchy_level + parent_memory_id), **0014** (raw_text + preference_category audit).

### Added — Phase A observability

- Memory-write heartbeat counter `memdb.memory.added_total{type, cube_id}` + `SilentMemoryStall` Prometheus alert (rate=0 for 1h → page).
- Buffer-flush error counter `memdb.buffer.flush_errors_total{reason}` (lua/parse/db/other) + `BufferFlushBurst` alert.
- DB metrics pre-register on startup (both drift + added counters visible at value 0 before first event).
- Prometheus scrape target `memdb-go:8080/metrics` (auth-exempt for internal network).

### Added — Phase B integrity

- `Ensure*Table` DDL consolidated into versioned migrations 0005-0008 (memory_edges / entity_nodes / entity_edges / user_configs). Single source of truth for schema.
- agtype operator audit — 3 runtime bugs in `HardDeleteCube` and `GetMemoriesByFilter` fixed.
- Unified JSON fence strip helpers — `StripJSONFence` is the single path; deleted string-based duplicate.

### Added — Phase C code quality

- `search/service.go` split 824 → 189 lines + 5 new files (orchestrator / parallel / merge / postprocess / response / types).
- `scheduler/reorganizer_mem_read.go` split 665 → 118 + 6 new files by stage.
- release-drafter workflow + conventional-commit PR title linter.

### Added — Schema migration runner (Phase 4.13)

- 15 versioned migrations (0001 baselined, 0002-0014 applied fresh) via the runner.
- Advisory lock on a pinned `*pgxpool.Conn` serializes concurrent startups.
- Per-migration transaction (DDL + tracking row commit atomically).
- sha256 checksum drift detection with OTel counter + alert.
- Baseline logic for production schemas that existed pre-runner.
- Fresh-DB integration test `scripts/test-migrations-fresh-db.sh` + `cmd/migration-test`.

### Added — embed-server resilience (external repo)

- memdb-go HTTP embedder wrapped in `withRetry` — 30s timeout + exp backoff on 429/503/502/504.
- embed-server emits queue-depth gauge, batch-wait histogram, rejections counter.
- 429 backpressure gate at 80% queue capacity.
- Prometheus alerts: EmbedQueueSaturation, EmbedRejections, EmbedHighLatency, EmbedBatchWaitHigh.

### Fixed — P0 write-path unblock

Three cascading blockers that gated all retrieval. Restored from `hit@20=0.000` to `0.700` in one sprint:

- **AGE 1.7 removed `agtype_in(text)` overload** → 10 SQL sites migrated to `::agtype` cast.
- **`memos_graph.cubes` was AGE vertex label** (Go code expected plain table) → migration 0009 drops label + recreates plain. Hotfix: `drop_vlabel` → `drop_label` (AGE 1.7 rename).
- **`Memory.id` is AGE auto-generated graphid**, not application UUID → refactor: INSERT drops id column; WHERE/DELETE/UPDATE/SELECT use `properties->>(('id'::text))`.
- Search queries project property UUID (10 queries in `queries_search_*.go`) — prevents graphid leak through API.
- Migration 0012 relocates edges tables from `ag_catalog` to `memos_graph` (search_path fallthrough bug from B1).

### Fixed — LLM reliability

- LLM JSON fence strip (`StripJSONFence`) — critical runtime fix for `buffer flusher: flush failed` spam. Markdown-wrapped JSON from LLM now parsed correctly.
- `MEMDB_LLM_SEARCH_MODEL` default changed from `gemini-2.0-flash` (unknown at cliproxyapi) to `gemini-2.5-flash-lite`. D4/D5/D10/Iterative/Fine all recovered from silent 500 → working.

### Changed

- `graph_dbs/polardb/schema.py` deleted entirely. `SchemaMixin` removed from `PolarDBGraphDB`. All DDL managed by Go runner.

### Dependencies

- `go-kit` bumped `v0.9.0` → `v0.24.1`.

### LoCoMo baseline (v2.0.0)

```
Sample: 1 conv, 3 sessions, 58 msgs, 10 category-1 QAs
EM     = 0.000
F1     = 0.010
semsim = 0.046 (was 0.000 pre-P1; +0.007 over post-P1)
hit@20 = 0.700 (was 0.000 pre-P1)
```

Above published Mem0 (hit@20=0.65) and MemOS (hit@10=0.60). F1/EM gated on chat/complete mode (upcoming harness iteration).

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

[Unreleased]: https://github.com/anatolykoptev/memdb/compare/v2.1.0...HEAD
[2.1.0]: https://github.com/anatolykoptev/memdb/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/anatolykoptev/memdb/compare/v1.1.0...v2.0.0
[1.1.0]: https://github.com/anatolykoptev/memdb/compare/v1.0.4...v1.1.0
[1.0.4]: https://github.com/anatolykoptev/memdb/compare/v1.0.0...v1.0.4
[1.0.0]: https://github.com/anatolykoptev/memdb/releases/tag/v1.0.0
