# Server — HTTP-сервер и Middleware

Пакет `internal/server/` инициализирует HTTP-сервер, регистрирует все маршруты и применяет middleware стек.

---

## Инициализация сервера (`server.go`)

```go
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*http.Server, func())
```

Возвращает готовый `*http.Server` и `cleanup()` функцию для корректного завершения.

### Порядок инициализации

1. **Redis cache client** (`cache.New`) — для HTTP-кэш middleware
2. **Python proxy client** (`rpc.NewPythonClient`) — для fallback к Python
3. **Handler** (`handlers.NewHandler`) — центральный объект обработчиков
4. **DB clients** (`initDBClients`):
   - Postgres, Qdrant, Redis (все опциональны, ошибки не фатальны)
   - WorkingMemory VSET cache (`db.NewWorkingMemoryCache`)
   - Warm-up VSET из Postgres в background goroutine
5. **Embedder** — ONNX или VoyageAI (по `MEMDB_EMBEDDER_TYPE`)
6. **SearchService** (`search.NewSearchService`) + опциональные компоненты:
   - LLMReranker (если `LLM_PROXY_URL` задан)
   - Iterative retrieval (`NumStages=2`)
   - Profiler (если Redis + LLM доступны)
7. **LLM Extractor** для fine-mode add
8. **Scheduler Worker** в background goroutine (если Redis доступен)
9. **HTTP router** — `http.ServeMux` (Go 1.22+)
10. **Middleware стек**

---

## Маршруты

### Health

| Method | Path | Handler |
|---|---|---|
| GET | `/health` | `Health` — всегда 200 OK |
| GET | `/ready` | `ReadinessCheck` — проверяет все зависимости |

### Embeddings

| Method | Path | Handler |
|---|---|---|
| POST | `/v1/embeddings` | `OpenAIEmbeddings` — OpenAI-совместимый endpoint |

### Memory CRUD

| Method | Path | Handler | Режим |
|---|---|---|---|
| POST | `/product/get_all` | `NativeGetAll` | Native |
| POST | `/product/add` | `NativeAdd` | Native / Proxy |
| POST | `/product/search` | `NativeSearch` | Native / Proxy |
| POST | `/product/get_memory` | `NativePostGetMemory` | Native / Proxy |
| GET | `/product/get_memory/{memory_id}` | `NativeGetMemory` | Native / Proxy |
| POST | `/product/get_memory_by_ids` | `NativeGetMemoryByIDs` | Native / Proxy |
| POST | `/product/delete_memory` | `NativeDelete` | Native / Proxy |

### Chat

| Method | Path | Handler |
|---|---|---|
| POST | `/product/chat/complete` | `ValidatedChatComplete` |
| POST | `/product/chat/stream` | `ValidatedChatStream` |
| POST | `/product/chat/stream/playground` | `ProxyToProduct` |
| POST | `/product/chat` | `ValidatedChatStream` (product_router variant) |
| POST | `/product/llm/complete` | `ProxyLLMComplete` — direct CLIProxyAPI |

### Users & Config

| Method | Path | Handler |
|---|---|---|
| POST | `/product/users/register` | `NativeRegisterUser` |
| GET | `/product/users` | `NativeListUsers` |
| GET | `/product/users/{user_id}` | `NativeGetUser` |
| GET | `/product/users/{user_id}/config` | `NativeGetUserConfig` |
| PUT | `/product/users/{user_id}/config` | `NativeUpdateUserConfig` |
| POST | `/product/configure` | `NativeConfigure` |
| GET | `/product/configure/{user_id}` | `NativeGetConfig` |

### Scheduler monitoring

| Method | Path | Handler |
|---|---|---|
| GET | `/product/scheduler/allstatus` | `NativeSchedulerAllStatus` |
| GET | `/product/scheduler/status` | `NativeSchedulerStatus` |
| GET | `/product/scheduler/task_queue_status` | `NativeSchedulerTaskQueueStatus` |

### Instance monitoring

| Method | Path | Handler |
|---|---|---|
| GET | `/product/instances/status` | `NativeInstancesStatus` |
| GET | `/product/instances/count` | `NativeInstancesCount` |

### Internal

| Method | Path | Handler |
|---|---|---|
| POST | `/product/exist_mem_cube_id` | `NativeExistMemCube` |
| POST | `/product/get_user_names_by_memory_ids` | `NativeGetUserNamesByMemoryIDs` |
| POST | `/product/feedback` | `ValidatedFeedback` |
| POST | `/product/suggestions` | `ProxyToProduct` |
| GET | `/product/suggestions/{user_id}` | `ProxyToProduct` |

---

## Middleware стек

Применяется в порядке (внешний → внутренний):

```
RequestID → Recovery → Logging → CORS → Auth → RateLimit → OTel → Cache → Handler
```

### RequestID (`request_id.go`)

Генерирует уникальный `X-Request-ID` для каждого запроса (UUID v4) если не передан клиентом. Добавляет в context и response headers.

### Recovery (`recovery.go`)

`recover()` panic'ов. Логирует stack trace, возвращает `500 Internal Server Error`. Предотвращает падение всего сервера при панике в обработчике.

### Logging (`logging.go`)

Структурированное логирование каждого запроса через `slog`:
- `method`, `path`, `status`, `duration`, `request_id`
- Использует `http.ResponseWriter` обёртку для захвата статус-кода

### CORS (`cors.go`)

Добавляет CORS заголовки для всех запросов:
- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS`
- `Access-Control-Allow-Headers: Content-Type, Authorization, X-Service-Secret`
- Preflight (`OPTIONS`) → немедленный `200 OK`

### Auth (`auth.go`)

```go
type AuthConfig struct {
    Enabled       bool
    MasterKeyHash string  // SHA-256 hex digest мастер-ключа
    ServiceSecret string  // service-to-service bypass
}
```

При `AuthEnabled=true`:
- Принимает `Authorization: Bearer <token>` или `X-Service-Secret: <secret>`
- Сравнивает SHA-256(token) с `MASTER_KEY_HASH`
- Service secret bypass: если `X-Service-Secret == InternalServiceSecret` — пропускает проверку
- Незащищённые пути: `/health`, `/ready`

### RateLimit (`ratelimit.go`)

```go
type RateLimitConfig struct {
    Enabled       bool
    RPS           float64  // запросов в секунду (default 50)
    Burst         int      // burst size (default 100)
    ServiceSecret string   // bypass для service-to-service
}
```

Token bucket алгоритм через `golang.org/x/time/rate`. Per-IP лимитирование. Service-to-service запросы (`X-Service-Secret`) исключены из лимитов. При превышении: `429 Too Many Requests`.

### OTel (`otel.go`)

OpenTelemetry трейсинг (если `OTEL_ENABLED=true`):
- Создаёт span для каждого HTTP запроса
- Атрибуты: `http.method`, `http.route`, `http.status_code`
- Экспортирует в `OTEL_EXPORTER_OTLP_ENDPOINT`

### Cache (`cache.go`)

```go
type CacheConfig struct {
    Client *cache.Client  // nil = кэш отключён
}
```

HTTP response кэш на Redis (`memdb:cache:*`):
- Кэширует только `GET` запросы + специфические `POST` (search, get_memory)
- Ключ = SHA-256 от (метод + путь + тело)
- TTL задаётся обработчиком через заголовок (по умолчанию 30 секунд)
- `Cache-Control: no-cache` в запросе → пропуск кэша

---

## Config (`internal/config/config.go`)

Конфигурация загружается через `config.Load()` из переменных окружения:

| Env переменная | Default | Описание |
|---|---|---|
| `MEMDB_GO_PORT` | `8080` | Порт сервера |
| `MEMDB_GO_READ_TIMEOUT` | `30s` | ReadTimeout HTTP сервера |
| `MEMDB_GO_WRITE_TIMEOUT` | `120s` | WriteTimeout HTTP сервера |
| `MEMDB_PYTHON_URL` | `http://localhost:8000` | Python backend URL |
| `AUTH_ENABLED` | `false` | Включить аутентификацию |
| `MASTER_KEY_HASH` | — | SHA-256 мастер-ключа |
| `INTERNAL_SERVICE_SECRET` | — | Service-to-service секрет |
| `MEMDB_LOG_LEVEL` | `info` | Уровень логирования |
| `MEMDB_LOG_FORMAT` | `json` | Формат логов: `json` или `text` |
| `OTEL_ENABLED` | `false` | OpenTelemetry трейсинг |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | OTLP endpoint |
| `MEMDB_CACHE_ENABLED` | `false` | HTTP-кэш middleware |
| `MEMDB_REDIS_URL` | `redis://redis:6379/1` | Redis для HTTP-кэша |
| `MEMDB_RATE_LIMIT_ENABLED` | `false` | Rate limiting |
| `MEMDB_RATE_LIMIT_RPS` | `50` | Запросов в секунду |
| `MEMDB_RATE_LIMIT_BURST` | `100` | Burst размер |
| `MEMDB_POSTGRES_URL` | — | PostgreSQL (PolarDB) connection string |
| `MEMDB_QDRANT_ADDR` | — | Qdrant gRPC `host:port` |
| `MEMDB_DB_REDIS_URL` | — | Redis для VSET и планировщика |
| `MEMDB_EMBEDDER_TYPE` | `onnx` | `onnx` или `voyage` |
| `MEMDB_ONNX_MODEL_DIR` | `/models` | Директория ONNX модели |
| `VOYAGE_API_KEY` | — | VoyageAI API ключ |
| `MEMDB_LLM_PROXY_URL` | `http://cliproxyapi:8317` | CLIProxyAPI URL |
| `CLI_PROXY_API_KEY` | — | CLIProxyAPI ключ |
| `MEMDB_LLM_MODEL` | `gemini-2.5-flash` | Модель для rerank/profiler |
| `MEMDB_LLM_EXTRACT_MODEL` | `gemini-2.0-flash-lite` | Модель для fine-mode extract |

---

---

## Сравнение с конкурентами: HTTP Server & API

### mem0 (Python — FastAPI)

| Аспект | mem0 self-hosted | memdb-go |
|---|---|---|
| Framework | FastAPI (Python) | **net/http (Go stdlib)** |
| Concurrency model | asyncio + uvicorn workers | **goroutines (O(1) overhead)** |
| GIL | ❌ Есть (Python) | **✅ Нет GIL** |
| Memory per request | ~5–50MB (Python VM) | **~8KB (goroutine stack)** |
| Startup time | ~3–10s | **< 1s** |
| Middleware stack | Starlette (Python) | net/http chain |
| Observability | Нет встроенного tracing | ✅ OpenTelemetry OTLP |
| Rate limiting | Нет | ✅ Token bucket per-IP |
| Auth | API key header | ✅ SHA-256 + service secret |
| Proxy fallback | ❌ (монолит) | ✅ Python backend fallback |

**Наше ключевое преимущество:** goroutine model — каждый HTTP request обрабатывается в goroutine с 8KB стека vs Python asyncio coroutine. При 1000 concurrent requests Go использует ~8MB RAM vs ~500MB в Python.

**Где mem0 сильнее:** Mature FastAPI экосистема — автоматическая OpenAPI/Swagger документация, pydantic validation, dependency injection. В Go нужно писать всё вручную.

**Цель Go-миграции:** Добавить автогенерацию OpenAPI схемы через `swaggo/swag` или `ogen` — это закроет главный UX gap с FastAPI.

---

### MemOS (Python — FastAPI)

| Аспект | MemOS API | memdb-go |
|---|---|---|
| Routes | REST + WebSocket | REST only |
| Streaming | ✅ SSE/WebSocket для live memory | ❌ Нет streaming memory events |
| API versioning | `/v1/`, `/v2/` | ❌ Нет versioning |
| Health checks | `/health` | ✅ `/health` + `/ready` (детальный) |
| Multi-tenant | Через `mem_cube_id` | ✅ Через `user_name` + `agent_id` |

**Где MemOS сильнее:** WebSocket streaming — клиент может подписаться на live события памяти (new_memory, memory_updated). Это критично для real-time UI.

**Цель Go-миграции:** Добавить SSE endpoint `GET /product/memory/events?user_id=X` — при каждом successful add/update/delete писать событие в Redis Pub/Sub → SSE fanout к подписчикам.

---

### Graphiti/Zep (Python + Go Cloud)

Zep Cloud backend написан на **Go** — это подтверждает правильность нашего направления. Zep Python SDK взаимодействует с Go-бэкендом через REST API.

| Аспект | Zep Cloud | memdb-go |
|---|---|---|
| Backend | **Go** | **Go** |
| SDK | Python, TypeScript | Python (через proxy) |
| Latency | Sub-200ms retrieval | ~50–150ms (pgvector + VSET) |
| Deployment | Managed cloud | **Self-hosted** |
| Pricing | $0.001/req (managed) | **Бесплатно (self-hosted)** |

**Наше преимущество:** Self-hosted = нулевая стоимость per-request. Для high-volume deployments (миллионы запросов/день) это ключевое экономическое преимущество.

---

### Redis Agent Memory Server (Python — FastAPI)

| Аспект | redis/agent-memory-server | memdb-go |
|---|---|---|
| Framework | FastAPI + uvicorn | **net/http** |
| Кэш middleware | ❌ Нет HTTP кэша | ✅ Redis response cache (30s TTL) |
| CORS | ✅ | ✅ |
| Auth | API key + JWT (опц.) | ✅ SHA-256 + service secret |
| Docker | ✅ Один контейнер | ✅ docker-compose (multi-service) |
| HTTPS/TLS | Через nginx | Через nginx/Caddy |

**Где redis/agent-memory-server сильнее:** JWT-based auth с refresh tokens — нативный пользовательский auth без внешнего identity provider. Наш SHA-256 master key — более простая модель, не подходящая для multi-tenant с разными правами доступа.

**Цель Go-миграции:** Добавить JWT middleware в `auth.go` — при включённом auth выдавать user-scoped токены с `user_id` claim. Это разблокирует прямое использование API от браузерных клиентов без service secret.

---

### LangMem — No Server Architecture

LangMem намеренно не является сервером — это **библиотека**, встраиваемая в LangGraph. Нет отдельного процесса, нет HTTP.

**Где LangMem сильнее:** Zero-network-latency — память работает in-process с агентом. Наш HTTP round-trip добавляет 5–50ms на каждый memory call.

**Цель Go-миграции (долгосрочная):** Предоставить Go SDK (`go get memdb.io/go-sdk`) с embedded SQLite/pgvector бэкендом — для встраивания в Go-агентов без сетевого вызова. Это даст LangMem-style zero-latency при сохранении нашей production-grade search архитектуры.

---

## Точки входа (`cmd/`)

| Директория | Бинарник | Описание |
|---|---|---|
| `cmd/server/` | `memdb-go` | Основной HTTP сервер |
| `cmd/mcp-server/` | `mcp-server` | MCP сервер (stdio/HTTP) |
| `cmd/mcp-stdio-proxy/` | `mcp-stdio-proxy` | Stdio ↔ HTTP прокси для MCP |
| `cmd/reembed/` | `reembed` | CLI для перевекторизации воспоминаний |
