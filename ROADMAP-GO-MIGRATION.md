# MemDB Go Migration Roadmap
> Анализ MemTensor/MemOS + mem0ai/mem0 → Фазовый план перевода на Go

_Составлен: февраль 2026. Основан на deep-анализе upstream репозиториев и текущей инфраструктуры._

---

## 0. Реализовано ✅

> Хронология выполненных улучшений. Обновляется по мере работы.

### Инфраструктура (февраль 2026)

| Компонент | Было | Стало | Эффект |
|-----------|------|-------|--------|
| PostgreSQL | 15 | **17.8** + AGE 1.7.0 + pgvector 0.8.1 | MERGE...RETURNING, улучшенный JSON, новые Cypher функции |
| Vector index | IVFFlat (lists=100) | **HNSW halfvec_cosine_ops** + iterative_scan + ef_search=100 | 2x меньше памяти, лучше recall при фильтрации |
| Redis | 7.x | **8.6.0** (VSET native vector search) | Готово к WorkingMemory hot cache |
| Qdrant | — | **1.16.2** (sparse vectors) | Готово к hybrid dense+sparse preference search |
| .env + compose | Ручные контейнеры | Всё под **docker compose** | Reproducible deploys |

### Search Pipeline (февраль 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **WorkingMemory → ActMem** | Параллельный горутин `GetWorkingMemory`, cosine rerank, soft threshold (relativity−0.10) | `service.go`, `postgres.go` |
| **Real MMR: queryVec relevance** | `querySimCache[i] = cosine(item, queryVec)` — оба терма MMR используют одну метрику | `dedup.go` |
| **Real MMR: Phase 1 по cosine** | Prefill сортирует по `querySimCache`, а не по смешанному `item.Score` | `dedup.go` |
| **MMR lambda 0.8 → 0.7** | 70% relevance / 30% diversity. Configurable через `SearchParams.MMRLambda` | `config.go`, `service.go` |
| **MMR alpha 10.0 → 5.0** | Экспоненциальный penalty стал soft block (был hard block при sim=0.95) | `config.go`, `dedup.go` |
| **HNSW session params** | `hnsw.iterative_scan=relaxed_order`, `ef_search=100` на каждый connection | `postgres.go` |
| **halfvec search queries** | `embedding::halfvec(1024)` — используют HNSW индекс вместо seq scan | `search_queries.go` |

### Add Pipeline + LLM Extraction — Фаза 1 ✅ (февраль 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **Go LLM extractor v2** | Unified extraction+dedup в одном LLM вызове: ADD/UPDATE/DELETE/SKIP + confidence score + valid_at | `internal/llm/extractor.go` |
| **Go native add pipeline** | `POST /product/add` нативно в Go: fast-mode (embed→dedup→insert) + fine-mode (LLM→dedup→insert) | `internal/handlers/add.go`, `add_fast.go`, `add_fine.go` |
| **Batched ONNX embedding** | N фактов за один ONNX inference вызов вместо N последовательных | `add_fine.go`, `embedder/onnx.go` |
| **WorkingMemory VSET hot cache** | Redis 8 VSET для WM dedup candidates: VAdd/VRem/VSim/VRemBatch, Q8+CAS, FILTER by ts | `internal/db/vset.go` |
| **VSET eviction sync** | CleanupWorkingMemoryWithIDs возвращает удалённые ID → VRemBatch | `postgres.go`, `handlers/add.go` |
| **Conflict detection (DELETE action)** | LLM помечает противоречащую память action=delete → soft-invalidate | `extractor.go`, `add_fine.go` |
| **valid_at temporal grounding** | Bi-temporal модель: LLM извлекает когда факт стал правдой, хранится в created_at | `extractor.go`, `add_fine.go` |
| **VSET REDUCE dim 1024→256** | Johnson-Lindenstrauss random projection: 16x vs FP32 (4x REDUCE × 4x Q8). Fallback для существующих ключей без проекции | `internal/db/vset.go` |

### Scheduler Migration — Фаза 3.3 ✅ (февраль 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **Redis Streams Worker** | Go consumer: readLoop + reclaimLoop + processLoop, consumer group `memdb_go_scheduler` | `internal/scheduler/worker.go` |
| **Memory Reorganizer** | Union-Find кластеризация → один LLM вызов на кластер → UpdateMemoryNodeFull + VSET eviction | `internal/scheduler/reorganizer.go` |
| **FindNearDuplicates SQL** | pgvector cosine similarity пары (threshold=0.85), LTM+UserMemory, LIMIT 60 | `db/queries/queries.go`, `postgres.go` |
| **LLM consolidation prompt** | Синтез MemOS+redis/agent-memory-server: third-person, resolve time refs, keep_id+remove_ids+merged_text | `internal/scheduler/prompts.go` |
| **RabbitMQ удалён** | docker-compose.yml: сервис rabbitmq, том rabbitmq_data, dependency и env vars удалены | `krolik-server/docker-compose.yml` |
| **Periodic Reorganizer Timer** | `periodicReorgLoop` (6h) — reorganizer запускается по таймеру для всех активных кубов независимо от stream сообщений. Fallback: scan stream keys если нет VSET ключей | `internal/scheduler/worker.go` |
| **Dead Letter Queue** | `moveToDLQ` — неудачные сообщения пишутся в `scheduler:dlq:v1` (MAXLEN 1000). Inspect: `XRANGE scheduler:dlq:v1 - +` | `internal/scheduler/worker.go` |
| **MemOS memory lifecycle** | `SoftDeleteMerged`: статус `activated → merged` + `merged_into_id` вместо `[deleted:...]` текста. Merged memories остаются для audit/history | `db/queries/queries.go`, `postgres.go`, `scheduler/reorganizer.go` |
| **VSET sliding-window TTL** | `Expire(30d)` после каждого VAdd — активные кубы никогда не истекают, неактивные (30 дней без WM) авто-эвиктируются Redis | `internal/db/vset.go` |
| **Аудит всех 8 Python labels** | Все labels задокументированы в `message.go`; `worker.go` dispatch явно обрабатывает каждый с комментарием о статусе | `internal/scheduler/message.go`, `worker.go` |
| **mem_update handler** | Go-native: embed query → `SearchLTMByVector` → `VAdd` в VSET. Зеркало Python `process_session_turn`. SQL: `SearchLTMByVector` (cosine >= 0.60, top-10) | `reorganizer.go`, `postgres.go`, `queries.go` |
| **query handler** | Pre-emptive WM refresh до Python relay: `query` → `RefreshWorkingMemory` напрямую. VAdd идемпотентен (CAS) — двойной refresh безопасен | `worker.go`, `reorganizer.go` |
| **Worker unit tests** | `TestSplitStreamKey_*`, `TestIndexOf_*`, `TestLabelConstants`, `TestWorkerConstants`, `TestDLQStreamKey` — 10 тестов | `worker_test.go` |
| **VSET Sync (warm-cache)** | `Sync()` на старте: `ListUsers` → `GetRecentWorkingMemory` (top-50 per cube) → `VAdd`. Запускается в goroutine, не блокирует readiness | `db/vset.go`, `db/postgres.go`, `db/queries/queries.go`, `server/server.go` |
| **SearchLTMByVector** | SQL + метод: cosine search по LTM для mem_update handler. Возвращает `embedding::text` — используется как собственный embedding ноды при VAdd (не queryVec) | `db/queries/queries.go`, `db/postgres.go` |
| **mem_feedback handler** | Полный Go-native: `parseFeedbackPayload` → `ProcessFeedback` → `llmAnalyzeFeedback` (keep/update/remove) → `UpdateMemoryNodeFull` + `DeleteByPropertyIDs` + VSET evict. Fallback на `RunTargeted` при LLM error. `FindNearDuplicatesByIDs` SQL для targeted reorg | `worker.go`, `reorganizer.go`, `prompts.go`, `postgres.go`, `queries.go` |
| **mem_read handler** | Go-native: `parseMemReadIDs` (JSON + CSV) → `ProcessRawMemory` → `llmEnhance` (per-node LLM) → embed → `InsertMemoryNodes` LTM → `DeleteByPropertyIDs` WM + VSET evict. Полная замена Python `fine_transfer_simple_mem` | `worker.go`, `reorganizer.go`, `prompts.go` |
| **pref_add handler** | Go-native: `parsePrefConversation` (JSON history|plain text) → `ExtractAndStorePreferences` → `llmExtractPreferences` → embed batch → insert `UserMemory` в Postgres. Без Qdrant — preferences идут в основной vector search pipeline | `worker.go`, `reorganizer.go`, `prompts.go` |
| **Scheduler status endpoints** | Go-native: `GET /product/scheduler/status`, `allstatus`, `task_queue_status`. Запрашивают Redis Streams XINFO GROUPS для `memdb_go_scheduler` consumer group. Fallback to Python proxy если Redis не настроен | `handlers/scheduler.go`, `server/server.go` |
| **ID type audit + fixes** | Полный аудит: все handlers/scheduler/mcptools используют `properties->>'id'` (UUID), не AGE graph ID. Удалены `GetMemoryByID/GetMemoryByIDs`. `GetAllMemories` → `properties->>'id'`. Lookup-запросы оптимизированы: `WHERE id = $1` (PK) вместо JSON extraction | `db/queries/queries.go`, `postgres.go`, `handlers/memory.go`, `mcptools/memory.go` |
| **GetAllMemoriesByTypes** | `NativePostGetMemory` теперь включает `UserMemory` в text_mem (вместе с `LongTermMemory`) — preferences из pref_add корректно отображаются | `db/queries/queries.go`, `postgres.go`, `handlers/memory.go` |
| **UpdateMemoryNodeFull CASE fix** | SQL: `embedding = CASE WHEN $3 = '' THEN embedding ELSE $3::vector(1024) END` — предотвращает ошибку `''::vector(1024)` при вызове с пустым embedding | `db/queries/queries.go` |
| **applyDeleteAction fix** | `DELETE action` теперь делает hard delete через `DeleteByPropertyIDs` вместо text overwrite `[deleted: ...]` с `status=activated`. Противоречивые воспоминания больше не появляются в поиске | `handlers/add_fine.go` |
| **add_fast VSET write** | `nativeFastAddForCube` теперь пишет новые WM ноды в VSET hot cache после вставки — паритет с fine-mode | `handlers/add_fast.go` |
| **SQL PK optimization** | Все WHERE/SELECT/RETURNING используют `id` column (PK) вместо `properties->>'id'` JSON extraction — быстрее при наличии PK index | `db/queries/queries.go` |
| **SearchLTMByVector halfvec** | `SearchLTMByVector` теперь использует `embedding::halfvec(1024) <=> $2::halfvec(1024)` — задействует HNSW halfvec_cosine_ops index вместо seq scan | `db/queries/queries.go` |
| **cacheGet redis.Nil fix** | `cacheGet` в handlers.go: заменено фрагильное `err.Error() != "redis: nil"` на `errors.Is(err, redis.Nil)` — идиоматичная проверка cache miss | `handlers/handlers.go` |
| **ParseVectorString perf** | `ParseVectorString`: заменено `fmt.Sscanf` на `strconv.ParseFloat` — ~3-5x быстрее на hot search path (1024-dim векторы на каждом результате поиска) | `db/postgres.go` |
| **FormatVector perf** | `FormatVector`: заменено `fmt.Fprintf(&b, "%g", v)` на `strconv.AppendFloat` с pre-allocated буфером — устраняет reflection overhead на hot query path | `db/postgres.go` |

### Анализ потерь при удалении Python scheduler

Python `GeneralScheduler` регистрирует 8 handlers. Статус каждого в Go:

| Label | Python делает | Go статус | Риск при удалении Python |
|-------|--------------|-----------|--------------------------|
| `add` | Логирует addMemory/knowledgeBaseUpdate events | ✅ XACK (Go pipeline уже сделал add) | Нет — только event logging теряется |
| `mem_organize` | FindNearDuplicates → merge/archive | ✅ Go Reorganizer | Нет |
| `mem_read` | **Главный pipeline**: raw WM → `fine_transfer_simple_mem` → LTM insert → delete raw WM | ✅ Go-native: `parseMemReadIDs` → `ProcessRawMemory` → `llmEnhance` → `InsertMemoryNodes` → delete WM | Нет |
| `mem_update` | **WorkingMemory refresh** по истории запросов (`process_session_turn`) | ✅ `RefreshWorkingMemory`: embed → `SearchLTMByVector` → VAdd | Нет — Go реализация полная |
| `pref_add` | Preference extraction → Qdrant | ✅ Go-native: `parsePrefConversation` → `ExtractAndStorePreferences` → `UserMemory` в Postgres (without Qdrant dependency) | Нет |
| `query` | Логирует user query → re-submit как `mem_update` | ✅ Pre-emptive `RefreshWorkingMemory` (быстрее Python relay) | Нет |
| `answer` | Логирует assistant answer | ✅ XACK (чистый logging) | Нет — только event log |
| `mem_feedback` | User feedback → add/update memories | ✅ Go-native: `parseFeedbackPayload` → `ProcessFeedback` → LLM keep/update/remove analysis → `UpdateMemoryNodeFull` / `DeleteByPropertyIDs`. Fallback to `RunTargeted` on LLM error | Нет |

**Вывод:** Все 8 Python scheduler labels реализованы в Go нативно (6 полных, 2 XACK-only). Python scheduler можно удалять без потери функциональности.

---

## 1. Текущее состояние инфраструктуры

### Сервисы (docker-compose)

| Сервис | Роль | Порт | Статус |
|--------|------|------|--------|
| **memdb-go** | Go API Gateway: auth, ONNX embedder, search, MCP | 8080 | ✅ Active, Go |
| **memdb-api** | Python backend: add, memory extraction, scheduler | 8000 | ⚠️ Legacy, Python |
| **memdb-mcp** | MCP server (Go) | 8001 | ✅ Active, Go |
| **postgres+AGE** | Graph DB (pgvector + Apache AGE) | 5432 | ✅ Active |
| **qdrant** | Vector store | 6333 | ✅ Active |
| **redis** | Cache + Streams queue | 6379 | ✅ Active |
| ~~**rabbitmq**~~ | ~~Message broker (scheduler)~~ | ~~5672~~ | ❌ Удалён (заменён Redis Streams) |

### Что уже в Go (memdb-go, ~18K строк)

| Компонент | Реализован |
|-----------|-----------|
| Auth middleware (Bearer + X-Service-Secret) | ✅ |
| ONNX embedder (multilingual-e5-large, dim=1024, batched) | ✅ |
| VoyageAI embedder (fallback) | ✅ |
| Search pipeline: embed → Postgres+Qdrant → merge → rerank → dedup | ✅ |
| Text/Skill/Pref/Tool memory types (read) | ✅ |
| Redis cache layer | ✅ |
| MCP tools: search, add, users | ✅ |
| LLM proxy (pass-through) + LLM extractor (unified v2) | ✅ |
| Temporal search / tokenizer (EN+RU) | ✅ |
| REST API (oapi-codegen, OpenAPI spec) | ✅ |
| **Native add pipeline** (fast + fine, LLM extraction+dedup) | ✅ |
| **WorkingMemory VSET hot cache** (Redis 8, Q8+CAS+FILTER) | ✅ |
| **Scheduler Worker** (Redis Streams, XREADGROUP+XAUTOCLAIM) | ✅ |
| **Memory Reorganizer** (FindNearDuplicates → Union-Find → LLM merge) | ✅ |

### Что остаётся в Python (memdb-api)

| Компонент | Приоритет миграции |
|-----------|-------------------|
| **LLM-based memory extraction** (parse messages → structured facts) | ✅ Готово в Go |
| **Add memory pipeline** (dedup check → graph insert → vector insert) | ✅ Готово в Go |
| **Memory reorganizer** (find duplicates, merge/archive) | ✅ Готово в Go |
| **Scheduler** (Redis Streams XREADGROUP consumer) | ✅ Готово в Go, RabbitMQ удалён |
| **Skill memory evolution** (extract skills from conversations) | 🟡 Средний (Фаза 4) |
| **Preference extraction** (implicit + explicit) | 🟡 Средний (Фаза 4) |
| **Chat endpoint** (/product/chat/complete) | 🟡 Средний |
| **Image memory** | 🔵 Низкий — Фаза 6 |
| **Graph traversal** (Apache AGE Cypher queries) | 🟡 Средний |

---

## 2. Анализ конкурентов

### Benchmarks (LOCOMO, февраль 2026)

| Система | Score | Backend | MMR | Search |
|---------|-------|---------|-----|--------|
| **MemOS** | **73.31** | Python | lambda=0.5, no penalty | VEC_COT |
| **mem0** | 66.90 | Python | delegated to vector store | basic |
| LangMem | 58.10 | Python | — | — |
| OpenAI Memory | 52.75 | — | — | — |
| **MemDB** (цель) | **> 75** | **Go** | **Real MMR** | **HNSW+BM25+Graph** |

---

### Глубокий анализ MMR: LangChain vs MemOS vs **MemDB**

> Проведён deep-анализ исходных кодов `langchain-ai/langchain` (`vectorstores/utils.py`) и `MemTensor/MemOS` (`modules/retrieval/rerank/mmr_reranker.py`) — февраль 2026.

#### Сравнительная таблица реализаций

| Фича | LangChain | MemOS | **MemDB (наш)** |
|------|-----------|-------|----------------|
| Relevance term | `cosine(item, query)` ✅ | `cosine(item, query)` ✅ | `querySimCache = cosine(item, query)` ✅ |
| Diversity term | `max cosine(item, selected)` ✅ | `max cosine(item, selected)` ✅ | `max simMatrix[idx][j]` ✅ |
| Обе метрики одинаковы | ✅ | ✅ | ✅ (исправлено: было `item.Score` для relevance) |
| Lambda default | 0.5 (баланс) | 0.5 (баланс) | **0.7** (релевантность > diversity) |
| Lambda configurable | ✅ `fetch_k` param | ✅ `lambda_mult` | ✅ `SearchParams.MMRLambda` |
| Exponential penalty | ❌ | ❌ | ✅ `exp(alpha*(sim−0.9))`, alpha=5.0 |
| Phase 1 prefill | ❌ | ❌ | ✅ top-2 по `querySimCache` |
| Text similarity guard | ❌ | ❌ | ✅ dice+tfidf+bigram (ловит перефразы) |
| Bucket логика | ❌ | ❌ | ✅ text vs preference отдельными квотами |
| NxN матрица | ❌ lazy (O(k·n)) | ❌ lazy | ✅ upfront O(n²) — ok при n≤100 |
| fetch_k концепция | ✅ `fetch_k >> k` | ❌ | ✅ `InflateFactor=5` (TopK*5 из Postgres) |
| WorkingMemory в ActMem | ❌ | ❌ | ✅ параллельный горутин, soft threshold |
| Fulltext+Vector hybrid | ❌ | ❌ | ✅ BM25 + halfvec HNSW merge |
| Graph recall | ❌ | ❌ | ✅ AGE Cypher по ключам и тегам |
| Backend latency | Python ~200ms | Python ~300ms | **Go ~15-30ms embed+search** |

#### Что у нас уникального (нет у конкурентов)

1. **Text similarity guard** (`isTextHighlySimilar`): dice+tfidf+bigram комбинация ловит перефразированные дубли, которые embeddings считают разными (sim=0.85 но содержание то же). LangChain/MemOS это не решают.
2. **Exponential penalty**: мягкий блок для почти-дубликатов (sim > 0.9). Конкуренты просто берут `maxSim` линейно — дубли с sim=0.95 получают недостаточный штраф.
3. **Bucket-aware MMR**: text и preference memory управляются отдельными квотами внутри одного MMR прохода. Конкуренты применяют MMR только к одному типу.
4. **Phase 1 prefill**: детерминированный seed из топ-2 по cosine(item,query) перед MMR итерацией. Гарантирует что самые релевантные попадут, даже если MMR их бы вытеснил из-за diversity.
5. **WorkingMemory (ActMem)**: session context ищется отдельно (recency-ordered), скорится по cosine, возвращается в `act_mem`. Ни LangChain ни MemOS не отделяют working memory.
6. **Go backend**: x5-10 быстрее Python по latency (embed + search + MMR ≈ 15-30ms vs 200-300ms).

#### Что у них есть, что нам ещё нужно

| Пробел | У кого | Наш план |
|--------|--------|----------|
| VEC_COT search | MemOS | Фаза 2.1 — `SearchParams.Mode="smart"` |
| Memory Reorganizer | MemOS | Фаза 3.1 — периодический Go worker |
| LLM entity extraction | mem0 | Фаза 1.1 — Go LLM extractor |
| Dedup-before-insert | mem0 | Фаза 1.2 — top-5 nearest → merge decision |
| Skill memory evolution | MemOS | Фаза 4.1 — experience + examples накопление |
| Sparse vectors (hybrid) | — (Qdrant 1.16+) | Запланировано: Qdrant sparse для preferences |
| Temporal decay | — | Фаза 2.3 — `score *= exp(-α * days)` |

---

### MemTensor/MemOS — что брать

**Уникальные фичи которых нет в MemDB:**

| Фича | Описание | Ценность |
|------|----------|----------|
| **VEC_COT search** | LLM декомпозирует сложный запрос на sub-queries, затем параллельный поиск | Высокая — улучшает релевантность на 15-20% |
| **Memory Reorganizer** | Периодический LLM-job: cosine > 0.8 → detect conflict → merge/archive | Высокая — предотвращает дрейф качества |
| **Skill memory evolution** | Skills накапливают `experience` и `examples` со временем | Средняя |
| **Memory lifecycle** | Generated → Activated → Merged → Archived → Deleted | Средняя — нужна для garbage collection |
| **MemCube cross-sharing** | Управление правами доступа между кубами | Средняя |
| **Параметрическая память** | LoRA-адаптеры из накопленных знаний | Низкая (требует инфра для fine-tuning) |

**Что у нас лучше чем в MemOS:**
- Go backend вместо Python (x5-10 скорость, меньше RAM)
- PostgreSQL+pgvector+AGE вместо Neo4j (лучше для продакшена)
- ONNX локальные эмбеддинги (ноль зависимостей от внешних API)
- SearXNG вместо платного Bocha API
- Реальный BM25/fulltext поиск (tsvector в Postgres)
- **Real MMR** с text similarity guard, bucket logic, phase prefill — превосходит MemOS реализацию

### mem0ai/mem0 — что брать

**Ключевые архитектурные идеи:**

| Фича | Описание | Ценность |
|------|----------|----------|
| **LLM entity extraction** | Перед insert: LLM выделяет (entity, relation, entity) триплеты → graph | Высокая — структурированные связи |
| **Dedup before insert** | LLM сравнивает новую память с топ-K существующих → merge if duplicate | Высокая — качество данных |
| **Unified simple API** | `add/search/get_all/delete` — минимальный интерфейс | Средняя — UX |
| **Multi-level memory** | User / Session / Agent изоляция в одном API | Средняя |
| **Async batch ingestion** | Очередь для тяжёлых операций (extraction + embedding) | Уже есть в MemDB |

**Benchmarks mem0 vs конкуренты (LOCOMO):**
- MemOS: 73.31 overall (лучший)
- mem0: 66.90
- OpenAI Memory: 52.75
- LangMem: 58.10

**Вывод:** MemOS лучше по качеству памяти, mem0 лучше по developer experience. MemDB должен взять лучшее из обоих.

---

### Путь к лидерству (LoCoMo > 75)

```
Текущий оценочный score MemDB: ~68-70
  + VEC_COT search          → +5-7 points  (Фаза 2.1)
  + Memory Reorganizer       → +3-5 points  (Фаза 3)
  + Temporal decay           → +1-2 points  (Фаза 2.3)
  + Dedup-before-insert      → +2-3 points  (Фаза 1)
  + Sparse vector prefences  → +1-2 points
─────────────────────────────────────────
Цель: > 75 (превзойти MemOS 73.31)
```

**Наше уникальное преимущество** которое дадут рейтинг выше всех:
- Go latency (15ms vs 200ms) → можно делать больше поисковых итераций за то же время
- Real MMR с text guard → меньше дублей, выше diversity quality
- WorkingMemory separation → session context не замусоривает LTM результаты
- BM25+HNSW+Graph hybrid → 3 источника vs 1 у конкурентов

---

## 3. Фазовый план Go-миграции

### Принципы
1. **Go-first**: все новые фичи пишутся на Go. Python — только legacy, постепенно удаляется.
2. **Strangler Fig pattern**: каждый компонент переносится по одному, Python удаляется после верификации.
3. **API-compatibility**: Go реализует те же REST endpoints, клиенты не меняются.
4. **Zero-downtime**: миграция через feature flags / dual-write.

---

### Фаза 1 — Go Add Pipeline + LLM Extraction ✅ ЗАВЕРШЕНО

**Цель:** Перенести самый горячий путь — `POST /product/add` — из Python в Go.

**Работы:**

#### 1.1 Go LLM extraction client
```go
// internal/llm/extractor.go
type MemoryExtractor struct {
    client *http.Client  // → cliproxyapi
    model  string
}

// ExtractFacts(messages []Message) → []ExtractedFact
// ExtractSkills(messages []Message) → []Skill
// ExtractPreferences(messages []Message) → []Preference
// CheckDuplicate(newFact string, candidates []string) → (isDup bool, mergeWith string)
```

Промпты берём из MemOS upstream (`src/memdb/memories/*/extract*.py`) + адаптируем под Gemini Flash.

#### 1.2 Go native add handler
```go
// internal/handlers/add.go (уже есть прокси, заменяем на native)
func (h *Handler) AddMemories(w http.ResponseWriter, r *http.Request) {
    // 1. Parse request
    // 2. Embed all texts (параллельно)
    // 3. LLM extract facts/skills/prefs (параллельно)
    // 4. Dedup check against existing (топ-5 nearest neighbors)
    // 5. Graph insert (Postgres+AGE)
    // 6. Vector insert (Qdrant)
    // 7. Redis cache invalidation
}
```

#### 1.3 Apache AGE graph queries в Go
```go
// internal/db/graph.go
type GraphDB struct { *Postgres }

func (g *GraphDB) UpsertMemoryNode(ctx, node MemoryNode) error
func (g *GraphDB) CreateEdge(ctx, from, rel, to string) error
func (g *GraphDB) GetRelated(ctx, nodeID string, depth int) ([]MemoryNode, error)
```

**Метрика успеха:** `POST /product/add` отвечает без вызова Python, latency < 500ms. ✅

> **Дополнительно реализовано:** WorkingMemory VSET hot cache (Redis 8, Q8+CAS), VSET eviction sync при cleanup, batched ONNX inference, bi-temporal valid_at.
>
> **Pending оптимизация:** VSET `REDUCE dim` (1024→256 при VAdd) — Redis 8 поддерживает, даст 4x уменьшение размера VSET. Не критично (Q8 уже даёт 4x vs FP32).

---

### Фаза 2 — VEC_COT Search + Real MMR Dedup (2-3 недели)

**Цель:** Значительно улучшить качество поиска.

#### 2.1 VEC_COT mode
```go
// internal/search/vec_cot.go
// Когда mode="smart":
// 1. LLM декомпозирует запрос: "что я знаю о Ване?" → ["Ваня профессия", "Ваня хобби", "Ваня контакты"]
// 2. Параллельный поиск по каждому sub-query
// 3. Merge и rerank финального списка
type VecCOTSearcher struct {
    extractor *llm.Extractor
    base      *SearchService
}
```

Добавить `mode` параметр в SearchParams: `fast` (текущий), `smart` (VEC_COT), `graph` (traversal).

#### 2.2 Настоящий MMR dedup
```go
// internal/search/dedup.go
// Текущий MMR — фейк (просто текстовый). Реализовать:
// Maximal Marginal Relevance: iteratively select items maximizing
// λ * sim(item, query) - (1-λ) * max(sim(item, selected))
func MMRRerank(candidates []ScoredResult, query []float32, lambda float64, topK int) []ScoredResult
```

#### 2.3 Temporal awareness в поиске
Текущая реализация парсит время в запросе (есть). Добавить:
- Decay function: `score *= exp(-α * days_since_memory)`
- Recency boost для last N days

**Метрика успеха:** LoCoMo benchmark score > 70 (текущий оценочно 65-68).

---

### Фаза 3 — Memory Reorganizer (3-4 недели)

**Цель:** Предотвратить деградацию качества памяти со временем.

#### 3.1 Conflict detector
```go
// internal/reorganizer/detector.go
// Периодический job (каждые N часов):
// 1. Fetch все пары с cosine > threshold (0.8)
// 2. LLM: "эти два факта дублируют/противоречат/дополняют друг друга?"
// 3. Action: merge (обновить + удалить старое), archive, keep
type ConflictDetector struct {
    postgres *db.Postgres
    qdrant   *db.Qdrant
    llm      *llm.Extractor
}
func (c *ConflictDetector) Run(ctx context.Context, userID string) error
```

#### 3.2 Memory lifecycle
```go
// internal/db/lifecycle.go
type MemoryStatus string
const (
    StatusActive   MemoryStatus = "activated"
    StatusMerged   MemoryStatus = "merged"
    StatusArchived MemoryStatus = "archived"
    StatusDeleted  MemoryStatus = "deleted"
)
// Добавить status + archived_at + merged_into_id в схему
```

#### 3.3 Scheduler migration: убрать RabbitMQ ✅ ЗАВЕРШЕНО

~~Сейчас у нас: Redis Streams (enabled в фазе 0) + RabbitMQ (legacy).~~ Реализован Go worker:

- `internal/scheduler/worker.go` — `readLoop` (XREADGROUP) + `reclaimLoop` (XAUTOCLAIM, idle>1h) + `processLoop`
- `internal/scheduler/reorganizer.go` — Union-Find кластеризация пар + LLM consolidate (один вызов на кластер) + soft-delete + VSET eviction
- `internal/scheduler/message.go` — парсинг Redis stream entries в `ScheduleMessage`
- `internal/scheduler/prompts.go` — LLM промпт (синтез MemOS + redis/agent-memory-server)
- Consumer group: `memdb_go_scheduler` — независим от Python's `scheduler_group`, оба работают параллельно
- `krolik-server/docker-compose.yml` — rabbitmq сервис, том и dependency удалены

**Метрика успеха:** ✅ memdb-api scheduler workers переведены в Go, RabbitMQ удалён из docker-compose.

---

### Фаза 3.5 — Memory Quality: конкурентный анализ → реализация ✅/🔄 (февраль 2026)

> Глубокий анализ 5 конкурентов (mem0 47k★, MemOS 5.6k★, redis/agent-memory-server, A-MEM, SimpleMem).
> Выявлены 4 критических gap + 3 важных улучшения. Реализуются в порядке impact/effort.

#### Gap-анализ: что есть у конкурентов, чего нет в MemDB Go

| Gap | Конкурент | Impact | Effort | Статус |
|-----|-----------|--------|--------|--------|
| **Content-hash dedup при insert** | redis/agent-memory-server | 🔴 Высокий | S | ✅ |
| **retrieval_count tracking** | A-MEM | 🟡 Средний | S | ✅ |
| **importance_score + decay** | MemOS + SimpleMem | 🔴 Высокий | M | ✅ |
| **Periodic compaction** (независимо от stream) | redis/agent-memory-server | 🟡 Средний | S | ✅ (periodicReorgLoop) |
| **Graph edges** (memory_edges table + traversal) | mem0 (Neo4j) | 🔴 Высокий | L | ✅ |
| **Contradiction detection** (dup ≠ conflict) | mem0 | 🟡 Средний | M | ✅ |
| **Topic/entity extraction при add** | redis/agent-memory-server | 🟡 Средний | M | 🔄 |

#### 3.5.1 Content-hash dedup при insert ✅

redis/agent-memory-server делает `SHA256(lowercase(text))` перед insert — точные дубликаты
отсекаются без LLM-вызова. В MemDB fast-mode один факт может прийти из нескольких окон → дубли.

```go
// db/queries: INSERT ... WHERE NOT EXISTS (SELECT 1 FROM ... WHERE content_hash = $hash AND user_name = $user)
// add_fast.go + add_fine.go: textHash(text) перед InsertMemoryNodes
// Колонка content_hash TEXT, GIN/hash index по (user_name, content_hash)
```

**Метрика:** LLM-вызовы на exact дубликаты → 0.

#### 3.5.2 retrieval_count tracking ✅

A-MEM инкрементирует `retrieval_count` при каждом retrieve. Часто вспоминаемые факты получают
importance boost → лучше выживают при compaction. Реализация: async goroutine в SearchService после
формирования финального result, без блокировки response.

```go
// db/queries: UPDATE ... SET retrieval_count = retrieval_count + 1, last_retrieved_at = now() WHERE id = ANY($ids)
// search/service.go: go s.postgres.IncrRetrievalCount(bg, ids) после trim
```

#### 3.5.3 importance_score + periodic decay ✅

MemOS хранит `importance_score` (float, default 1.0). При каждом compaction цикле:
`importance_score *= 0.95`. Воспоминания с `importance_score < 0.1` → auto soft-delete.
При retrieval: `+0.15` к score.

```go
// reorganizer: в periodicReorgLoop → DecayImportanceScores(ctx, cubeID) → soft-delete below threshold
// db/queries: UPDATE SET importance_score = importance_score * 0.95 ... WHERE importance_score > 0.1
// add: default importance_score = 1.0 в properties при insert
```

#### 3.5.4 Graph edges (memory_edges table) ✅

Сейчас `GraphRecallByKey` / `GraphRecallByTags` — это поиск по атрибутам нод, а не по рёбрам.
Apache AGE поддерживает настоящий `MATCH (a)-[:RELATED]->(b)`, но рёбра не создаются.

Добавить создание рёбер при `mem_read` (LTM facts → WM source) и при consolidation (merged→keep):

```cypher
-- При consolidation:
MATCH (keep:Memory {id: $keep_id}), (removed:Memory {id: $remove_id})
CREATE (removed)-[:MERGED_INTO {at: $now}]->(keep)

-- При mem_read (WM → LTM transfer):
MATCH (wm:Memory {id: $wm_id}), (ltm:Memory {id: $ltm_id})
CREATE (ltm)-[:EXTRACTED_FROM {at: $now}]->(wm)

-- Graph recall при поиске:
MATCH (seed:Memory {id: ANY($seed_ids)})-[:RELATED|EXTRACTED_FROM*1..2]->(related:Memory)
WHERE related.status = 'activated'
RETURN related
```

#### 3.5.5 Contradiction detection (dup ≠ conflict) ✅

Сейчас consolidation prompt содержит правило "keep most specific" при противоречии.
Нужен отдельный LLM path: если два факта **противоречат** (не дублируют), то:
- NLP: извлечь subj+predicate из обоих
- Если predicate одинаковый, subj одинаковый, но object разный → contradiction
- Action: keep newer + mark older `status=contradicted`

```go
// consolidationResult добавить: "relation": "duplicate"|"contradiction"|"complement"
// В consolidateCluster: при "contradiction" → SoftDeleteContradicted вместо SoftDeleteMerged
// Новый SQL: status = 'contradicted', contradicted_by_id = $keep_id
```

---

### Фаза 4 — Skill Memory + Tool Memory в Go (3-4 недели)

**Цель:** Перенести специализированные типы памяти.

#### 4.1 Skill memory Go handler
```go
// internal/handlers/skills.go
type SkillNode struct {
    ID          string
    Name        string
    Procedure   []string  // шаги
    Experience  []string  // накопленный опыт
    Examples    []string  // примеры применения
    Scripts     []string  // готовые шаблоны
    UsageCount  int
    LastUsedAt  time.Time
}

// SkillMatcher: semantic search по skills + usage frequency boost
func (h *Handler) AddSkill(w, r)
func (h *Handler) SearchSkills(w, r)
func (h *Handler) EvolveSkill(ctx, skillID string, newExperience string) error
```

#### 4.2 Tool memory (agent traces)
```go
// internal/handlers/tool_memory.go
type ToolTrace struct {
    ToolName   string
    Input      map[string]any
    Output     string
    Thought    string
    Success    bool
    DurationMs int
    SessionID  string
}
// Хранить как отдельный тип ноды в графе, искать по tool_name + similarity
```

#### 4.3 Graph-aware preference extraction
Преференции как граф: `User --[PREFERS]--> Topic --[WEIGHT:0.9]--> Subtopic`
Позволяет строить персонализированный профиль пользователя.

---

### Фаза 5 — Python Deprecation (2-3 недели)

**Цель:** Удалить memdb-api из production.

**Checklist перед удалением Python:**
- [x] `GET /product/scheduler/status|allstatus|task_queue_status` — native Go (XINFO GROUPS Redis Streams)
- [ ] Остальные `/product/*` endpoints отвечают из Go (suggestions, scheduler/wait)
- [x] Scheduler workers работают из Go (`memdb_go_scheduler` consumer group)
- [x] `mem_update` нативный Go: embed query → `SearchLTMByVector` → `VAdd` в VSET
- [x] `query` нативный Go: pre-emptive `RefreshWorkingMemory` без Python relay
- [x] VSET warm-cache `Sync()` на старте — первый запрос после перезапуска уже горячий
- [x] `mem_read` Go-native pipeline: `parseMemReadIDs` → `ProcessRawMemory` → `llmEnhance` → embed → insert LTM → delete WM
- [x] `mem_feedback` Go-native: `ProcessFeedback` → LLM keep/update/remove analysis → `UpdateMemoryNodeFull` / delete
- [x] `pref_add` Go-native: `ExtractAndStorePreferences` → LLM → `UserMemory` в Postgres (no Qdrant dep)
- [ ] Memory extraction покрыта тестами (accuracy ≥ Python baseline)
- [ ] `POST /product/add` latency p95 < 1s
- [ ] 2 недели без Python-related ошибок в логах
- [ ] Load test: 50 concurrent adds без degradation

**После:** docker-compose уменьшится с 9 контейнеров (rabbitmq уже удалён) до 6:
postgres, qdrant, redis, memdb-go, go-search, cliproxyapi

---

### Фаза 6 — Image Memory + Multimodal (4-6 недель, после Фазы 5)

**Цель:** Нативная поддержка изображений (из MemOS v2.0).

```go
// internal/handlers/image_memory.go
type ImageMemory struct {
    ID        string
    URL       string      // или base64
    Caption   string      // LLM-generated
    Embedding []float32   // CLIP embedding
    Tags      []string
}
// Требует: CLIP модель (ONNX) для image embeddings
// Storage: Qdrant (image vectors) + Postgres (metadata)
```

---

## 4. Приоритизация vs mem0 concepts

### Взять из mem0:

1. **LLM entity/relation extraction** (Фаза 1) — до insert выделять (subject, predicate, object) триплеты и хранить как AGE edges
2. **Dedup-before-insert** (Фаза 1) — найти top-5 nearest → LLM решает merge/keep/update
3. **`memory.add(messages)` API** (Фаза 1) — упрощённый вход: просто массив сообщений, всё остальное Go решает сам

### Взять из MemOS:

1. **VEC_COT** (Фаза 2) — лучший метод поиска
2. **Reorganizer** (Фаза 3) — долгосрочное качество данных
3. **Skill evolution** (Фаза 4) — уникальная фича

---

## 5. Технические решения

### LLM prompts для extraction (Go templates)
```
// Взять за основу MemOS prompts, адаптировать под Gemini Flash:
// - src/memdb/memories/textual/prompts.py (EN+RU)
// - src/memdb/memories/skill/prompts.py
// Хранить как embedded Go templates в internal/llm/prompts/
```

### Схема AGE графа
```sql
-- Узлы
CREATE (n:MemoryNode {id, type, content, user_id, cube_id, status, created_at, embedding_id})
-- Типы рёбер
HAS_CONTEXT, IS_SKILL_FOR, CONTRADICTS, MERGED_INTO, EVOLVED_FROM, PREFERS
```

### Feature flags для dual-write
```go
// internal/config/flags.go
type FeatureFlags struct {
    NativeAdd     bool // false = proxy to Python, true = Go native
    NativeSkills  bool
    Reorganizer   bool
    VecCOT        bool
}
// Читать из env: MEMDB_FEATURE_NATIVE_ADD=true
```

---

## 6. Метрики успеха по фазам

| Фаза | KPI | Цель |
|------|-----|------|
| 1 | Add latency p95 | < 500ms (vs ~2s Python) |
| 1 | Memory extraction accuracy | ≥ Python baseline |
| 2 | Search quality (LoCoMo) | > 70 |
| 3 | Memory freshness (% stale) | < 5% |
| 4 | Skill reuse rate | > 30% |
| 5 | Python containers | 0 |
| 6 | Multimodal queries | Image + text co-retrieval |

---

## 7. Что НЕ делаем

- ❌ Параметрическая память (LoRA) — требует GPU и fine-tuning инфра, ROI низкий
- ❌ Активационная память (KV-cache) — требует специфичного LLM deployment
- ❌ Миграция на Neo4j — у нас AGE лучше интегрирован с Postgres ecosystem
- ❌ Milvus — у нас Qdrant, он лучше для self-hosted
