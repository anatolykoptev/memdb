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

### Что остаётся в Python (детальный аудит, апрель 2026)

> Верифицировано: grep `ProxyToProduct|ProxyLLMComplete|ValidatedFeedback` по `memdb-go/internal/handlers/` + `server.go:274-334`.

#### Полностью проксируется (100% Python)

| Endpoint | Обработчик в Go | Бэкенд Python | Приоритет |
|----------|-----------------|---------------|-----------|
| ~~`POST /product/feedback`~~ | `NativeFeedback` via `nativeAddForCube` | — | ✅ Фаза 4.5 |
| `POST /product/llm/complete` | `ProxyLLMComplete` | `llms/base.py` | 🟡 thin proxy |
| ~~`POST /product/chat/stream/playground`~~ | removed 2026-04-18 | — | ✅ Фаза 4.5 followup |
| ~~`POST /product/suggestions`~~ | removed 2026-04-18 | — | ✅ Фаза 4.5 followup |
| ~~`GET /product/suggestions/{user_id}`~~ | removed 2026-04-18 | — | ✅ Фаза 4.5 followup |

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
  → 4.5 (feedback — L) ← последний БЛОКЕР Фазы 5
    → 4.11 (cube tools: решение — S, порт или sunset — L или S)
      → 4.9 (tests, E2E parity)
        → 4.10b (optional service layer refactor, 🟢 оптимизация)
          → Фаза 5 (удаление memdb-api)
```

**Оценка:** Неделя 1 ✅. Остаётся 4.5 (2-3 недели) + 4.11 (решение 30 мин, реализация зависит).

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
