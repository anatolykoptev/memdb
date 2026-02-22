# Сравнение с конкурентами

> Честный анализ возможностей memdb-go относительно конкурентов.
> Обновлено: февраль 2026 (после P0+P1+P2: entity graph, OllamaEmbedder, EmbedQuery, retry, factory).

## Конкуренты

| Проект | Репозиторий | Язык | Stars | Тип |
|---|---|---|---|---|
| **mem0** | [mem0ai/mem0](https://github.com/mem0ai/mem0) | Python | ~50k | Open source + Managed cloud |
| **MemOS** | [MemTensor/MemOS](https://github.com/MemTensor/MemOS) | Python | ~20k | Open source (research) |
| **Graphiti/Zep** | [getzep/graphiti](https://github.com/getzep/graphiti) | Python (Cloud: Go) | ~10k | Open source + Managed cloud |
| **LangMem** | [langchain-ai/langmem](https://github.com/langchain-ai/langmem) | Python | ~3k | Библиотека (LangChain) |
| **Redis AMS** | [redis/agent-memory-server](https://github.com/redis/agent-memory-server) | Python | ~2k | Open source |
| **Memobase** | — | Python | ~1k | Open source |
| **memdb-go** | — | **Go** | — | Self-hosted |

---

## Сводная таблица возможностей

**Легенда:** ✅ Есть | ❌ Нет | ⚠️ Частично / ограниченно

### Runtime

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Язык сервера | Python | Python | Python | Library | Python | Python | **Go** |
| Нет GIL / True parallelism | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Goroutine-per-request (8KB stack) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Startup < 1s | ❌ | ❌ | ❌ | — | ❌ | ❌ | ✅ |

### Embedding

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Локальный inference (без сети) | ⚠️ sentence-tr. | ⚠️ | ❌ | ❌ | ❌ | ❌ | ✅ ONNX RT |
| 15+ API провайдеров | ✅ | ❌ | ❌ | ⚠️ | ❌ | ❌ | ❌ |
| Ollama (local LLM embeddings) | ✅ | ✅ | ❌ | ✅ | ❌ | ❌ | ✅ |
| Batched inference (1 forward pass) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| EmbedQuery (query vs doc prefix) | ✅ | ❌ | ✅ | ❌ | ✅ | ❌ | ✅ |
| Retry с backoff (429/503) | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| Auto-detect embedding dim | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| Factory / pluggable backend | ✅ | ❌ | ✅ | ✅ | ✅ | ❌ | ✅ |

### Add / Extraction Pipeline

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| LLM-based extraction | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |
| Unified extract+dedup (1 LLM вызов) | ❌ 2 вызова | ❌ 2 вызова | ❌ | ❌ | — | — | ✅ |
| Confidence score per fact | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| ADD / UPDATE / DELETE actions | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |
| Bi-temporal modeling (valid_at + invalid_at) | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ |
| Tags per extracted fact | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Content-hash dedup (SHA-256) | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| Fast mode (без LLM) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| WM auto-summarization + token budget | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |

### Search Pipeline

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Vector search | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Full-text search (BM25 / tsquery) | ❌ | ✅ BM25 | ✅ | ❌ | ❌ | ❌ | ✅ tsquery |
| Graph BFS traversal | ✅ Neo4j | ✅ | ✅ Neo4j | ❌ | ❌ | ❌ | ✅ AGE |
| Entity-graph поиск (triplets) | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ entity_nodes+edges |
| Community summaries | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ |
| Параллельный поиск (errgroup) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Temporal cutoff filter | ❌ | ❌ | ⚠️ | ❌ | ❌ | ❌ | ✅ |
| Temporal decay (importance score) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ exp(-α·t) |
| LLM rerank | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Iterative expansion (multi-stage) | ❌ | ✅ Advanced | ❌ | ❌ | ❌ | ❌ | ✅ port |
| Dedup в выдаче (sim / mmr) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| User profile injection | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ |
| WorkingMemory отдельным слоем | ❌ | ⚠️ | ❌ | ❌ | ✅ | ❌ | ✅ |
| VSET hot cache (1–5ms WM поиск) | ❌ | ❌ | ❌ | ❌ | ⚠️ only | ❌ | ✅ Redis 8+ |
| HTTP response cache | ❌ | ❌ | ❌ | — | ❌ | ❌ | ✅ Redis 30s |

### Scheduler / Background Processing

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Фоновая обработка | Thread pool | Redis Streams | asyncio | — | asyncio | ❌ | ✅ Redis Streams |
| At-least-once delivery (XACK) | ❌ | ✅ | ❌ | — | ❌ | ❌ | ✅ |
| Crash recovery (XAUTOCLAIM) | ❌ | ❌ | ❌ | — | ❌ | ❌ | ✅ |
| Dead Letter Queue | ❌ | ❌ | ❌ | — | ❌ | ❌ | ✅ |
| Retry с exponential backoff | ❌ | ✅ | ❌ | — | ❌ | ❌ | ✅ ZSet backoff |
| Priority queue задач | ❌ | ✅ | ❌ | — | ❌ | ❌ | ❌ |
| Near-duplicate merge (LLM) | ❌ | ❌ | ⚠️ | ❌ | ❌ | ❌ | ✅ Union-Find |
| Importance decay + archiving | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Periodic reorg (все кубы каждые 6h) | ❌ | ❌ | ❌ | — | ❌ | ❌ | ✅ |
| Predictive preload (Next-Scene) | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |

### Database

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Pluggable vector store (10+ backends) | ✅ | ❌ | ❌ | ✅ BaseStore | ❌ | ❌ | ❌ |
| Entity linking / identity resolution | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ cosine HNSW |
| Cube versioning / snapshot | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |

### Интеграции

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| MCP server | ✅ OpenMemory | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| `get_context` (profile+mem в 1 вызов) | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| Procedural memory (edit system prompt) | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| SSE / WebSocket streaming events | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| OpenTelemetry tracing | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Rate limiting (per-IP token bucket) | ❌ | ❌ | ❌ | — | ❌ | ❌ | ✅ |
| Python proxy fallback | — | — | — | — | — | — | ✅ |
| OpenAPI / Swagger (auto-gen) | ✅ FastAPI | ✅ FastAPI | ✅ FastAPI | — | ✅ FastAPI | ✅ | ✅ embedded |
| JWT auth (user-scoped tokens) | ❌ | ❌ | ✅ Zep | — | ⚠️ | ❌ | ❌ |

### Деплой

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | Memobase | memdb-go |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Self-hosted | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Managed cloud | ✅ SOC2 | ❌ | ✅ Zep | ❌ | ❌ | ❌ | ❌ |
| Статический Go binary | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |

---

## Где мы реально впереди

1. **Go runtime, нет GIL** — единственный production memory сервер на Go. При 1000 concurrent requests: ~8MB RAM vs ~500MB у Python конкурентов.
2. **Unified LLM extract+dedup (1 вызов)** — все конкуренты делают минимум 2 LLM round-trips на каждый add. Мы экономим ~500–1500ms latency.
3. **ONNX in-process embedding** — нет сетевого latency, нет per-token cost, нет rate limits. Redis AMS платит $$ за каждый OpenAI embed call.
4. **Temporal decay + archiving** — уникально. Никто не реализует exponential importance decay с auto-archiving.
5. **VSET two-tier search** — WorkingMemory в Redis HNSW (1–5ms) + pgvector LTM (50ms). Уникальная комбинация.
6. **XAUTOCLAIM crash recovery** — production-grade гарантия at-least-once без потери задач при краше. Только у нас и частично у MemOS.
7. **User profile injection** — только у нас (и у Memobase как источника идеи).
8. **Bi-temporal valid_at + invalid_at** — только у нас и Graphiti. Рёбра никогда не удаляются физически, `invalid_at` фиксирует конец периода валидности — полный аудит истории.
9. **Entity knowledge graph** — `entity_nodes` + `entity_edges` (triplets) + MENTIONS_ENTITY + CONTRADICTS penalty в recall + embedding identity resolution. На уровне Graphiti/mem0.
10. **Embedding-based identity resolution** — `UpsertEntityNodeWithEmbedding` мерджит "Яндекс" и "Yandex" через cosine similarity (threshold 0.92) без LLM-вызова.
11. **Python proxy fallback** — постепенная миграция без big-bang rewrite. Уникально.
12. **Apache AGE в PostgreSQL** — граф + SQL + vector в одном сервисе без отдельного Neo4j.

---

## Что нужно допилить, чтобы догнать конкурентов

### ✅ Реализовано (P0+P1, февраль 2026)

- **Entity triplet extraction** — `llm/extractor.go`: `EntityRelation`, `entities` + `relations` в промпте (up to 5 entities, up to 3 relations)
- **entity_nodes + entity_edges** — таблицы в PostgreSQL, HNSW embedding index, cosine identity resolution (threshold 0.92)
- **MENTIONS_ENTITY рёбра + entity graph recall** — `linkEntitiesAsync`, `GetMemoriesByEntityIDs` в `search/service.go`
- **CONTRADICTS penalty** — `PenalizeContradicts` в `search/merge.go` (-0.30 от score)
- **Bi-temporal invalidation (invalid_at)** — `InvalidateEdgesByMemoryID` + `InvalidateEntityEdgesByMemoryID`, вызывается при DELETE/UPDATE
- **valid_at на рёбрах** — `memory_edges.valid_at` из `ExtractedFact.ValidAt`
- **OllamaEmbedder** — `embedder/ollama.go`: HTTP-клиент к Ollama `/api/embed`, batch поддержка, нулевые зависимости (нет CGO/ONNX). Env: `MEMDB_EMBEDDER_TYPE=ollama`
- **OpenAPI / Swagger UI** — `internal/server/openapi.go`: OpenAPI 3.1 spec встроен через `embed.FS`, Swagger UI на `/docs` (CDN), spec на `/openapi.json`. Нулевые зависимости.
- **EmbedQuery в интерфейсе** — `embedder/embedder.go`: отдельный метод для поисковых запросов с query-специфичным префиксом (`WithQueryPrefix`). `search/service.go` использует `EmbedQuery`.
- **Retry с exponential backoff (embedder)** — `embedder/retry.go`: `withRetry[T]` для Ollama и Voyage (429/503/504, 3 попытки, 200ms→5s).
- **Auto-detect dim** — `OllamaClient.Dimension()` возвращает реальную размерность из первого ответа модели.
- **Factory pattern** — `embedder/factory.go`: `embedder.New(cfg, logger)` + типизированный `embedder.Config`. `server.go` больше не содержит switch-case по типу embedder'а.
- **Retry с exponential backoff (scheduler)** — `scheduler/worker_retry.go`: Redis Sorted Set `scheduler:retry:v1` для отложенных повторов. Backoff: 5s→10s→20s→DLQ (max 3 попытки). `retryLoop` опрашивает ZSet каждые 5s. XACK только для оригинальных stream-сообщений.
- **WorkingMemory auto-compaction** — `scheduler/reorganizer_wm_compact.go`: при ≥ 50 WM-нод LLM суммаризирует старые → `EpisodicMemory` LTM нод (searchable), 10 последних нод сохраняются. Запускается в `periodicReorgLoop`. `EpisodicMemory` включён в `SearchLTMByVector` и `FindNearDuplicates`.
- **Priority queue (high/low channels)** — `scheduler/worker_priority.go`: два канала `highMsgCh`/`lowMsgCh`. HIGH: `mem_update`, `query`, `mem_feedback` — user-triggered. LOW: `mem_organize`, `mem_read`, `pref_add`, `add`, `answer` — фоновые. `processLoop` всегда дренирует `highMsgCh` первым. Retry-сообщения сохраняют приоритет.
- **SSE streaming (RFC 8895)** — `rpc/sse.go`: `SSEWriter` с полями `id/event/data/retry`, `bufio.Scanner`-based proxy (SSE-safe, без разрыва полей), отдельный `sseClient` без Timeout, `X-Accel-Buffering: no`. `ProxyLLMComplete` поддерживает `stream:true` (OpenAI streaming). Context-aware — стоп при disconnect клиента.

---

### 🟡 Важно — значимые пробелы в функциональности

#### 3. Community detection + cross-session summaries
**Кто имеет:** Graphiti/Zep
**Что это:** Периодически кластеризовать связанные воспоминания в "сообщества" и генерировать их суммари. Позволяет отвечать на абстрактные вопросы без точного vector match.
**Что сделать:**
- В `scheduler/reorganizer.go`: после Union-Find merge — если кластер > 3 узлов → LLM summary → `CommunityMemory` тип
- В `search/service.go`: включить `CommunityMemory` в `TextScopes`

#### 4. `get_context` MCP tool
**Кто имеет:** Redis Agent Memory Server (`memory_prompt`)
**Что это:** Один MCP вызов возвращает готовый контекстный блок: profile + act_mem + top search results как строку для вставки в system prompt. Агент не собирает контекст вручную.
**Что сделать:**
- В `mcptools/`: новый tool `get_context` — вызывает `SearchService.Search` + `Profiler.GetProfile` → форматирует в единый текстовый блок

---

### 🟢 Желательно — улучшают DX и enterprise-ready

#### 6. Pluggable VectorStore interface
**Кто имеет:** mem0 (10+ backends), LangMem (BaseStore)
**Что это:** Позволяет использовать любой vector store без переписывания SearchService.
**Что сделать:**
```go
type VectorStore interface {
    VectorSearch(ctx context.Context, vec []float32, user string, types []string, agentID string, topK int) ([]VectorSearchResult, error)
    InsertMemoryNodes(ctx context.Context, nodes []MemoryInsertNode) error
}
```
Первая реализация — текущий `db.Postgres`. Вторая — Qdrant как standalone.

#### 7. Procedural memory (update system prompt)
**Кто имеет:** LangMem
**Что это:** LLM анализирует feedback агента и редактирует system prompt — агент обучается новому поведению без fine-tuning.
**Что сделать:**
- Новый memory type `ProcedureMemory` в Postgres
- MCP tool `update_system_prompt` — принимает instruction → сохраняет в `ProcedureMemory`
- В `SearchService.Search`: читать `ProcedureMemory` и инжектировать в ответ

#### 8. SSE streaming memory events
**Кто имеет:** MemOS
**Что это:** Клиент подписывается на `GET /product/memory/events` и получает real-time обновления при add/update/delete.
**Что сделать:**
- ✅

#### 9. JWT auth (user-scoped tokens)
**Кто имеет:** Zep, частично Redis AMS
**Что это:** Выдача user-scoped JWT с `user_id` claim. Позволяет браузерным клиентам обращаться к API без service secret.
**Что сделать:**
- ✅

#### 10. Go embedded SDK (zero-latency)
**Что это:** `go get memdb.io/sdk` — встраиваемый клиент с SQLite + pgvector для Go-агентов без HTTP round-trip. LangMem-style zero-latency.
**Что сделать:**
- `sdk/` директория с in-process SearchService (subset функций)
- SQLite + `sqlite-vec` extension как lightweight бэкенд
- Приоритет: **низкий** (долгосрочно)

---

## Итог: приоритизированный backlog

| # | Задача | Файл | Конкурент догоняем |
|---|---|---|---|
| ✅ | Entity triplet extraction + knowledge graph | реализовано | mem0, Graphiti |
| ✅ | Bi-temporal invalid_at на рёбрах | реализовано | Graphiti |
| ✅ | Embedding identity resolution | реализовано | Graphiti |
| ✅ | OllamaEmbedder (batch HTTP, без CGO) | `embedder/ollama.go` | mem0, MemOS |
| ✅ | OpenAPI 3.1 + Swagger UI (`/docs`) | `server/openapi.go` | Все Python |
| ✅ | EmbedQuery + WithQueryPrefix | `embedder/embedder.go` | Redis AMS, mem0 |
| ✅ | Retry с backoff в embedder (Ollama+Voyage) | `embedder/retry.go` | mem0, Redis AMS |
| ✅ | Auto-detect dim + Factory pattern | `embedder/factory.go` | mem0, Graphiti |
| ✅ | Retry + backoff в scheduler (ZSet, 5s→10s→20s→DLQ) | `scheduler/worker_retry.go` | MemOS |
| ✅ | WM compaction: count-based (50 нод) → EpisodicMemory LTM | `scheduler/reorganizer_wm_compact.go` | Redis AMS |
| ✅ | Priority queue: highMsgCh/lowMsgCh, priority select | `scheduler/worker_priority.go` | MemOS |
| ✅ | SSE streaming: SSEWriter, bufio.Scanner proxy, stream:true | `rpc/sse.go` | MemOS |
| 2 | Community detection + summaries | `scheduler/reorganizer.go` | Graphiti |
| 4 | `get_context` MCP tool | `mcptools/context.go` | Redis AMS |
| 5 | VectorStore interface | `db/`, `search/service.go` | mem0, LangMem |
| 6 | Procedural memory | `llm/`, `mcptools/` | LangMem |
| 7 | JWT auth | `middleware/auth.go` | Zep |
| 9 | Go embedded SDK | `sdk/` | LangMem |
