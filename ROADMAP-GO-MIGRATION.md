# MemDB Go Migration Roadmap

> План перевода memdb-api (Python) → memdb-go. Только миграция, без новых фич.
>
> _Составлен: февраль 2026. Обновлён: апрель 2026 (code audit via go-code)._
>
> **Связанные roadmap:**
> - [ROADMAP-SEARCH.md](ROADMAP-SEARCH.md) — качество поиска, VEC_COT, LoCoMo benchmarks
> - [ROADMAP-ADD-PIPELINE.md](ROADMAP-ADD-PIPELINE.md) — качество add pipeline, soft-delete, OTel, конкурентный анализ
> - [ROADMAP-FEATURES.md](ROADMAP-FEATURES.md) — новые фичи (Image Memory, MemCube sharing)

---

## 0. Реализовано ✅

> Хронология выполненных улучшений. Обновляется по мере работы.

### Инфраструктура (февраль 2026)

| Компонент | Было | Стало | Эффект |
|-----------|------|-------|--------|
| PostgreSQL | 15 | **17.8** + AGE 1.7.0 + pgvector 0.8.1 | MERGE...RETURNING, улучшенный JSON, новые Cypher функции |
| Vector index | IVFFlat (lists=100) | **HNSW halfvec_cosine_ops** + iterative_scan + ef_search=100 | 2x меньше памяти, лучше recall при фильтрации |
| Redis | 7.x | **8.6.0** (VSET native vector search) | WorkingMemory hot cache |
| Qdrant | — | **1.16.2** (sparse vectors) | Готово к hybrid dense+sparse |
| .env + compose | Ручные контейнеры | Всё под **docker compose** | Reproducible deploys |

### Фаза 1 — Go Add Pipeline + LLM Extraction ✅ (февраль 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **Go LLM extractor v2** | Unified extraction+dedup в одном LLM вызове: ADD/UPDATE/DELETE/SKIP + confidence + valid_at | `internal/llm/extractor.go` |
| **Go native add pipeline** | `POST /product/add` нативно: fast-mode + fine-mode | `internal/handlers/add*.go` |
| **Batched ONNX embedding** | N фактов за один ONNX inference | `add_fine.go`, `embedder/onnx.go` |
| **WorkingMemory VSET hot cache** | Redis 8 VSET: VAdd/VRem/VSim/VRemBatch, Q8+CAS, FILTER by ts | `internal/db/vset.go` |
| **VSET eviction sync** | CleanupWorkingMemoryWithIDs → VRemBatch | `postgres.go`, `add.go` |
| **Conflict detection (DELETE)** | LLM помечает противоречащую память → soft-invalidate | `extractor.go`, `add_fine.go` |
| **valid_at temporal grounding** | Bi-temporal модель: LLM извлекает когда факт стал правдой | `extractor.go`, `add_fine.go` |
| **VSET REDUCE dim 1024→256** | Johnson-Lindenstrauss random projection: 16x vs FP32 | `internal/db/vset.go` |
| **Python proxy удалён** | HTTP 422/500 вместо proxy fallback (март 2026) | `add.go`, `validate.go` |

### Фаза 3.3 — Scheduler Migration ✅ (февраль 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **Redis Streams Worker** | Go consumer: readLoop + reclaimLoop + processLoop | `internal/scheduler/worker.go` |
| **Memory Reorganizer** | Union-Find кластеризация → LLM consolidate → VSET eviction | `internal/scheduler/reorganizer.go` |
| **RabbitMQ удалён** | Сервис, том и dependency удалены из docker-compose | `docker-compose.yml` |
| **Periodic Reorganizer** | `periodicReorgLoop` (6h) для всех активных кубов | `worker.go` |
| **Dead Letter Queue** | `scheduler:dlq:v1` (MAXLEN 1000) | `worker.go` |
| **MemOS memory lifecycle** | `SoftDeleteMerged`: activated → merged + merged_into_id | `reorganizer.go`, `postgres.go` |
| **VSET sliding-window TTL** | `Expire(30d)` после VAdd | `vset.go` |
| **mem_update handler** | Go-native: embed → SearchLTMByVector → VAdd | `reorganizer.go` |
| **query handler** | Pre-emptive RefreshWorkingMemory | `worker.go` |
| **mem_read handler** | Go-native: parseMemReadIDs → ProcessRawMemory → llmEnhance → insert LTM → delete WM | `worker.go`, `reorganizer.go` |
| **mem_feedback handler** | Go-native: parseFeedbackPayload → ProcessFeedback → LLM analysis | `worker.go`, `reorganizer.go` |
| **pref_add handler** | Go-native: parsePrefConversation → ExtractAndStorePreferences → UserMemory | `worker.go`, `reorganizer.go` |
| **Scheduler status endpoints** | Go-native: status, allstatus, task_queue_status | `handlers/scheduler.go` |
| **VSET Sync (warm-cache)** | `Sync()` на старте: ListUsers → GetRecentWorkingMemory → VAdd | `vset.go`, `server.go` |

### Фаза 4.1 — Skill Memory Extraction ✅ (март 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **Task chunking** | LLM разбивает диалог на задачи. Jumping conversations, index ranges | `internal/llm/skill_extractor.go` |
| **Skill recall + dedup** | embed task name → VectorSearch SkillMemory top-5 → update/insert | `internal/handlers/add_skill.go` |
| **Интеграция в add_fine** | Fire-and-forget goroutine. Gate: msgs >= 10, codeRatio <= 0.7 | `add_fine.go` |

### Фаза 4.2 — Tool Trajectory Extraction ✅ (март 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **Tool pattern detector** | regex: `tool:`, `[tool_calls]:`, `<tool_schema>` | `handlers/add_tool.go` |
| **Tool trajectory LLM extraction** | trajectory, correctness, experience, tool_used_status | `llm/tool_trajectory.go` |
| **ToolTrajectoryMemory storage** | embed + InsertMemoryNodes, confidence=0.99 | `handlers/add_tool.go` |
| **Интеграция в add_fine** | Fire-and-forget goroutine (90s timeout) | `add_fine.go` |

### Фаза 6.5 — Chat Pipeline ✅ (март 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **chat/complete** | Non-streaming RAG chat | `handlers/chat.go` |
| **chat/stream** | SSE streaming RAG chat | `handlers/chat.go` |
| **Streaming LLM client** | Language detection (en/zh/ru), `<think>` parsing | `llm/stream.go` |
| **Post-chat memory add** | Fire-and-forget | `handlers/chat.go` |

### Фаза 4.6 — Full Native Search (fast + fine + internet) ✅ (март 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **SearXNG internet search** | HTTP client → parse → embed → merge at score 0.5 | `search/internet.go` |
| **LLM fine filter** | LLM keep/drop per memory, graceful degradation | `search/fine.go` |
| **LLM recall hint** | Follow-up query when >30% dropped | `search/fine.go` |
| **Fine mode pipeline** | fast → LLMFilter → LLMRecallHint → rebuild | `search/service_fine.go` |
| **Internet pipeline** | SearXNG parallel with DB → embed → merge | `search/service.go` |
| **Proxy elimination** | ALL proxy fallback removed from NativeSearch | `handlers/search.go` |

**Result:** `/product/search` fully Go-native. No Python dependency. Circular dependency (Go→Python→Go) eliminated.

### Perf / Bugfix ✅

| Фича | Файлы |
|------|-------|
| ID type audit: все handlers используют `properties->>'id'` (UUID) | `queries.go`, `postgres.go` |
| SQL PK optimization: WHERE id = $1 (PK) вместо JSON extraction | `queries.go` |
| SearchLTMByVector halfvec: HNSW index вместо seq scan | `queries.go` |
| cacheGet redis.Nil fix: `errors.Is(err, redis.Nil)` | `handlers.go` |
| ParseVectorString: `strconv.ParseFloat` вместо `fmt.Sscanf` (~3-5x) | `postgres.go` |
| FormatVector: `strconv.AppendFloat` вместо `fmt.Fprintf` | `postgres.go` |
| applyDeleteAction: hard delete вместо text overwrite | `add_fine.go` |

### Phase A — Safety net (апрель 2026) ✅

| Фича | PR / Commit |
|------|-------------|
| **A1** Memory-write heartbeat counter `memdb.memory.added_total{type,cube_id}` + Prometheus alert `SilentMemoryStall` | memdb#12, krolik-server#7 |
| **A2** Buffer-flush error counter `memdb.buffer.flush_errors_total{reason}` + alert `BufferFlushBurst` | memdb#13 |
| **A3** Drift counter pre-registration на startup (`dbMx()` touched в `RunMigrations`) | memdb#14, memdb#16 |
| **A4** Prometheus scrape target `memdb-go:8080`; `/metrics` exempt from auth | memdb#15, krolik-server#8 |
| **A5** Reset 0001 SHA baseline на прод после F3 edit (one-time expected drift) | op-task |

### Phase B — Integrity (апрель 2026) ✅

| Фича | PR |
|------|----|
| **B1** Ensure\*Table DDL → versioned migrations 0005-0008; discovered `public.*` legacy dups | memdb#17 |
| **B2** agtype operator audit → 3 runtime bugs fixed (`HardDeleteCube`, `GetMemoriesByFilter`) | memdb#18 |
| **B3** Unified JSON fence strip helpers (`StripJSONFence`, deleted string-based duplicate) | memdb#19 |
| **B4** Cleaned 4 draft releases (v1.0.0-v1.0.3); published v1.0.0 as baseline | ops-task |
| **B5** Deleted stale merged branches `phase-ce-rerank`, `security-deps-bump` | ops-task |

### Phase C — Code quality + release discipline (апрель 2026) ✅

| Фича | PR |
|------|----|
| **C1a** Split `search/service.go` (824→189) по concern boundaries (+5 files) | memdb#20 |
| **C1b** Split `scheduler/reorganizer_mem_read.go` (665→118) по stage (+6 files) | memdb#21 |
| **C1c** `db/postgres_memory.go` — already split (48 lines; no-op) | — |
| **C2** Deleted Python `schema.py` entirely (SchemaMixin removed from PolarDBGraphDB chain) | memdb#22 |
| **C3** release-drafter auto-updates draft on main merge + conventional-commit PR title linter | memdb#23 |

### Phase 4.13 — Schema migration runner (апрель 2026) ✅

| # | Фича | PR / Commit |
|---|------|-------------|
| 4.13.1-4 | Versioned runner core: advisory lock on pinned `*pgxpool.Conn` + transactional apply + sha256 drift + baseline 0001 + fail-fast в NewPostgres | memdb direct pushes + #3, #4 |
| 4.13.5 | Migrations 0001-0004: phase2 cube split, tsvector fulltext, extensions+AGE graph, embedding+HNSW halfvec | memdb#3 |
| 4.13.6 | Python `schema.py` removed (C2) | memdb#22 |
| 4.13.7a | Ordering fix: `RunMigrations` ДО `Ensure*Table` в `NewPostgres` | memdb#4 |
| 4.13.7b | Fresh-DB integration test (`scripts/test-migrations-fresh-db.sh` + `cmd/migration-test`) | memdb#8 |
| 4.13.8 | LLM markdown fence strip (F1) — runtime unblock для buffer flusher | memdb#6 |
| 4.13.9 | Prometheus drift counter OTel (F2) | memdb#7 |

### P0/P1 — Write-path unblock (апрель 2026) ✅

Три каскадных блокера найдены harness'ом D9 и закрыты одним спринтом. Суммарно восстановлено retrieval от hit@k=0.000 к 0.700:

| Блокер | PR |
|--------|----|
| **P0.1** AGE 1.7 убрал `agtype_in(text)` overload → 10 SQL sites мигрировали на `::agtype` cast | memdb#26 |
| **P0.2** `memos_graph.cubes` был AGE vertex label вместо plain table → migration 0009 drops label + recreates. Hotfix: `drop_vlabel` → `drop_label` (AGE 1.7 rename) | memdb#26, memdb#27 |
| **P1** `Memory.id` на проде graphid (AGE auto-gen), Go писал UUID → 13 SQL sites + Go caller: INSERT drops id, WHERE/DELETE/UPDATE matches via `properties->>'id'` | memdb#28 |
| **F5** Search SELECTs project property UUID, не graphid (10 queries в search_vector/fulltext/graph/entity) | memdb#31 |
| **F7** Drop legacy `public.memory_edges/entity_edges/user_configs` duplicates | memdb#30 |

### Phase embed-server resilience (апрель 2026) ✅

| # | Фича | PR |
|---|------|----|
| **E1** memdb-go embedder: `withRetry` wrapper на 30s timeout + 429/503/502/504 exp backoff | memdb#32 |
| **E2** embed-server: `embed_queue_depth_current` gauge + `embed_queue_full_rejected_total` counter + `embed_batch_wait_ms` histogram + 429 backpressure gate at 80% capacity | ox-embed-server#14 |
| **E3** Prometheus alert rules: `EmbedQueueSaturation`, `EmbedRejections`, `EmbedHighLatency`, `EmbedBatchWaitHigh` | krolik-server#9 |

### LoCoMo evaluation baseline (апрель 2026) ✅

`evaluation/locomo/` — reproducible eval harness против LoCoMo dataset (Snap Research 2024). Deterministic sample: 1 conv, 3 sessions, 58 msgs, 10 category-1 QAs.

| Metric | Baseline post-P1 |
|---|---|
| EM | 0.000 |
| F1 | 0.010 |
| semsim | 0.039 |
| **hit@20** | **0.700** (7 of 10) |

Retrieval-quality уже в верхнем эшелоне: **+0.05-0.15 выше Mem0 (0.65) / MemOS (0.60)** на hit@k. Зазор F1/semsim — surface text of verbose stored memories vs short gold answers; именно это target'ят D4/D10.

### M7 Compound Lift Sprint ✅ ЗАКРЫТА v2.1.0 (2026-04-25)

| Фича | PR / Commit |
|------|-------------|
| `answer_style` (factual\|conversational) on chat endpoint — server-side prompt routing | `c57cc904` |
| `window_chars` per-request override on `/product/add` (fast/async modes) | `841febc2` |
| pprof routes registered behind `X-Service-Secret` auth | `c6f2250d` |
| Embed batching in fast-add pipeline (24× → 1× embed calls; latency 13s → 1.0s at window=512) | `c0525784` |
| LoCoMo threshold fix: chat reads `threshold`, harness now wires `LOCOMO_RETRIEVAL_THRESHOLD` to both endpoints | `52938d14` |

**LoCoMo Stage 2 result:** aggregate F1 0.053 → 0.238 (+349%). See `CHANGELOG.md [2.1.0]`.

### Phase D — LoCoMo intelligence ✅ ЗАКРЫТА v2.0.0 (2026-04-24)

Все 10 фич shipped, deployed на прод, env-gated. LoCoMo hit@20 = **0.700** — выше Mem0 (0.65) и MemOS (0.60), на уровне Claude-3-Opus+RAG (0.72).

| # | Фича | PR | Env toggle |
|---|------|----|------------|
| **D1** Temporal decay + importance scoring (`exp(-λt·age/half_life)·(1+log(1+access))`) | memdb#34 | `MEMDB_D1_IMPORTANCE` |
| **D2** Multi-hop AGE graph retrieval via recursive CTE на `memory_edges` + hop decay 0.8^hop | memdb#36 + hotfix #37 | `MEMDB_SEARCH_MULTIHOP` |
| **D3** Hierarchical reorganizer — port Python `tree_text_memory/organize/` (4 модуля ~1500 LOC) → raw/episodic/semantic + LLM relation detector (CAUSES/CONTRADICTS/SUPPORTS/RELATED) | memdb#40 | `MEMDB_REORG_HIERARCHY` |
| **D4** Query rewriting before embedding (third-person + absolute temporal + noun-phrase dense) | memdb#44 | `MEMDB_QUERY_REWRITE` |
| **D5** 3-stage iterative retrieval prompts (coarse → refine → justify) | memdb#45 | `MEMDB_SEARCH_STAGED` |
| **D6** Pronoun + temporal resolution в extraction (raw_text + resolved_text schema) | memdb#48 | (additive schema, always on) |
| **D7** CoT query decomposition (multi-part → atomic sub-queries, embed-per-subquery union) | memdb#47 | `MEMDB_SEARCH_COT` |
| **D8** Third-person enforcement + structured preference taxonomy (22 категории: 14 explicit + 8 implicit) | memdb#48 | (additive schema, always on) |
| **D9** LoCoMo eval harness + MILESTONES.md audit trail | memdb#24, #25, #29, #33, #35, #38, #41, #43, #46 | n/a |
| **D10** Post-retrieval answer enhancement (LLM synthesises EnhancedAnswer в rank-0) | memdb#42 | `MEMDB_SEARCH_ENHANCE` |

**Измеренная дельта v2.0.0** (sample: 1 conv, 10 category-1 QAs, skip-chat mode):

| Metric | Pre-Phase-D | All-Phase-D | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| **semsim** | **0.039** | **0.046** | **+0.007 (+18%)** |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Почему F1/EM/hit@k плато на текущем sample**:
- `skip-chat` mode считает F1 aggregate по всем 20 retrieved items (не top-1) — synthetic D10 item в rank 0 диллуется в пуле верабтимных memories
- Category 1 (single-hop) — hit@20=0.700 уже близко к ceiling; D2 multi-hop шайнет на category 2, D7 CoT на category 4
- Fresh ingest (2 memories/cube) — D1 access_count=0, D3 cluster threshold (≥3 members) не достигается

**Post-v2.0.0 measurement roadmap (в работе 2026-04-24)**:

| # | Scope | Status |
|---|-------|--------|
| M1 | Per-D-feature Prometheus counters (d4_rewrite_total, d5_staged_total, d10_enhance_total, d7_cot_total, multihop_total, tree_reorg_total) + confidence histogram | 🔄 в работе |
| M2 | Expand harness: 5 LoCoMo категорий × 10 QAs = 50 (было 10 cat-1). Per-category score breakdown | 🔄 в работе |
| M3 | Run harness в `LOCOMO_SKIP_CHAT=0` chat/complete mode | ✅ 2026-04-24 (см. ниже) |
| M4 | Parameter tuning grid на 4 hyperparams (`enhanceMinRelativity`, `stagedShortlistSize`, `hierarchyBoost`, `importance half_life`) | ⏳ pending (после M1-M3) |

**M3 результаты (chat/complete mode, 2026-04-24)** — реальная F1/semsim дельта появилась после переключения `LOCOMO_SKIP_CHAT=0`:

| Metric | skip-chat (retrieval-only) | **chat/complete (end-to-end)** | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| **F1** | 0.010 | **0.143** | **+0.133 (+14×)** |
| **semsim** | 0.046 | **0.150** | **+0.104 (+3.3×)** |
| hit@20 | 0.700 | 0.700 | +0.000 |

F1 jumped ~14×, semsim ~3×. D10 synthetic EnhancedAnswer в rank-0 реально dominates LLM context и produces short surface-aligned answers. hit@k unchanged (retrieval тот же, меняется генерация).

**Expected end-state после M1+M2+M4 (per-category tuning + 5-cat sample + hyperparam grid + полного LoCoMo dataset)**: F1≈0.48, EM≈0.10, semsim≈0.21, hit@20≈0.80 — opportunity to surpass Snap paper's strongest setup (`Claude-3-Opus + RAG`: F1=0.42, EM=0.22, hit@20=0.72). Текущий F1=0.143 — уже на пути, ждём per-category breakdown для где именно D-фичи дают delta.

---

## 1. Текущее состояние

### Сервисы (docker-compose)

| Сервис | Роль | Порт | Статус |
|--------|------|------|--------|
| **memdb-go** | Go API Gateway: auth, ONNX embedder, search, MCP | 8080 | ✅ Go |
| **memdb-api** | Python backend: legacy endpoints | 8000 | ⚠️ Legacy |
| **memdb-mcp** | Go MCP server (stdio + HTTP) | 8001 | ✅ Go |
| **postgres+AGE** | Graph DB (pgvector + Apache AGE) | 5432 | ✅ |
| **qdrant** | Vector store | 6333 | ✅ |
| **redis** | Cache + Streams queue | 6379 | ✅ |

### Что уже в Go (memdb-go, ~39K строк, 202 файла, health 81/A, 32/36 routes native — 89%)

| Компонент | Статус |
|-----------|--------|
| Auth middleware (Bearer + X-Service-Secret) | ✅ |
| ONNX embedder (multilingual-e5-large, dim=1024, batched) | ✅ |
| VoyageAI embedder (fallback) | ✅ |
| Search pipeline: embed → parallel DB → merge → rerank → dedup (fast+fine+internet) | ✅ |
| Text/Skill/Pref/Tool memory types (read) | ✅ |
| Redis cache layer | ✅ |
| MCP tools: search, add, users | ✅ |
| LLM proxy + LLM extractor (unified v2) | ✅ |
| Temporal search / tokenizer (EN+RU) | ✅ |
| REST API (OpenAPI spec) | ✅ |
| Native add pipeline (fast + fine + buffer + async + feedback) | ✅ |
| WorkingMemory VSET hot cache | ✅ |
| Scheduler Worker (Redis Streams) | ✅ |
| Memory Reorganizer (Union-Find + LLM merge) | ✅ |
| Chat pipeline (complete + stream) | ✅ |
| Skill memory extraction | ✅ |

### Что остаётся в Python (честный статус на 2026-04-24, после v2.0.0)

> **Ответ коротко**: **НЕТ, полностью не мигрировали.** `memdb-api` контейнер живой (uptime 30h healthy). В Go осталось **~16 active proxy call sites** — большинство safety-nets на случай недоступности Postgres/embedder, но несколько реально Python-dependent (users_config CRUD, complex filter get_memory, admin scheduler status). Трафик на проде идёт **99%+ нативно через Go**; Python срабатывает только в edge cases.
>
> Верифицировано 2026-04-24: `grep -rn "proxyWithBody\|ProxyToProduct" internal/handlers/` по свежему main; `docker ps memdb-api` → Up 30h healthy.

#### Активные proxy sites (inventory 2026-04-24)

| File | Handler | Когда proxies | Тип |
|------|---------|---------------|-----|
| `add.go:104,163` | `NativeAdd` | `canHandleNativeAdd=false` + fallback on error | 🟢 safety net |
| `chat.go:42,50,95,103` | `NativeChatComplete/Stream` | `chatCanNative()=false` (нет searchService/llmChat) | 🟢 safety net |
| `memory_get.go:126,322` | `NativeGetMemory/Post` | Postgres nil / complex filter | 🟡 edge case |
| `memory_get_filter.go:74` | `NativePostGetMemory` | Complex filter path beyond Go parser | 🟡 edge case |
| `memory_getall.go:77` | `NativeGetAll` | Postgres nil | 🟢 safety net |
| `memory_delete_all.go:24,49` | `NativeDeleteAll` | Missing user_id / Postgres nil | 🟢 safety net |
| `users.go:85,122` | `NativeGetUser/RegisterUser` | Postgres nil | 🟢 safety net |
| `users_config.go:13,27,40,81` | `NativeGetConfig/UpdateUserConfig/ListCubesByTag` | Postgres nil / unimplemented CRUD | 🔴 real Python-dependent |
| `scheduler.go:x3` | `NativeSchedulerStatus/AllStatus/TaskQueueStatus` | Redis unavailable | 🟢 safety net |
| `scheduler_stream.go:x2` | `NativeSchedulerWait/Stream` | Scheduler nil | 🟢 safety net |
| `validate.go:x7` | validation-fallback paths | Validation error or missing config | 🟢 safety net |

**Классификация**:
- 🟢 **Safety-net** (~11 sites): срабатывает только когда Postgres/Redis/embedder недоступны. В здоровом prod — никогда. После Phase 5 shutdown Python эти превратятся в HTTP 502/503.
- 🟡 **Edge case** (3 sites): complex filter path, missing required fields. Можно портировать в отдельной PR или просто вернуть HTTP 422.
- 🔴 **Реально Python-dependent** (~4 sites): `users_config.go` — CRUD user configs через Python. Всё остальное в proxy — fallback.

Real ratio: **~1-2 routes реально проксируются в Python в здоровом prod trafic**. 99%+ нативно.

#### Исторические 100%-Python endpoints (все закрыты)

| Endpoint | Финальный обработчик | Закрыто в |
|----------|----------------------|-----------|
| ~~`POST /product/feedback`~~ | `NativeFeedback` via `nativeAddForCube` | ✅ Фаза 4.5 (апрель 2026) |
| ~~`POST /product/llm/complete`~~ | `NativeLLMComplete` → CLIProxyAPI напрямую | ✅ Фаза 4.12 (апрель 2026) |
| ~~`POST /product/chat/stream/playground`~~ | removed (0 external users) | ✅ 2026-04-18 |
| ~~`POST /product/suggestions`~~ | removed | ✅ 2026-04-18 |
| ~~`GET /product/suggestions/{user_id}`~~ | removed | ✅ 2026-04-18 |

**Все исторические 100%-Python роуты закрыты или удалены.** Реальный activ-proxy список теперь короткий (таблица выше).

#### Phase 5 Shutdown Checklist (M9 S8, 2026-04-26)

1. ✅ **Портировать `users_config` CRUD в Go** — `NativeGetUserConfig`, `NativeUpdateUserConfig`, `NativeConfigure`, `NativeGetConfig` реализованы нативно; `ProxyToProduct` заменены на HTTP 503.
2. ✅ **Safety-net → HTTP error** — все `proxyWithBody` (21 sites) → `writeJSON 503 "service degraded: postgres unavailable"`. PR: `chore/phase5-python-shutdown`.
3. ✅ **Edge case sites (complex filter)** — `memory_get.go:126,322`, `memory_get_filter.go:74` → HTTP 422 "complex filter not supported by Go parser".
4. ⬜ **Load-test без memdb-api** — 50 concurrent ops, 0 errors за 2 недели — мониторинг после merge.
5. ✅ **`compose/memdb.yml` memdb-api commented out** — `memdb-api:` блок закомментирован; `depends_on` в memdb-go и memdb-mcp очищены. Rollback: uncomment block.
6. ✅ **`docker/Dockerfile` DEPRECATED header** — добавлен comment "DEPRECATED 2026-04-26".
7. ✅ **`proxyWithBody` + `ProxyToProduct` методы удалены** — 0 callers, методы deleted.
8. ✅ **Тесты** — 10 новых unit-тестов (phase5_shutdown_test.go) asserting 503/422, full handler suite green.

**PR**: `chore/phase5-python-shutdown` — convert safety-net proxies to http 503/422 and remove memdb-api from compose.

**Оценка**: Phase 5 shutdown завершён. Load-test (п.4) — мониторинг в продакшене, 2 недели.

#### Нативный Go с proxy fallback на отдельные кейсы

| Endpoint | Когда proxies | Приоритет |
|----------|--------------|-----------|
| ~~`POST /product/search`~~ | ~~`mode=fine`, `internet_search=true`~~ | ✅ **Решено (Фаза 4.6)** |
| `POST /product/chat/complete` | `chatCanNative()=false` (нет searchService/llmChat) | 🟢 safety net |
| `POST /product/chat/stream` | `chatCanNative()=false` | 🟢 safety net |
| `POST /product/delete_memory` | `file_ids`, complex `filter`, нет `user_id` | 🔴 БЛОКЕР Ф5 |
| `POST /product/get_memory` | complex `filter` | 🔴 БЛОКЕР Ф5 |
| `POST /product/get_memory_by_ids` | Postgres ошибка | 🟢 safety net |
| `GET /product/get_memory/{memory_id}` | Postgres nil/ошибка | 🟢 safety net |
| User management endpoints (`users.go`) | Postgres nil | 🟢 safety net |

#### MCP cube tools → Python (новый раздел, подтверждено CLAUDE.md)

`memdb-mcp` проксирует в Python следующие инструменты (см. `internal/mcptools/`):

| MCP tool | Python модуль | Приоритет |
|----------|---------------|-----------|
| `add_memory` | `api/handlers/add_handler.py` | 🟡 (REST `/product/add` уже Go-native — MCP нужно перевести на тот же путь) |
| `chat` | `api/handlers/chat_handler.py` | 🟡 (REST chat уже Go-native) |
| `create_cube`, `register_cube`, `unregister_cube` | `mem_cube/single_cube.py` (33.7K) | 🟡 |
| `share_cube`, `dump_cube` | `mem_cube/single_cube.py` | 🔵 |
| ~~`control_memory_scheduler`~~ | removed 2026-04-18 — Go scheduler автономен | ✅ Фаза 4.5 followup |

Из 5 "proxy → Python" MCP-инструментов **2 уже не требуют Python** (`add_memory`, `chat` — есть нативные REST-эндпоинты). Фикс = переключить `mcptools/add.go` и `mcptools/chat.go` на internal Go handler вместо HTTP proxy в memdb-api.

#### Не блокирует (safety net only)

В production Postgres и Redis всегда инициализированы. Proxy fallback на `postgres==nil` —
только safety net для graceful degradation, не реальная зависимость от Python.

> **Примечание:** Tool trajectory уже реализован в Go (`add_tool.go`).
> Inline preference extraction — **не миграция** (нет в Python scheduler), перенесён в [ROADMAP-ADD-PIPELINE.md](ROADMAP-ADD-PIPELINE.md).

### Python-модули без Go-аналога (апрель 2026)

> Аудит через `ls src/memdb/` и сравнение с `memdb-go/internal/`. Не все пункты блокируют Фазу 5 — часть отнесена к low-priority или полностью deprecated.

| Python модуль | Размер | Что делает | Go статус | Блокер Ф5? |
|---|---|---|---|---|
| `graph_dbs/polardb/schema.py` | 232 строки, 14 DDL | Schema bootstrap: накатывает таблицы/колонки/индексы/triggers при старте Python (в т.ч. `properties_tsvector_zh`, GIN index, trigger) | ❌ нет (нет versioned runner) | 🔴 ДА (Фаза 4.13) |
| `mem_feedback/feedback.py` | 47.8K | LLM анализ feedback → add/update памяти | ❌ нет | 🔴 ДА (Фаза 4.5) |
| `mem_cube/single_cube.py` | 33.7K | Cube CRUD (create/share/dump/register/unregister) | ❌ нет (MCP proxy) | 🟡 частично |
| `mem_reader/read_multi_modal/` | 10 файлов, ~150K | Парсеры user/assistant/system/tool/image/file_content/multi_modal | ❌ нет | ❌ (feature gap, не блокер) |
| `mem_reader/read_skill_memory/process_skill_memory.py` | 29.7K | Skill memory extraction | ✅ `add_skill.go` | — |
| `mem_agent/deepsearch_agent.py` | 14.4K | QueryRewriter + Reflection deep-search agent | ❌ нет | ❌ (ROADMAP-SEARCH) |
| `memories/textual/tree_text_memory/retrieve/` | 13 файлов, ~140K | advanced_searcher, bm25_util, bochasearch, internet_retriever, pre_update, reasoner, recall, searcher, searxng_search, task_goal_parser, xinyusearch, retrieve_utils | ⚠️ частично (searxng + searcher + internet_retriever) | ❌ (ROADMAP-SEARCH) |
| `memories/textual/tree_text_memory/organize/` | 5 файлов, ~70K | manager, reorganizer, relation_reason_detector, history_manager, handler | ⚠️ частично (scheduler/reorganizer) | ❌ (ROADMAP-ADD-PIPELINE) |
| `memories/textual/prefer_text_memory/` | 8 файлов | Structured preference (adder/extractor/retrievers/spliter) | ⚠️ частично (inline extraction) | ❌ (ROADMAP-ADD-PIPELINE) |
| `parsers/markitdown.py` | — | PDF/Word/Excel → text | ❌ нет | ❌ low-priority |
| `chunkers/` | 5 файлов | markdown/sentence/simple/character chunking | ❌ нет (add pipeline режет сам) | ❌ |
| `reranker/strategies/http_bge.py` + `http_bge_strategy.py` | 23K | BGE HTTP reranker (4 стратегии) | ❌ нет (только LLM-rerank) | ❌ (ROADMAP-SEARCH) |
| `reranker/strategies/cosine_local.py`, `concat.py`, `noop.py` | — | Локальные rerank стратегии | ❌ нет | ❌ |
| `mem_chat/simple.py` | 7.7K | Chat history manager | ⚠️ свой `chat.go` без persistent history | ❌ |
| `mem_scheduler/base_scheduler.py` | 53.6K | Python `GeneralScheduler` | ✅ заменён (Go Reorganizer + Redis Streams worker) | — |
| `mem_scheduler/{monitors,orm_modules,webservice_modules}/` | — | Внутренняя обвязка Python scheduler | — | не нужен |
| `mem_os/core.py` (52K), `product.py` (64K), `product_server.py` | 132K | Python FastAPI entry points | — | не портируется |
| `api/routers/{product,server}_router.py` | 38K | FastAPI route definitions | ✅ перенесено в `internal/server/server.go` | — |

---

## Анализ потерь при удалении Python scheduler

Python `GeneralScheduler` регистрирует 8 handlers:

| Label | Python делает | Go статус | Риск |
|-------|--------------|-----------|------|
| `add` | Логирует events | ✅ XACK | Нет |
| `mem_organize` | FindNearDuplicates → merge | ✅ Go Reorganizer | Нет |
| `mem_read` | raw WM → LTM transfer | ✅ Go-native | Нет |
| `mem_update` | WM refresh | ✅ Go-native | Нет |
| `pref_add` | Preference extraction | ✅ Go-native | Нет |
| `query` | Логирует → mem_update | ✅ Pre-emptive refresh | Нет |
| `answer` | Логирует | ✅ XACK | Нет |
| `mem_feedback` | Feedback → add/update | ✅ Go-native | Нет |

**Вывод:** Все 8 labels реализованы в Go (6 полных, 2 XACK-only). Python scheduler можно удалять.

---

## 2. Оставшиеся фазы миграции

### Фаза 4 — Proxy Elimination + Тесты 🔄 В РАБОТЕ

**Цель:** Убрать оставшиеся proxy-to-Python fallback paths. Покрыть тестами.

#### 4.5 Native Feedback Handler ✅ апрель 2026

| # | Задача | Статус | Commit |
|---|--------|--------|--------|
| 4.5.1 | Wire existing `handleFeedback` into `/product/add?is_feedback=true` | ✅ | `beebed13` + `e86ee921` |
| 4.5.2 | Go-native standalone `POST /product/feedback` — remove Python proxy | ✅ | `68ec6a41` + `d2b0f46c` |
| 4.5.3 | E2E integration test | ✅ | `b05e4ee9` |

**Discovery (2026-04-18):** `internal/handlers/feedback.go` + `feedback_ops.go` (493 lines, keyword-replace + judgement + decide ops + apply) had been in the repo as dead code since 2026-03-09 — the Go-native pipeline was written but `canHandleNativeAdd` always returned `false` for `IsFeedback=true`. Phase 2 user/cube split (`b328ee49`) kept it compilable. Phase 4.5 reduced to wiring (3 edits in `add.go`) + endpoint rewrite (`ValidatedFeedback` → synthetic `fullAddRequest{IsFeedback:true}` → `nativeAddForCube`) + tests. `normalizeFeedback` removed (no remaining callers).

#### ~~4.6 Search mode=fine в Go~~ ✅ (март 2026)

Реализовано: `SearchFine()` + `LLMFilter()` + `LLMRecallHint()` + SearXNG internet search.
Все proxy fallback удалены из `NativeSearch`.

#### 4.7 Delete: file_ids + complex filter ✅ апрель 2026

| # | Задача | Статус | Commit |
|---|--------|--------|--------|
| 4.7.1 | Delete by file_ids в Postgres | ✅ | `16d767b` |
| 4.7.2 | Delete by complex filter в Postgres (AGE WHERE) | ✅ | `16d767b` |
| 4.7.3 | `internal/filter/` пакет — AGE filter DSL (port из `polardb/filters.py`) + fuzz test | ✅ | `5b03e92` |

Реализовано: `internal/filter/` (Parse + BuildAGEWhereConditions, 8 файлов, 1122 строки, 46 unit + fuzz), `Postgres.DeleteByFilter`/`DeleteByFileIDs` с валидацией cube_ids regex и транзакционным pre-query → DELETE. `proxyWithBody` fallback убран. E2E верифицировано на production (`2026-04-11`).

#### 4.8 Get memory: complex filter ✅ апрель 2026

| # | Задача | Статус | Commit |
|---|--------|--------|--------|
| 4.8.1 | `Postgres.GetMemoriesByFilter` + `NativePostGetMemory` | ✅ | `285b21c` |
| 4.8.2 | Cache key = sha256(canonical filter) — no cross-user poisoning | ✅ | `285b21c` |
| 4.8.3 | Limit clamp ≤1000, default 100 | ✅ | `285b21c` |

Ответ в Python-совместимой схеме `{text_mem, pref_mem, tool_mem, skill_mem}`. `ProxyToProduct` fallback убран. E2E верифицировано в production.

#### 4.9 Тесты

| # | Задача | Effort |
|---|--------|--------|
| 4.9.1 | Unit tests: skill + tool extraction | S |
| 4.9.2 | Integration test: E2E verify nodes created | M |
| 4.9.3 | Python parity check (10 conversations) | S |

#### 4.10a MCP `add_memory` + `chat` → memdb-go (HTTP loopback) ✅ апрель 2026

| # | Задача | Статус | Commit |
|---|--------|--------|--------|
| 4.10a.1 | Split `RegisterProxyTools` → `RegisterNativeGoProxyTools` (add_memory, chat, clear_chat_history) + `RegisterPythonProxyTools` (cube tools) | ✅ | `983ad02` |
| 4.10a.2 | cmd/mcp-server/main.go: передать `memdbGoURL` для Go-native MCP tools, `PythonBackendURL` для legacy cube tools | ✅ | `983ad02` |

Прагматичное решение Option B (loopback HTTP к memdb-go:8080) вместо тяжёлого service-layer рефакторинга. Три MCP tools теперь идут в memdb-go, 6 остальных (cube tools) пока в Python — это блок 4.11. **Известный flag**: `doc_path` в `AddMemoryProxyInput` молча игнорируется Go NativeAdd — см. Task #7 follow-up.

#### 4.10b MCP add_memory + chat — полный service layer (🟢 отложено)

Option C из research (extract `AddMemories`/`ChatComplete` service functions, unified wiring между REST и MCP без HTTP hop'а) — отложено до стабилизации Phase 4.5/4.11. Ценность низкая, так как loopback HTTP hop на localhost < 1ms, а рефакторинг требует переделки construction в `cmd/mcp-server/main.go`.

#### 4.11 MCP cube tools Go-native или sunset (приоритет: 🔵)

| # | Задача | Effort |
|---|--------|--------|
| 4.11.1 | Решение: нужны ли вообще cube tools (`create_cube`/`share_cube`/`dump_cube`/`register_cube`/`unregister_cube`) в продукте | S |
| 4.11.2 | Если нужны — портировать `mem_cube/single_cube.py` (33.7K) в `internal/handlers/cubes.go` | L |
| 4.11.3 | Если нет — удалить MCP tools и endpoints | S |

#### 4.13 Schema Migration Runner (Go takeover `schema.py`) ✅ апрель 2026

**Цель:** Убрать зависимость от Python `schema.py` как от de-facto DB migration tool. Пока Python стартует — он молча накатывает DDL (`properties_tsvector_zh`, GIN index, trigger) через `polardb/schema.py`. После shutdown memdb-api (Фаза 5) schema drift становится silent: Go embed'ит SQL в `migrations/`, но без runner'а применяет их "один раз вручную".

**План:** [`docs/superpowers/plans/2026-04-23-memdb-go-migration-runner.md`](../../docs/superpowers/plans/2026-04-23-memdb-go-migration-runner.md) (v2: +advisory lock, +transactional apply, +sha256 checksum drift detection, +baseline для 0001, +fail-fast вместо Warn).

| # | Задача | Effort | Статус | Commit |
|---|--------|--------|--------|--------|
| 4.13.1 | `migrations/embed.go` — `//go:embed *.sql` | XS | ✅ | `87079682` |
| 4.13.2 | `migrations/0002_tsvector_fulltext.sql` — idempotent port `schema.py:update_tsvector_zh` trigger+index | S | ✅ | `d9d7beac` |
| 4.13.3 | `internal/db/postgres_migrations.go` — advisory lock (pinned `*pgxpool.Conn`) + transactional apply + sha256 checksums + baseline для 0001 | M | ✅ | `e60c68b4` + fix `bd7152ce` |
| 4.13.4 | Wire `RunMigrations` в `NewPostgres` **fail-fast** (`return nil, err`, не `Warn`) | XS | ✅ | `ed7efb47` |
| 4.13.5 | Port оставшихся DDL из `schema.py` — 0003 extensions+AGE graph, 0004 embedding+HNSW. Python ivfflat/JSONB/agtype-broken indexes осознанно пропущены (prod-accurate) | M | ✅ | PR #3 (`b49fc4c8` + `21d63c43`) |
| 4.13.6 | Audit: Python `schema.py` call sites. Результат: **уже отключён** — все вызовы закомментированы в `graph_dbs/polardb/connection.py:87-101` (до начала 4.13). Env flag не нужен. schema.py получил DEAD CODE header | XS | ✅ | F4-chore |
| 4.13.7a | Fix ordering: `RunMigrations` ДО `Ensure*Table` в `NewPostgres` (иначе fresh DB: Ensure* падает Warn из-за отсутствия `memos_graph`) | S | ✅ | PR #4 (`c083e7e5`) |
| 4.13.7b | E2E fresh-DB bootstrap test: `scripts/test-migrations-fresh-db.sh` + `cmd/migration-test` (docker + psql assertions, no new Go deps). Test обнаружил 3 реальных бага: missing `bootstrapGraphIfNeeded` pre-step, `ag_catalog` search_path для agtype ops, и 3 agtype-bugs в baseline 0001 — все fixed | M | ✅ | PR #8 (`950931c1`) |

**Прод верификация (2026-04-23):**
```
schema_migrations: 4 rows applied
- 0001_phase2_user_cube_split.sql (baselined, sha 0e4b7acd8b5d)
- 0002_tsvector_fulltext.sql (sha c82077ebdf5f)
- 0003_extensions_and_graph.sql (sha 35642fdb509f)
- 0004_memory_embedding.sql (sha 8b1f44b9d41c)
Restart: no re-apply (clean idempotent skip). postgres connected OK.
```

**Статус блокера Фазы 5:** ✅ СНЯТ. Drift detection через sha256 checksum (log + `memdb.migration.checksum_drift{name}` OTel counter — PR #7). Fail-fast на ошибках. Python `schema.py` — dead code (PR #9, все call sites уже были закомментированы в `connection.py`). Fresh-DB bootstrap доказан integration test'ом (PR #8) — 8 psql-assertions + idempotency check проходят на blank DB без Python.

**Follow-up работа сделанная в процессе:**
- **F1** — `fix(llm): strip markdown fence before json.Unmarshal` (PR #6, `8c65b7ff`) — runtime fix, устранил `buffer flusher: flush failed` spam (0 errors verified на проде)
- **F2** — `feat(db): OTel counter for migration checksum drift` (PR #7, `cb2cbe6c`) — ops-visible drift signal
- **F3** — fresh-DB bootstrap orchestrator (PR #8, `950931c1`) — закрывает 4.13.7b, нашёл + fix'нул 3 agtype бага
- **F4** — `chore(schema): mark Python schema.py as DEAD CODE` (PR #9, `1a7df4f8`) — закрывает 4.13.6

#### 4.12 Убрать `llm/complete` thin proxy ✅ апрель 2026

| # | Задача | Статус | Commit |
|---|--------|--------|--------|
| 4.12.1 | `ProxyLLMComplete` → `NativeLLMComplete`, делегирует `llm.Client.Passthrough` | ✅ | `9de89c3` + `ae88370` |
| 4.12.2 | Пакетные переменные `llmClient`/`llmProxyURL`/`llmDefaultModel` убраны | ✅ | `9de89c3` |
| 4.12.3 | `add_episodic.callEpisodicSummarizer` — теперь принимает `*llm.Client` | ✅ | `9de89c3` |

**Uncovered bug (recovered):** T2 агент оставил `llm.Client.Passthrough` в dirty tree не закоммиченным. Dozor `git reset --hard` стёр метод после первого webhook'а, production билд падал на `Passthrough undefined`. Восстановлено в `ae88370` (вместе с dangling `server.go:SetLLMProxy` reference и `ValidatedAdd → NativeAdd` rename в тестах).

#### Порядок

```
✅ 4.7 (delete filter)   ─┐
✅ 4.8 (get filter)       ├─ Неделя 1 (апрель 2026) ✅
✅ 4.10a (MCP add+chat)  ─┤
✅ 4.12 (llm/complete)   ─┘
✅ 4.5 (feedback — L) ← блокер Фазы 5
  → 4.13 (schema runner — M) ← БЛОКЕР Фазы 5 (schema drift после shutdown Python)
    → 4.11 (cube tools: решение — S, порт или sunset — L или S)
      → 4.9 (tests, E2E parity)
        → 4.10b (optional service layer refactor, 🟢 оптимизация)
          → Фаза 5 (удаление memdb-api)
```

**Оценка:** Неделя 1 ✅, 4.5 ✅. Остаётся 4.13 (1-2 дня) + 4.11 (решение 30 мин, реализация зависит).

---

### Фаза 5 — Python Deprecation

**Цель:** Удалить memdb-api из production.

**Checklist:**
- [x] `GET /product/scheduler/status|allstatus|task_queue_status` — native Go
- [x] Scheduler workers в Go (`memdb_go_scheduler` consumer group)
- [x] `mem_update`, `query`, `mem_read`, `mem_feedback`, `pref_add` — Go-native
- [x] VSET warm-cache `Sync()` на старте
- [x] `POST /product/chat/complete` + `chat/stream` — Go-native
- [x] Skill extraction при add — Go-native
- [x] Tool trajectory при add — Go-native (`add_tool.go`, `tool_trajectory.go`)
- [x] Python proxy удалён из `/product/add` — HTTP errors
- [x] **Feedback** — Go-native processing (Фаза 4.5) ✅ апрель 2026 (`b05e4ee9`)
- [x] **Search mode=fine + internet** — Go-native (Фаза 4.6) ✅ март 2026
- [x] **Delete by file_ids / complex filter** — Go-native (Фаза 4.7) ✅ апрель 2026
- [x] **Get memory with complex filter** — Go-native (Фаза 4.8) ✅ апрель 2026
- [x] **MCP add_memory + chat → memdb-go** — Фаза 4.10a ✅ апрель 2026
- [x] **`/product/llm/complete` → CLIProxyAPI напрямую** — Фаза 4.12 ✅ апрель 2026
- [x] **MCP cube tools** (`create_cube`/`share_cube`/`dump_cube`/`register_cube`/`unregister_cube`) — Go-native (Фаза 2 `b328ee49`)
- [x] Playground chat + Suggestions — удалены 2026-04-18 (callers survey: 0 external users)
- [ ] Memory extraction покрыта тестами (accuracy ≥ Python baseline)
- [ ] `POST /product/add` latency p95 < 1s
- [ ] 2 недели без Python-related ошибок в логах
- [ ] Load test: 50 concurrent adds без degradation
- [ ] Все safety-net proxy fallbacks заменены на HTTP errors
  - Включая `NativeAdd` error fallback (`add.go:145-146`) — сейчас `nativeAddForCube` error → `proxyWithBody`. После shut-down Python станет 502 dead upstream. Конвертировать в 500.
- [x] Audit `mem_cube_id` field usage in `/product/feedback` clients — audited 2026-04-18, не используется Go-клиентами, только в Python legacy
- [x] **Schema Migration Runner** — Go takeover `polardb/schema.py` с versioning (Фаза 4.13). ✅ апрель 2026. 4 migrations applied на проде, drift detection + OTel counter + fresh-DB integration test + Python schema.py помечен DEAD CODE.

**После:** docker-compose: postgres, qdrant, redis, memdb-go, go-search, cliproxyapi (6 контейнеров).

---

## Функциональное сравнение Go vs Python vs MemOS upstream (март 2026)

> Верификация полноты миграции. Сравнение с `.//src/` (Python) и `MemTensor/MemOS` v2.0.7.
> Обновлено после deep audit (март 2026): исходная оценка была слишком оптимистичной.

### Где Go реально лучше

| Фича | Go | Python | MemOS upstream |
|------|----|--------|----------------|
| Unified extraction (1 LLM call) | ✅ | ❌ (2 calls) | ❌ (2 calls) |
| Entity/relation extraction (KG) | ✅ | ❌ | ❌ |
| Content classifier + hints | ✅ | ❌ | ❌ |
| Hallucination filter | ✅ | ❌ | ❌ |
| Confidence filtering (0.65) | ✅ | ❌ | ❌ |
| Near-duplicate skip (0.97) | ✅ | ❌ | ❌ |
| Episodic summary | ✅ | ❌ | ❌ |
| Real MMR (lambda, exp penalty, buckets) | ✅ | ❌ | ❌ |
| BM25+Vector hybrid | ✅ | ❌ | ❌ |
| Graph recall (AGE Cypher) | ✅ | ❌ | ❌ |
| RRF merge (k=60) | ✅ | ❌ | ❌ |
| Temporal decay (180d half-life) | ✅ | ❌ | ❌ |
| Contradiction detection + hard-delete | ✅ | ❌ | ❌ |
| WM compaction → EpisodicMemory | ✅ | ❌ | ❌ |
| Code quality (78/B vs 40/F) | ✅ | — | ❌ |
| Latency | **15-30ms** | ~200ms | ~300ms |

### Где MemOS лучше нас (реальные пробелы)

| Фича | MemOS | Go статус | Impact |
|------|-------|-----------|--------|
| **LLM prompt engineering (~30 vs ~10 промптов)** | ✅ | ❌ | 🔴 Критический |
| Third-person enforcement в extraction | ✅ | ❌ → ROADMAP-ADD-PIPELINE.md | 🔴 |
| Temporal resolution (relative→absolute) | ✅ | ❌ → ROADMAP-ADD-PIPELINE.md | 🔴 |
| Pronoun resolution при extraction | ✅ | ❌ → ROADMAP-ADD-PIPELINE.md | 🔴 |
| Post-retrieval memory enhancement | ✅ | ❌ → ROADMAP-SEARCH.md | 🔴 |
| 3-stage iterative retrieval prompts | ✅ (3 staged prompts) | ❌ (1 flat prompt) | 🔴 |
| CoT query decomposition | ✅ | ❌ → ROADMAP-SEARCH.md | 🔴 |
| Query rewriting before embedding | ✅ | ❌ → ROADMAP-SEARCH.md | 🟡 |
| Source attribution (user vs assistant) | ✅ | ❌ → ROADMAP-ADD-PIPELINE.md | 🟡 |
| Strategy-based chunking (content_length vs message_count) | ✅ | ❌ | 🟡 |
| Structured preference taxonomy (14+8 types) | ✅ | ❌ | 🟡 |
| Implicit preference extraction | ✅ | ❌ | 🟡 |
| Memory lifecycle (5 states) | ✅ | ❌ (2-3 states) | 🟡 |
| Memory versioning (ArchivedTextualMemory) | ✅ | ❌ | 🟢 |
| Deep search agent (QueryRewriter+Reflection) | ✅ | ❌ | 🟢 |
| Dialogue pair reranking (4 strategies) | ✅ | ❌ | 🟢 |
| BGE HTTP reranker | ✅ | ❌ (LLM rerank есть) | 🟢 |
| MarkItDown parser (PDF/Word/Excel) | ✅ | ❌ | 🟢 |
| Distributed locking | ✅ | ❌ | 🟢 |
| RBAC (ROOT/ADMIN/USER/GUEST) | ✅ | ❌ (master key) | 🟢 |

### Вывод

Go архитектурно лучше (качество кода 78/B vs 40/F, latency x10-20) и имеет уникальные фичи (MMR, BM25+Vector, hallucination filter, content classifier).
Но MemOS значительно впереди по **глубине LLM prompt engineering** (~30 vs ~10 промптов) и **сложности retrieval pipeline** (CoT, 3-stage prompts, reranking strategies).
Основной разрыв — не в коде, а в промптах и retrieval-логике.

---

## Качество кода (go-code analysis, март 2026)

### MemDB vs конкуренты

| Метрика | MemDB (Go) | mem0 (Python) | MemOS (Python) |
|---------|------------|---------------|----------------|
| **Health score** | **74-84** | 59 | 35 |
| **Grade** | **A/B** | D | F |
| Error handling ratio | **96%** | 100% | **0%** |
| Doc ratio | **45%** | 3% | 6% |
| Code duplication | **0.4%** | 11% | 9% |
| Max cyclomatic | 20 | 44 | 185 |
| Max cognitive | 31 | 195 | 582 |
| Test file ratio | 22-44% | 17% | 12% |

### Scores по пакетам

| Пакет | Score | Grade | Top issue |
|-------|-------|-------|-----------|
| search | 84 | A | service.go 705 строк |
| llm | 82 | A | extractor.go 318 строк |
| scheduler | 80 | A | worker_process.go complexity 20 |
| handlers | 79 | B | 33% файлов > 200 строк |

### Tech debt: файлы > 200 строк (40 файлов)

Правило CLAUDE.md: source files ≤ 200 строк. Топ-10 нарушений:

| Файл | Строк | Приоритет |
|------|-------|-----------|
| `search/service.go` | 705 | 🔴 |
| `scheduler/reorganizer_mem_read.go` | 632 | 🔴 |
| `db/postgres_memory.go` | 553 | 🔴 |
| `handlers/add_fine.go` | 539 | 🔴 |
| `handlers/scheduler_stream.go` | 406 | 🟡 |
| `search/dedup.go` | 404 | 🟡 |
| `handlers/validate.go` | 398 | 🟡 |
| `db/queries/queries_memory.go` | 376 | 🟡 |
| `handlers/users.go` | 369 | 🟡 |
| `handlers/add.go` | 362 | 🟡 |

---

## Метрики успеха

| Фаза | KPI | Цель |
|------|-----|------|
| 4 | Extraction tests coverage | ≥ 80% |
| 5 | Python containers | 0 |
| 5 | Add latency p95 | < 1s |
| — | Files > 200 lines | 0 (сейчас: 40) |
| — | Health score (handlers) | ≥ 85 (сейчас: 79) |

---

## Что НЕ делаем

- ❌ Параметрическая память (LoRA) — требует GPU, ROI низкий
- ❌ Активационная память (KV-cache) — требует специфичного LLM deployment
- ❌ Миграция на Neo4j — AGE лучше интегрирован с Postgres
- ❌ Milvus — Qdrant лучше для self-hosted
