# Scheduler — Фоновый планировщик

Пакет `internal/scheduler/` реализует фоновую обработку задач через Redis Streams, реорганизацию памяти, профилирование пользователей.

---

## Worker — Redis Streams Consumer

**Файл:** `worker.go`

```go
type Worker struct {
    redis     *redis.Client
    reorg     *Reorganizer
    logger    *slog.Logger
    highMsgCh chan streamMsg  // HIGH: mem_update, query, mem_feedback (буфер 32)
    lowMsgCh  chan streamMsg  // LOW:  mem_organize, mem_read, pref_add, add, answer (буфер 128)
    stopCh    chan struct{}
}
```

**Создание и запуск:**
```go
worker := scheduler.NewWorker(redisClient, reorg, logger)
go worker.Run(ctx)  // запускает 4 горутины + основной processLoop
```

### Четыре горутины воркера

#### 1. readLoop

Каждые `10 секунд` (scanInterval):
1. `scanStreamKeys(ctx)` — SCAN Redis по паттерну `scheduler:messages:stream:v2.0:*` (production-safe, без KEYS *)
2. `ensureGroup(ctx, key)` — XGROUP CREATE MKSTREAM (идемпотентно, игнорирует BUSYGROUP)
3. `XREADGROUP` с consumer group `memdb_go_scheduler`, batch 10 сообщений, block 2 секунды

Разобранные сообщения → `enqueue()` → `highMsgCh` или `lowMsgCh` по приоритету label.

#### 2. reclaimLoop

Каждые `30 секунд` (reclaimInterval):
- `XAUTOCLAIM` сообщений, простаивающих > `1 часа` (minIdleTime = Python's DEFAULT_PENDING_CLAIM_MIN_IDLE_MS)
- Обрабатывает брошенные сообщения после краша/перезапуска
- Повреждённые сообщения → XACK без обработки
- Reclaimed сообщения → `enqueue()` с сохранением приоритета

#### 3. retryLoop

Каждые `5 секунд` (retryPollInterval):
- `ZRANGEBYSCORE scheduler:retry:v1 0 <now>` — выбирает созревшие задачи
- `ZREM` выбранных членов (атомарно перед переотправкой)
- Десериализует `retryPayload` из JSON → `ScheduleMessage` с увеличенным `RetryCount`
- Переотправляет через `enqueue()` — приоритет восстанавливается по label

#### 4. periodicReorgLoop

Каждые `6 часов`:
- Через `getActiveCubes(ctx)` находит активные кубы:
  - Primary: сканирует VSET ключи `wm:v:*` → cube IDs
  - Fallback: извлекает cube IDs из ключей scheduler streams
- Для каждого куба (в порядке):
  1. `reorg.CompactWorkingMemory(ctx, cubeID)` — WM compaction если ≥ 50 нод
  2. `reorg.Run(ctx, cubeID)` — near-duplicate merge
  3. `reorg.DecayAndArchive(ctx, cubeID)` — importance decay
- Первый запуск через 3 часа после старта (staggered)

### Priority Queue — маршрутизация по приоритету

**Файл:** `worker_priority.go`

Вместо одного канала используются два с разными буферами:

| Канал | Буфер | Labels |
|---|---|---|
| `highMsgCh` | 32 | `mem_update`, `query`, `mem_feedback` |
| `lowMsgCh` | 128 | `mem_organize`, `mem_read`, `pref_add`, `add`, `answer` |

**Алгоритм `processLoop` (priority select):**

1. **Фаза 1** — non-blocking drain `highMsgCh`: обрабатывает все доступные HIGH-сообщения без блокировки
2. **Фаза 2** — blocking select на обоих каналах: `highMsgCh` побеждает при одновременном поступлении
3. При получении LOW-сообщения — дополнительная non-blocking проверка `highMsgCh` (swap если HIGH появился)

**Почему не два воркера (как в MemOS):**
- Нет дублирования горутин и consumer group
- Единый PEL (Pending Entry List) — проще XAUTOCLAIM
- Retry-сообщения сохраняют приоритет через `isHighPriority(label)`
- Меньше памяти и сложности

```go
func isHighPriority(label string) bool {
    switch label {
    case LabelMemUpdate, LabelQuery, LabelMemFeedback:
        return true
    }
    return false
}
```

### processLoop — диспетчер сообщений

| Label | Обработка | Retry при ошибке |
| --- | --- | --- |
| `add` | XACK без обработки — Go-пайплайн уже выполнил add | — |
| `mem_organize` | `reorg.RunWithError(ctx, cubeID)` — FindNearDuplicates → Union-Find → LLM merge | ✅ DB ошибки |
| `mem_read` | `reorg.ProcessRawMemoryWithError(ctx, cubeID, wmIDs)` — LLM-улучшение WM→LTM | ✅ DB ошибки |
| `mem_update` | `reorg.RefreshWorkingMemoryWithError(ctx, cubeID, content)` — embed query → LTM search → VSET add | ✅ Embed ошибки |
| `pref_add` | `reorg.ExtractAndStorePreferencesWithError(ctx, cubeID, conv)` — LLM извлечение preferences | ✅ LLM ошибки |
| `query` | `reorg.RefreshWorkingMemory` (best-effort, не ретраится) | ❌ low priority |
| `answer` | XACK (только логирование) | — |
| `mem_feedback` | `reorg.ProcessFeedbackWithError(ctx, cubeID, ids, content)` — LLM анализ feedback | ✅ LLM/DB ошибки |
| unknown | XACK (будущие Python labels) | — |

**Важно:** Go воркер использует собственную consumer group (`memdb_go_scheduler`), **независимую** от Python (`scheduler_group`). Оба получают все сообщения параллельно.

### Retry с exponential backoff

**Файл:** `worker_retry.go`

При ошибке обработчика вместо немедленного DLQ задача планируется на повтор:

```text
Attempt 1 (RetryCount=0) → ошибка → scheduleRetry → ZSet score=now+5s
Attempt 2 (RetryCount=1) → ошибка → scheduleRetry → ZSet score=now+10s
Attempt 3 (RetryCount=2) → ошибка → scheduleRetry → ZSet score=now+20s
Attempt 4 (RetryCount=3) → ошибка → maxRetries exhausted → DLQ
```

Реализация:
- `scheduleRetry()` — сериализует `ScheduleMessage` в JSON → `ZADD scheduler:retry:v1 <retry_at_unix> <json>`
- `retryLoop` — каждые 5s: `ZRANGEBYSCORE [0, now]` → `ZREM` → переотправка в `msgCh`
- `ScheduleMessage.retryDelay()` — `baseDelay * 2^retryCount`, cap `5m`
- `ScheduleMessage.maxRetries()` — default 3, переопределяется через `MaxRetries` поле

| Константа | Значение | Описание |
| --- | --- | --- |
| `retryZSetKey` | `scheduler:retry:v1` | Redis Sorted Set для отложенных задач |
| `defaultMaxRetries` | 3 | Попыток до DLQ |
| `retryBaseDelay` | 5s | Начальная задержка |
| `retryMaxDelay` | 5m | Максимальная задержка |
| `retryPollInterval` | 5s | Период опроса ZSet |

**Инспекция retry очереди:**
```
ZRANGE scheduler:retry:v1 0 -1 WITHSCORES
```

### Dead Letter Queue

Неудачные сообщения пишутся в `scheduler:dlq:v1` (Redis Stream, MAXLEN 1000) только после исчерпания всех retry:
```
XRANGE scheduler:dlq:v1 - +
```
Содержит: `origin_stream`, `origin_msg_id`, `cube_id`, `label`, `reason`, `failed_at`.

### Параметры воркера

| Константа | Значение | Описание |
| --- | --- | --- |
| `consumerName` | `memdb_go_worker` | Имя consumer в группе |
| `readBatchSize` | 10 | Сообщений за один XREADGROUP |
| `blockDuration` | 2s | Блокировка ожидания новых сообщений |
| `scanInterval` | 10s | Период пересканирования ключей |
| `reclaimInterval` | 30s | Период проверки брошенных сообщений |
| `minIdleTime` | 1h | Минимальное время простоя для XAUTOCLAIM |
| `msgChanBuffer` | 64 | Размер буфера канала сообщений |
| `periodicReorgInterval` | 6h | Период периодической реорганизации |
| `retryZSetKey` | `scheduler:retry:v1` | Redis ZSet для retry очереди |
| `defaultMaxRetries` | 3 | Попыток до DLQ |
| `retryBaseDelay` | 5s | Начальная задержка backoff |
| `retryMaxDelay` | 5m | Максимальная задержка backoff |
| `retryPollInterval` | 5s | Период опроса retry ZSet |

---

## Reorganizer — Реорганизатор памяти

**Файл:** `reorganizer.go` + `reorganizer_consolidate.go` + `reorganizer_feedback.go` + `reorganizer_mem_read.go` + `reorganizer_prefs.go` + `reorganizer_wm.go`

```go
type Reorganizer struct {
    postgres *db.Postgres
    embedder embedder.Embedder
    wmCache  *db.WorkingMemoryCache  // nil = VSET не настроен
    llmURL   string
    llmKey   string
    llmModel string  // default: "gemini-2.0-flash-lite"
}
```

### CompactWorkingMemory — WM compaction

**Файл:** `reorganizer_wm_compact.go`

Запускается в `periodicReorgLoop` **перед** `Run()` для каждого активного куба.

**Алгоритм:**

1. `CountWorkingMemory(ctx, cubeID)` — если < 50 нод, пропускаем
2. `GetWorkingMemoryOldestFirst(ctx, cubeID, 200)` — загружаем все WM ноды (oldest first)
3. Разбиваем: `toSummarize = nodes[:-10]`, `toKeep = nodes[-10:]`
4. LLM суммаризирует `toSummarize` → один абзац `EpisodicMemory`
5. Embed summary → `InsertMemoryNodes` как `EpisodicMemory` LTM нод
6. `DeleteByPropertyIDs(toSummarize)` + `VRemBatch(toSummarize)` из VSET

**Ключевое отличие от Redis AMS:**

| | Redis AMS | memdb-go |
| --- | --- | --- |
| Триггер | Token count (tiktoken) | Node count (50 нод) |
| Что происходит с WM | Суммари в `context` поле, WM остаётся | WM удаляется, суммари → LTM |
| Потеря контекста | Нет (context поле) | Нет (EpisodicMemory в LTM, searchable) |
| Зависимость от модели | Да (cl100k_base) | Нет (count-based) |

| Константа | Значение | Описание |
| --- | --- | --- |
| `wmCompactThreshold` | 50 | Порог нод для запуска compaction |
| `wmCompactKeepRecent` | 10 | Сколько последних нод оставить |
| `wmCompactFetchLimit` | 200 | Максимум нод для загрузки |
| `wmCompactLLMTimeout` | 60s | Таймаут LLM вызова |

### Run — основной цикл реорганизации

**Файл:** `reorganizer_consolidate.go`

Алгоритм (на основе redis/agent-memory-server + MemOS best practices):

1. **FindNearDuplicates** — pgvector поиск пар с cosine > `0.85` (dupThreshold), лимит 60 пар
2. **Union-Find** — группировка пар в кластеры связанных воспоминаний
3. Для каждого кластера (один LLM вызов, timeout 45s):
   - LLM выбирает `keep_id` + `remove_ids` + `merged_text`
4. `UpdateMemoryNodeFull(keep_id, merged_text, new_embedding)` — обновляет лучшую память слитым текстом
5. Soft-delete всех `remove_ids`
6. Evict `remove_ids` из Redis VSET hot cache

### DecayAndArchive

**Файл:** `reorganizer.go`

```go
func (r *Reorganizer) DecayAndArchive(ctx context.Context, cubeID string) (int64, error)
```

Двухфазный importance lifecycle:
1. `importance_score *= 0.95` для всех LTM/UserMemory (decay per 6h cycle)
2. Автоархивирование узлов с `importance_score < 0.10`

**Математика:**
- Начальное значение: 1.0
- После 1 дня (4 цикла по 6h): `1.0 * 0.95^4 ≈ 0.81`
- После 7 дней: `1.0 * 0.95^28 ≈ 0.24`
- После ~11 дней без обращений: `< 0.10` → архивирование
- Каждое обращение (retrieval) поднимает `importance_score + 0.1` (IncrRetrievalCount)

### ProcessRawMemory (mem_read)

**Файл:** `reorganizer_mem_read.go`

Обрабатывает WorkingMemory узлы через LLM → конвертирует в LongTermMemory:
1. Загружает тексты WM узлов из Postgres
2. LLM улучшает/структурирует каждый факт
3. Вставляет новые LTM узлы
4. Удаляет исходные WM узлы

### RefreshWorkingMemory (mem_update/query)

**Файл:** `reorganizer_wm.go`

Обновляет Working Memory при поступлении нового запроса пользователя:
1. Embed query (timeout 10s)
2. `VectorSearch` в LTM/UserMemory, top-10, minScore `0.60`
3. Добавляет найденные узлы в VSET hot cache через `VAdd` (CAS-идемпотентно)

Цель: держать в hot cache наиболее релевантный сессионный контекст.

### ExtractAndStorePreferences (pref_add)

**Файл:** `reorganizer_prefs.go`

Нативная Go-замена Python pref_mem сервиса:
1. LLM извлекает user preferences из диалога
2. Сохраняет как `UserMemory` в Postgres (не требует Qdrant)

### ProcessFeedback (mem_feedback)

**Файл:** `reorganizer_feedback.go`

Полностью нативная Go обработка пользовательского feedback:
1. Парсинг `retrieved_memory_ids` + `feedback_content` из сообщения
2. LLM анализирует качество воспоминаний на основе feedback
3. `UpdateMemoryNodeFull` для улучшенных воспоминаний
4. `DeleteByPropertyIDs` для неверных/устаревших

---

## Profiler — Профилировщик пользователя

**Файл:** `profiler.go`

Генерирует и кэширует Memobase-style user profile summary в Redis.

```go
type Profiler struct {
    postgres    *db.Postgres
    redis       *db.Redis
    llmProxyURL string
    llmModel    string
}
```

### Как работает

**Мотивация:** Memobase достигает 85% на LOCOMO temporal questions потому что всегда инжектирует структурированный профиль пользователя (имя, возраст, работа, местоположение, хобби) вне зависимости от запроса.

**Lifecycle:**
1. `TriggerRefresh(cubeID)` — вызывается fire-and-forget после каждого fine-mode add
2. Загружает все `UserMemory` узлы куба (до 100)
3. Форматирует в список фактов
4. LLM (temperature=0.1, max_tokens=400) пишет 3-6 предложений профиля от третьего лица
5. Кэшируется в Redis как `profile:{cubeID}` с TTL 1 час
6. `SearchService` читает из Redis при каждом поиске (в памяти, ~0ms)

### GetProfile

```go
func (p *Profiler) GetProfile(ctx context.Context, cubeID string) string
```

Возвращает кэшированный профиль или пустую строку (нет Redis, нет данных).

### System prompt профиля

> "You are a personal assistant creating a user profile. From the memory facts provided, write a 3-6 sentence profile paragraph in third person covering: name, age, occupation, location, major interests/hobbies, and any other notable stable facts. Be concrete and specific. Do not make things up. If information is unavailable, omit that field."

### Параметры

| Константа | Значение | Описание |
|---|---|---|
| `profileKeyPrefix` | `profile:` | Prefix ключа в Redis |
| `profileTTL` | 1 час | TTL профиля в Redis |
| `profileMinLength` | 50 символов | Минимум данных для генерации |

---

## Message Parser Utilities

**Файл:** `worker.go`

| Функция | Описание |
|---|---|
| `parseMemReadIDs(content)` | Парсит WM IDs из mem_read: JSON `{"memory_ids":[...]}` или CSV строка |
| `parsePrefConversation(content)` | Извлекает conversation из pref_add: plain text, JSON `{"conversation":"..."}` или `{"history":[...]}` |
| `parseFeedbackPayload(content)` | Парсит `retrieved_memory_ids` + `feedback_content` из mem_feedback JSON |
| `splitStreamKey(key)` | Разбирает ключ `scheduler:messages:stream:v2.0:{user}:{cube}:{label}` |

---

## Prompts

**Файл:** `prompts.go`

Содержит системные промпты для всех LLM-вызовов реорганизатора:
- Консолидация дублей (выбор keep/merge/remove)
- Улучшение raw WM в структурированные LTM факты
- Анализ feedback для коррекции памяти
- Извлечение preferences

---

## LLM Client

**Файл:** `llm_client.go`

`callLLM(ctx, messages, cfg)` — общий OpenAI-compatible клиент для всех компонентов scheduler пакета. `temperature=0.1`, `max_tokens=2048`, timeout из конфига.

---

## Сравнение с конкурентами: Scheduler & Background Processing

### mem0 (Python)

| Аспект | mem0 | memdb-go |
|---|---|---|
| Фоновая обработка | asyncio / Thread pool | **Redis Streams Consumer Group** |
| At-least-once delivery | ❌ (fire-and-forget) | ✅ XACK + reclaimLoop |
| Dead Letter Queue | ❌ | ✅ `scheduler:dlq:v1` |
| Periodic cleanup | ❌ | ✅ `periodicReorgLoop` каждые 6h |
| Importance decay | ❌ Нет | ✅ `DecayAndArchive` (0.95 per cycle) |
| Near-duplicate merge | ❌ Нет | ✅ `Reorganizer` (Union-Find + LLM) |
| Crash recovery | ❌ | ✅ `XAUTOCLAIM` idle > 1h |

**Наше преимущество:** Redis Streams consumer group с XAUTOCLAIM — production-grade гарантия at-least-once delivery. В mem0 фоновые задачи теряются при краше процесса.

**Где mem0 сильнее:** mem0 Cloud предоставляет managed scheduler без self-hosting Redis. Для self-hosted нужно поднимать Redis отдельно.

---

### MemOS (Python) — Redis Streams Scheduler

| Аспект | MemOS RedisStreamsScheduler | memdb-go Worker |
|---|---|---|
| Transport | Redis Streams | **Redis Streams** |
| Consumer group | Python `scheduler_group` | **`memdb_go_scheduler` (независимая)** |
| Task types | INGEST / RETRIEVE / PROCESS | add / mem_organize / mem_read / mem_update / pref_add / query / answer / mem_feedback |
| Max retries | ✅ TaskModel с retry count | ✅ ZSet + exponential backoff (5s→10s→20s→DLQ) |
| Priority queue | ✅ priority поле у Task | ❌ FIFO |
| Periodic reorg | ❌ Только по событиям | ✅ Каждые 6h для всех активных кубов |

**Где MemOS сильнее:**
1. **Priority queue** — MemOS может приоритизировать задачи (например, user-triggered > background). Нам нужно добавить приоритет в stream message.
2. **Next-Scene Prediction** — MemOS предзагружает память *превентивно* во время инференса. Мы только реактивно на события.

**Цель Go-миграции:**
- Реализовать predictive VSET preload при получении `query` события (уже частично в `handle(LabelQuery)`)

---

### Redis Agent Memory Server (Python)

| Аспект | redis/agent-memory-server | memdb-go |
|---|---|---|
| WorkingMemory compaction | ✅ Авто-суммаризация при превышении token limit | ❌ Только cleanup по limit узлов |
| Session reconstruction | ✅ v0.12.2: WM из LTM при новой сессии | ⚠️ Частично через mem_read |
| Background indexing | ❌ Inline при add | ✅ Async через scheduler |
| Near-duplicate detection | ✅ Cosine threshold | ✅ Union-Find + LLM |

**Где redis/agent-memory-server сильнее:** Автоматическая компрессия WorkingMemory при превышении token budget — критично для длинных агентных сессий (50+ turns). При превышении порога они суммаризируют через LLM и заменяют окно одной компактной сводкой.

**Цель Go-миграции:** В `worker.go` добавить `LabelWMCompact` — при получении события считать суммарную длину WM узлов → если > N токенов → LLM суммаризация → replace всех WM одним `EpisodicMemory` + чистый VSET.

---

### Graphiti/Zep (Python)

| Аспект | Graphiti | memdb-go Reorganizer |
|---|---|---|
| Near-duplicate merge | Entity-level dedup | **Near-duplicate LLM merge** |
| Community detection | ✅ LLM-generated summaries | ❌ Нет |
| Temporal edge maintenance | ✅ invalid_at на противоречия | ✅ Hard delete |
| Background processing | Python asyncio | **Go goroutines** |

**Где Graphiti сильнее:** Community summaries — периодически строит суммари связанных entity-кластеров. Это позволяет отвечать на абстрактные вопросы ("Что я знаю о работе этого человека?") без точного vector match.

**Цель Go-миграции:** Расширить `Reorganizer.Run()` — после Union-Find merge добавить community detection: если кластер > 3 узлов → генерировать `CommunityMemory` суммари и хранить как отдельный тип узла.
