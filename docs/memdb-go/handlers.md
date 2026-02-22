# Handlers — HTTP-обработчики

Пакет `internal/handlers/` содержит все HTTP-обработчики. Каждый обработчик либо выполняется нативно в Go, либо проксирует запрос в Python-бэкенд через `rpc.PythonClient`.

## Handler (центральный объект)

```go
type Handler struct {
    python        *rpc.PythonClient
    postgres      *db.Postgres
    qdrant        *db.Qdrant
    redis         *db.Redis
    wmCache       *db.WorkingMemoryCache
    embedder      embedder.Embedder
    searchService *search.SearchService
    llmExtractor  *llm.LLMExtractor
    profiler      *scheduler.Profiler
    logger        *slog.Logger
}
```

Все поля опциональны (кроме `python`). Если зависимость не инициализирована — обработчик падает на proxy к Python.

---

## Обработчики памяти (add)

### `NativeAdd` — POST /product/add

**Файлы:** `add.go`, `add_fast.go`, `add_fine.go`, `add_windowing.go`, `add_props.go`

Точка входа для добавления воспоминаний. Поддерживает два режима:

#### Режим `fast` (по умолчанию)

**Пайплайн** (`add_fast.go`):
1. Извлечение памяти из сообщений через скользящее окно (`add_windowing.go`)
   — размер окна: 4096 символов, перекрытие: 800 символов
2. **Batch content-hash dedup** — SHA-256 каждого текста, один запрос к Postgres для фильтрации дублей
3. **Embed** — векторизация через ONNX/Voyage
4. **Cosine dedup** — сравнение с существующими векторами (порог `0.92`)
5. Создание пары узлов: `WorkingMemory` + `LongTermMemory/UserMemory`
6. Batch insert в Postgres
7. Запись в VSET hot cache (Redis HNSW) для WorkingMemory
8. Cleanup старых WorkingMemory (лимит по умолчанию: 20 узлов)

#### Режим `fine` (LLM-based)

**Пайплайн** (`add_fine.go`):
1. Форматирование диалога в текст `role: [time]: content`
2. **Fetch candidates** — двухуровневый поиск похожих воспоминаний:
   - Tier 1: Redis VSET HNSW (~1–5ms) для WorkingMemory
   - Tier 2: Postgres pgvector (~20–100ms) для LTM + UserMemory
3. **Один LLM вызов** `ExtractAndDedup(conversation, candidates)`:
   - Возвращает список `ExtractedFact` с action: `add/update/delete/skip`
4. **Content-hash dedup** перед embed (фильтрация ADD фактов)
5. **Batch embed** всех ADD/UPDATE фактов в одном ONNX forward pass
6. Применение действий:
   - `ADD` → создание пары WM + LTM узлов
   - `UPDATE` → `UpdateMemoryNodeFull` (новый текст + re-embed)
   - `DELETE` → hard delete противоречащего воспоминания + evict из VSET
7. Batch insert новых узлов
8. Cleanup WorkingMemory
9. Async: генерация EpisodicMemory (summary сессии)
10. Async: `Profiler.TriggerRefresh` (обновление профиля пользователя)

#### Логика выбора режима и proxy fallback

```go
func (h *Handler) canHandleNativeAdd(req) bool {
    // Требует: postgres + embedder
    // mode=fine дополнительно требует llmExtractor
    // async=true и is_feedback=true → всегда proxy
}
```

#### Константы

| Константа | Значение | Описание |
|---|---|---|
| `windowChars` | 4096 | Размер скользящего окна (символы) |
| `overlapChars` | 800 | Перекрытие между окнами |
| `maxWorkingMemory` | 20 | Максимум WorkingMemory узлов на куб |
| `dedupThreshold` | 0.92 | Порог cosine для dedup при fast-add |
| `maxMessages` | 200 | Максимум сообщений на запрос |

---

## Обработчики поиска

### `NativeSearch` — POST /product/search

**Файл:** `search.go`

Делегирует в `search.SearchService.Search()`. Proxy fallback при:
- `searchService == nil` или не может работать
- `mode == "fine"` (требует LLM)
- `internet_search == true` (требует SearXNG)
- Любой ошибке в поиске

Результаты кэшируются в Redis (TTL 30 секунд, ключ по SHA256 запроса).

**Параметры запроса:**

| Поле | Тип | По умолчанию | Описание |
|---|---|---|---|
| `query` | string | обязательно | Поисковый запрос |
| `user_id` | string | обязательно | ID пользователя |
| `top_k` | int | 10 | Бюджет text_mem |
| `skill_mem_top_k` | int | 3 | Бюджет skill_mem |
| `pref_top_k` | int | 6 | Бюджет pref_mem |
| `tool_mem_top_k` | int | 6 | Бюджет tool_mem |
| `dedup` | string | `"no"` | Режим: `no`, `sim`, `mmr` |
| `relativity` | float | 0 | Минимальный порог score |
| `include_skill_memory` | bool | true | Включить SkillMemory |
| `include_preference` | bool | true | Включить preferences |
| `search_tool_memory` | bool | false | Включить ToolMemory |

---

## Обработчики памяти (get/delete)

### `NativeGetMemory` — GET /product/get_memory/{memory_id}

**Файл:** `memory_get.go`

Поиск узла по property UUID через `postgres.GetMemoryByPropertyID`. Кэшируется 120 секунд. Возвращает 404 если не найдено.

### `NativeGetMemoryByIDs` — POST /product/get_memory_by_ids

Batch-получение по списку property UUID. Per-ID кэш, misses загружаются одним запросом.

### `NativePostGetMemory` — POST /product/get_memory

Возвращает все воспоминания куба с пагинацией:
- `text_mem` — LongTermMemory + UserMemory из Postgres
- `skill_mem` — SkillMemory из Postgres
- `pref_mem` — из Qdrant (explicit + implicit preference collections)
- `tool_mem` — всегда пустой (хранится в Python)

Proxy если переданы `filter` поля (сложная фильтрация).

### `NativeGetAll` — POST /product/get_all

**Файл:** `memory_getall.go`

Возвращает все воспоминания куба по типу с пагинацией через `postgres.GetAllMemories`.

### `NativeDelete` — POST /product/delete_memory

**Файл:** `memory_delete.go`

Hard delete по property IDs через `postgres.DeleteByPropertyIDs`. Инвалидирует кэш.

---

## Обработчики пользователей

**Файл:** `users.go`

| Обработчик | Endpoint | Описание |
|---|---|---|
| `NativeListUsers` | GET /product/users | Список всех user_name из Postgres (кэш 120с) |
| `NativeGetUser` | GET /product/users/{user_id} | Проверка существования пользователя |
| `NativeRegisterUser` | POST /product/users/register | Stub — echo user_id (авто-создание при первом add) |
| `NativeInstancesStatus` | GET /product/instances/status | Статус Go-шлюза (hostname, go_version) |
| `NativeInstancesCount` | GET /product/instances/count | Количество уникальных пользователей |
| `NativeConfigure` | POST /product/configure | Stub — конфигурация через env |
| `NativeGetConfig` | GET /product/configure/{user_id} | Конфигурация пользователя |
| `NativeGetUserConfig` | GET /product/users/{user_id}/config | Конфиг из Postgres |
| `NativeUpdateUserConfig` | PUT /product/users/{user_id}/config | Обновление конфига, инвалидация кэша |
| `NativeExistMemCube` | POST /product/exist_mem_cube_id | Проверка существования куба |
| `NativeGetUserNamesByMemoryIDs` | POST /product/get_user_names_by_memory_ids | Маппинг memory_id → user_name |

---

## Health endpoints

| Обработчик | Endpoint | Описание |
|---|---|---|
| `Health` | GET /health | Всегда 200 OK, возвращает go_version и hostname |
| `ReadinessCheck` | GET /ready | Проверяет Python + все DB клиенты. 503 если Python недоступен |

---

## Планировщик

### `NativeSchedulerAllStatus` / `NativeSchedulerStatus` / `NativeSchedulerTaskQueueStatus`

**Файл:** `scheduler.go`

Нативные Go-обработчики для мониторинга Redis Streams consumer group (`memdb_go_scheduler`). Не требуют Python.

---

## Embeddings

### `OpenAIEmbeddings` — POST /v1/embeddings

**Файл:** `embeddings.go`

OpenAI-совместимый endpoint для векторизации. Использует настроенный embedder напрямую. Нужен для MCP-сервера и внутреннего использования.

---

## Вспомогательные функции

### Cache helpers (`handlers.go`)

| Функция | Описание |
|---|---|
| `cacheGet(ctx, key)` | Читает из Redis, возвращает nil при промахе |
| `cacheSet(ctx, key, value, ttl)` | Пишет в Redis с TTL |
| `cacheDelete(ctx, key)` | Удаляет ключ |
| `cacheInvalidate(ctx, patterns...)` | SCAN + DEL по паттернам (production-safe) |

Все операции — no-op если `redis == nil`.

---

## Сравнение с конкурентами: Add Pipeline

### mem0 (Python, ~50k ⭐)

| Аспект | mem0 | memdb-go |
|---|---|---|
| Языки | Python (asyncio) | **Go (goroutines)** |
| Параллелизм | asyncio event loop (GIL) | **true parallelism** |
| Add pipeline | 2 фазы: Extraction → Update (отдельные LLM вызовы) | **1 вызов ExtractAndDedup** (быстрее на 1 RTT) |
| Content-hash dedup | Нет | ✅ SHA-256 batch за 1 DB round-trip |
| VSET hot cache | Нет | ✅ Redis HNSW 1–5ms для WM dedup |
| Async mode | Thread pool | Native goroutines, нет GIL |
| Fallback | Нет (монолит) | ✅ Python proxy fallback при ошибке |

**Где mem0 сильнее:** Graph memory (Neo4j) для multi-hop запросов — мы пишем в flat `memory_edges`, но не строим entity-relationship граф. mem0g извлекает (subject, relation, object) триплеты и хранит их в Neo4j.

**Цель Go-миграции:** Реализовать entity triplet extraction в `add_fine.go` и хранить в Apache AGE (уже есть в стеке) без Neo4j.

---

### Redis Agent Memory Server (Python)

| Аспект | redis/agent-memory-server | memdb-go |
|---|---|---|
| Язык | Python (FastAPI) | **Go** |
| WorkingMemory | Sliding window, автосуммаризация | VSET HNSW hot cache |
| Token budget | ✅ Явное управление токен-бюджетом | ❌ Нет |
| Conversation summarization | ✅ Авто-компрессия истории | ❌ Нет (только EpisodicMemory) |
| WM → LTM promotion | ✅ v0.12.2: реконструкция WM из LTM | ✅ mem_read handler |
| Content-hash dedup | ✅ | ✅ |

**Где redis/agent-memory-server сильнее:** Автоматическое сжатие рабочей памяти с контролем бюджета токенов — критично для длинных сессий. Их `v0.12.2` реконструирует WM из LTM при старте новой сессии.

**Цель Go-миграции:** Добавить token-budget manager в `add_windowing.go` — при превышении порога автоматически суммаризировать через LLM и создавать `EpisodicMemory`.

---

### MemOS (Python)

**Где MemOS сильнее:** Next-Scene Prediction — MemOS проактивно предзагружает память во время инференса LLM, предсказывая следующие нужды агента. Мы реагируем на `mem_update` события только постфактум.

**Цель Go-миграции:** Реализовать предсказательный prefetch в `scheduler/worker.go` — при получении `query` события предзагружать TopK LTM в VSET *до* того как придёт `mem_update`.

---

### Shared helpers (`add.go`)

| Функция | Описание |
|---|---|
| `textHash(text)` | SHA-256 первых 16 байт нормализованного текста для content-hash dedup |
| `workingBinding(wmID)` | Создаёт ссылку LTM→WM: `[working_binding:uuid]` |
| `cleanupWorkingMemory(ctx, cubeID)` | Удаляет старые WM узлы, evicts из VSET |
| `getWorkingMemoryLimit(ctx, cubeID)` | Лимит WM из конфига куба (с кэшем 5 мин) |
| `nowTimestamp()` | Текущее время в Python-совместимом формате |
