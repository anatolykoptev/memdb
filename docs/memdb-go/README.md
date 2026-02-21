# memdb-go — Документация

Go-реализация MemDB сервера. Является HTTP-шлюзом поверх Python-бэкенда с постепенным переносом функциональности на нативный Go.

## Структура модулей

| Пакет | Путь | Назначение |
|---|---|---|
| `server` | `internal/server/` | HTTP-сервер, маршруты, middleware |
| `handlers` | `internal/handlers/` | HTTP-обработчики (add, search, memory CRUD, users) |
| `search` | `internal/search/` | Унифицированный поиск с dedup, rerank, decay |
| `embedder` | `internal/embedder/` | Бэкенды векторизации (ONNX, VoyageAI) |
| `llm` | `internal/llm/` | LLM-извлечение фактов и дедупликация |
| `scheduler` | `internal/scheduler/` | Фоновый воркер Redis Streams + реорганизатор |
| `db` | `internal/db/` | Клиенты БД: Postgres (pgvector/AGE), Qdrant, Redis, VSET |
| `mcptools` | `internal/mcptools/` | MCP-сервер (Model Context Protocol) |
| `config` | `internal/config/` | Конфигурация из env-переменных |
| `cache` | `internal/cache/` | HTTP-кэш middleware (Redis) |
| `rpc` | `internal/rpc/` | Прокси-клиент к Python-бэкенду |

## Архитектура

```
Client / MCP
     │
     ▼
[memdb-go HTTP Server :8000]
     │   Middleware: RequestID → Recovery → Logging → CORS → Auth → RateLimit → OTel → Cache
     │
     ├─── Native handlers ──── Postgres (pgvector + AGE graph) + ONNX/Voyage
     │         │                     │
     │    add_fast.go           VectorSearch
     │    add_fine.go           FulltextSearch
     │    search.go             GraphRecall + BFS
     │    memory_*.go           WorkingMemory VSET
     │
     └─── Proxy fallback ──── [Python backend]
               │
          rpc.PythonClient
```

## Документация по модулям

- [server.md](server.md) — HTTP-сервер, маршруты, middleware стек, конфигурация
- [handlers.md](handlers.md) — HTTP-обработчики (add, search, get, delete)
- [search.md](search.md) — Поисковый pipeline (embed → recall → rerank → dedup → decay)
- [embedder.md](embedder.md) — Бэкенды векторизации (ONNX, VoyageAI)
- [llm.md](llm.md) — LLM-экстрактор фактов (fine-mode add)
- [scheduler.md](scheduler.md) — Фоновый воркер и реорганизатор памяти
- [db.md](db.md) — Слой базы данных (Postgres, Qdrant, Redis, VSET)
- [mcp.md](mcp.md) — MCP-сервер (интеграция с AI-агентами)

---

## Сравнение с конкурентами

### Конкуренты

| Проект | Язык | Stars | Тип |
|---|---|---|---|
| **mem0** (mem0ai/mem0) | Python | ~50k | Open source + Managed cloud |
| **MemOS** (MemTensor/MemOS) | Python | ~20k | Open source (research) |
| **Graphiti/Zep** (getzep/graphiti) | Python (Cloud: Go) | ~10k | Open source + Managed cloud |
| **LangMem** (langchain-ai/langmem) | Python | ~3k | Library (LangChain ecosystem) |
| **Redis Agent Memory** (redis/agent-memory-server) | Python | ~2k | Open source |
| **Memobase** | Python | ~1k | Open source |
| **memdb-go** | **Go** | — | Self-hosted |

---

### Сводная таблица

| Фича | mem0 | MemOS | Graphiti | LangMem | Redis AMS | memdb-go |
|---|---|---|---|---|---|---|
| **Язык** | Python | Python | Python | Python | Python | **Go** |
| **GIL** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ Нет |
| **Локальный embedding** | sentence-transformers | ✅ | Нет | Нет | Нет | **ONNX Runtime** |
| **Unified extract+dedup LLM** | ❌ (2 вызова) | ❌ | ❌ | ❌ | ❌ | ✅ **1 вызов** |
| **Temporal decay** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ exp(-α·days) |
| **Iterative retrieval** | ❌ | ✅ AdvancedSearcher | ❌ | ❌ | ❌ | ✅ Port |
| **Graph traversal** | ✅ Neo4j | ✅ Tree+Graph | ✅ Neo4j | ❌ | ❌ | ✅ AGE BFS |
| **Entity graph** | ✅ Triplets | ✅ | ✅ Temporal KG | ❌ | ❌ | ✅ KG+bi-temporal |
| **Community summaries** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| **VSET hot cache** | ❌ | ❌ | ❌ | ❌ | ⚠️ (основной store) | ✅ **Redis 8+** |
| **At-least-once scheduler** | ❌ | ✅ Redis Streams | ❌ | ❌ | ❌ | ✅ XAUTOCLAIM |
| **WM auto-summarization** | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ |
| **Procedural memory** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| **User profile injection** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ Memobase-style |
| **Pluggable vector store** | ✅ 10+ | ❌ | ❌ | ✅ BaseStore | ❌ | ❌ |
| **MCP server** | ✅ OpenMemory | ❌ | ❌ | ❌ | ✅ | ✅ |
| **OpenTelemetry** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **HTTP response cache** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ Redis 30s |
| **Python proxy fallback** | — | — | — | — | — | ✅ |
| **Self-hosted** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Managed cloud** | ✅ | ❌ | ✅ Zep | ❌ | ❌ | ❌ |

---

### Где мы впереди

1. **Go runtime** — нет GIL, goroutine-based parallelism, 8KB stack vs 50MB Python process
2. **ONNX local embedding + Ollama** — нет сетевого latency, нет per-token cost; Ollama даёт любую HuggingFace модель без CGO; `EmbedQuery` с раздельными query/doc префиксами; retry с backoff; auto-detect dim
3. **Unified LLM вызов** — ExtractAndDedup в одном round-trip (vs 2 у всех конкурентов)
4. **Temporal decay** — единственные кто реализует exp decay с importance archiving
5. **VSET two-tier** — WorkingMemory в Redis HNSW (1–5ms) + pgvector LTM (50ms)
6. **Python proxy fallback** — постепенная миграция без big-bang rewrite
7. **Apache AGE в PostgreSQL** — граф + SQL + vector в одном сервисе
8. **Entity knowledge graph** — `entity_nodes` + `entity_edges` + embedding identity resolution (cosine 0.92) + CONTRADICTS penalty. На уровне Graphiti/mem0.
9. **Bi-temporal valid_at + invalid_at** — рёбра никогда не удаляются, `invalid_at` фиксирует конец валидности. Только у нас и Graphiti.
10. **OpenAPI 3.1 + Swagger UI** — `/openapi.json` + `/docs` встроены в бинарь через `embed.FS`, нулевые зависимости.

### Где отстаём (цели Go-миграции)

| Приоритет | Фича | Конкурент | Файл для реализации |
|---|---|---|---|
| ✅ | Entity triplet extraction + knowledge graph | mem0, Graphiti | реализовано |
| ✅ | Bi-temporal invalid_at + identity resolution | Graphiti | реализовано |
| ✅ | OllamaEmbedder (без CGO/ONNX, batch HTTP) | mem0, MemOS | `embedder/ollama.go` |
| ✅ | OpenAPI 3.1 + Swagger UI (`/docs`, `/openapi.json`) | mem0 (FastAPI) | `server/openapi.go` |
| ✅ | EmbedQuery + WithQueryPrefix (query vs doc prefix) | Redis AMS, mem0 | `embedder/embedder.go` |
| ✅ | Retry с exponential backoff (Ollama + Voyage) | mem0, Redis AMS | `embedder/retry.go` |
| ✅ | Auto-detect dim из ответа модели | mem0, Redis AMS | `embedder/ollama.go` |
| ✅ | Factory pattern `embedder.New(cfg)` | mem0, Graphiti | `embedder/factory.go` |
| 🔴 Высокий | LLM provider interface (не только CLIProxyAPI) | mem0 (litellm) | `llm/extractor.go` |
| 🟡 Средний | WM auto-summarization + token budget | Redis AMS | `handlers/add.go`, `scheduler/worker.go` |
| 🟡 Средний | Retry с exponential backoff в scheduler | MemOS | `scheduler/worker.go` |
| 🟡 Средний | Community detection + summaries | Graphiti | `scheduler/reorganizer.go` |
| 🟡 Средний | SSE streaming memory events | MemOS | `server/server.go` |
| 🟢 Низкий | JWT-based user auth | Redis AMS | `middleware/auth.go` |
| 🟢 Низкий | Procedural memory (system prompt update) | LangMem | `llm/`, `handlers/` |
| 🟢 Низкий | API versioning `/v1/` | MemOS | `server/server.go` |
| 🟢 Низкий | Go embedded SDK (zero-latency) | LangMem | `sdk/` (новый cmd) |

---

## Быстрый старт

```bash
# Сборка
cd memdb-go
go build ./cmd/server

# Запуск (без ONNX)
PYTHON_BACKEND_URL=http://localhost:8080 \
POSTGRES_URL=postgres://... \
./memdb-go

# Запуск с ONNX embedder
ONNX_MODEL_DIR=/path/to/multilingual-e5-large \
./memdb-go
```

## Переменные окружения

| Переменная | По умолчанию | Описание |
|---|---|---|
| `PYTHON_BACKEND_URL` | `http://localhost:8080` | URL Python-бэкенда для proxy fallback |
| `POSTGRES_URL` | — | PostgreSQL (PolarDB) connection string |
| `QDRANT_ADDR` | — | Qdrant gRPC адрес (`host:port`) |
| `DB_REDIS_URL` | — | Redis URL для VSET и планировщика |
| `REDIS_URL` | — | Redis URL для HTTP-кэша middleware |
| `ONNX_MODEL_DIR` | — | Путь к директории с `model_quantized.onnx` и `tokenizer.json` |
| `EMBEDDER_TYPE` | `onnx` | Тип embedder: `onnx` или `voyage` |
| `VOYAGE_API_KEY` | — | API-ключ VoyageAI (если `EMBEDDER_TYPE=voyage`) |
| `LLM_PROXY_URL` | — | URL CLIProxyAPI (OpenAI-совместимый) для LLM-вызовов |
| `LLM_PROXY_API_KEY` | — | API-ключ CLIProxyAPI |
| `LLM_DEFAULT_MODEL` | `gemini-2.0-flash-lite` | Модель для rerank/profiler/iterative |
| `LLM_EXTRACT_MODEL` | `gemini-2.0-flash-lite` | Модель для извлечения фактов (fine-mode) |
| `PORT` | `8000` | Порт HTTP-сервера |
| `AUTH_ENABLED` | `false` | Включить Bearer-auth |
| `RATE_LIMIT_ENABLED` | `false` | Включить rate limiting |
