# LLM — Извлечение фактов и дедупликация

Пакет `internal/llm/` реализует LLM-based извлечение атомарных фактов из диалогов и принятие решений о дедупликации.

## LLMExtractor

```go
type LLMExtractor struct {
    client  *http.Client
    baseURL string  // CLIProxyAPI URL
    apiKey  string
    model   string  // default: "gemini-2.0-flash-lite"
}
```

**Создание:**
```go
extractor := llm.NewLLMExtractor(baseURL, apiKey, model)
```

Использует OpenAI-совместимый API (CLIProxyAPI → Gemini или любой другой LLM). Timeout: 90 секунд.

---

## Структуры данных

### ExtractedFact

Результат объединённого LLM-вызова:

```go
type ExtractedFact struct {
    Reasoning   string    // chain-of-thought (ПЕРВОЕ поле в JSON)
    Memory      string    // атомарный факт (для add/update)
    Type        string    // "LongTermMemory" или "UserMemory"
    Action      MemAction // add, update, delete, skip
    Confidence  float64   // 0.0–1.0, факты < 0.65 отбрасываются
    TargetID    string    // ID существующего воспоминания (для update/delete)
    ValidAt     string    // ISO-8601 время, когда факт стал истинным
    Tags        []string  // ключевые сущности/темы (2-4 тега)
    ContentHash string    // SHA-256 заполняется пайплайном, не LLM'ом
}
```

### MemAction

```go
const (
    MemAdd    MemAction = "add"
    MemUpdate MemAction = "update"
    MemDelete MemAction = "delete"  // противоречит существующему → инвалидация
    MemSkip   MemAction = "skip"
)
```

### Candidate

Существующее воспоминание, передаваемое LLM для dedup-контекста:

```go
type Candidate struct {
    ID     string `json:"id"`
    Memory string `json:"memory"`
}
```

---

## ExtractAndDedup (v2 — основной метод)

```go
func (e *LLMExtractor) ExtractAndDedup(
    ctx context.Context,
    conversation string,
    candidates []Candidate,
) ([]ExtractedFact, error)
```

**Один LLM вызов** решает обе задачи: извлечение новых фактов И дедупликацию относительно существующих.

### Дизайн промпта

Основан на анализе конкурентов:
- **mem0**: единый вызов extract+dedup, confidence score, ADD/UPDATE/DELETE/NOOP
- **LangMem**: SNR-компрессия, "консолидируй избыточные воспоминания"
- **Graphiti/Zep**: противоречие ≠ дубликат; `valid_at` темпоральная привязка
- **MemOS**: importance score (заменён на confidence)

### System prompt (ключевые правила)

**Правила действий:**
- `add` — новый факт, не покрытый существующими
- `update` — факт уточняет/исправляет/расширяет существующий → `target_id` + merged текст
- `delete` — факт прямо противоречит существующему → `target_id`, поле `memory` пустое
- `skip` — факт избыточен или уже покрыт → пропустить (не включать в вывод)

**SNR правила (LangMem):**
- Каждый факт атомарный: одна чёткая единица информации
- Сохранять конкретику: имена, числа, даты, места
- Не включать приветствия, фразы-заполнители
- Компрессия: если два факта говорят одно — оставить самый специфичный/свежий

**Уровни confidence:**
- 0.9+ — явно сказано, однозначно
- 0.7–0.9 — ясно подразумевается
- 0.5–0.7 — умозаключение, умеренная уверенность
- < 0.5 — спекулятивно → не включать

### Формирование запроса

```
Conversation:
user: [2024-01-15T10:00:00]: Привет, я живу в Москве
assistant: [2024-01-15T10:00:01]: Понятно!

EXISTING MEMORIES (for dedup context):
[{"id":"uuid1","memory":"Пользователь работает программистом"}]
```

### Фильтрация ответа

`parseExtractedFacts(raw)`:
1. Strip markdown fences (` ```json ... ``` `)
2. JSON unmarshal в `[]ExtractedFact`
3. Нормализация `action` (неизвестные → `add`)
4. Отброс фактов с `confidence < MinConfidence (0.65)` (кроме delete)
5. Отброс пустых `memory` (кроме delete)
6. Нормализация `type` (неизвестные → `LongTermMemory`)
7. Отброс `skip`

---

## ExtractFacts (legacy)

```go
func (e *LLMExtractor) ExtractFacts(ctx context.Context, conversation string) ([]ExtractedFact, error)
```

Вызов `ExtractAndDedup` без кандидатов (без dedup-контекста). Для обратной совместимости.

---

## JudgeDedupMerge (legacy)

```go
func (e *LLMExtractor) JudgeDedupMerge(
    ctx context.Context,
    newMem string,
    candidates []Candidate,
) (DedupDecision, error)
```

Старый двухшаговый API. Оборачивает `newMem` как минимальный "диалог" и вызывает `ExtractAndDedup`. Сохранён для обратной совместимости.

---

## callLLM (internal)

```go
func (e *LLMExtractor) callLLM(
    ctx context.Context,
    messages []map[string]string,
    timeout time.Duration,
) (string, error)
```

Низкоуровневый вызов OpenAI-compatible API:
- `temperature: 0.1` (детерминированный вывод)
- `max_tokens: 4096`
- Передаёт `Authorization: Bearer <apiKey>` если задан

---

## Типы памяти (классификация LLM)

| Тип | Описание |
|---|---|
| `LongTermMemory` | Факты о мире, событиях, проектах (по умолчанию) |
| `UserMemory` | Персональная информация о пользователе: имя, возраст, предпочтения, мнения |

LLM классифицирует каждый факт — `UserMemory` только если факт исключительно о личности пользователя.

---

---

## Сравнение с конкурентами: LLM Extraction

### mem0 (Python) — наиболее близкий конкурент

| Аспект | mem0 | memdb-go |
|---|---|---|
| Вызовов LLM на add | 2 (Extract + Update/Dedup) | **1 (ExtractAndDedup unified)** |
| Confidence score | ✅ | ✅ |
| Действия | ADD / UPDATE / DELETE / NOOP | ADD / UPDATE / DELETE / SKIP |
| Tags/entities | ❌ Нет | ✅ 2–4 тега на факт |
| valid_at (bi-temporal) | ❌ Нет | ✅ |
| Язык | Python | **Go** |
| Модели | OpenAI, Anthropic, Gemini, Ollama, 20+ | OpenAI-compatible (CLIProxyAPI) |
| Fallback | ❌ (монолит) | ✅ Python proxy если LLM не настроен |

**Где mem0 сильнее:** Значительно больше поддерживаемых LLM провайдеров через litellm. Поддержка Ollama/локальных моделей без дополнительного middleware.

**Цель Go-миграции:** Заменить `CLIProxyAPI` зависимость на прямые вызовы к OpenAI/Anthropic/Ollama. Реализовать `LLMBackend` интерфейс в `llm/extractor.go` с pluggable провайдерами.

---

### Graphiti/Zep (Python)

| Аспект | Graphiti | memdb-go |
|---|---|---|
| Extraction unit | Entity triplets: (subject, relation, object) | Atomic facts (prose sentences) |
| Bi-temporal | ✅ valid_at + invalid_at | ✅ valid_at (invalid через delete) |
| Contradiction detection | ✅ Graph-level (temporal edges) | ✅ action=delete |
| Entity linking | ✅ Entity dedup + merge | ❌ Нет entity identity |
| Community summaries | ✅ LLM-generated cluster summaries | ❌ Нет |

**Где Graphiti сильнее:** Извлечение структурированных (subject, relation, object) триплетов позволяет строить настоящий knowledge graph с entity identity — один и тот же "Алексей" будет одним узлом в графе, даже если упомянут в 50 разных разговорах. Наши атомарные факты — prose sentences без entity linking.

**Цель Go-миграции:** Добавить в `unifiedSystemPrompt` опциональное поле `"entities": [{"name":"...","type":"PERSON"}]` → писать сущности в отдельную AGE-таблицу → entity linking через нормализацию имён (string similarity).

---

### MemOS (Python)

| Аспект | MemOS | memdb-go |
|---|---|---|
| Importance score | ✅ Отдельный LLM вызов для scoring | ✅ Confidence как importance proxy |
| Memory type | Parametric / Activation / Plaintext | LTM / UserMemory / Episodic / Skill / Tool |
| Extraction granularity | Sentence-level | **Atomic facts** |
| ReAct integration | ✅ Tool calls для управления памятью | ❌ HTTP API only |

**Где MemOS сильнее:** Отдельный importance scoring (не только confidence при создании, но переоценка при retrieval). Наш `IncrRetrievalCount` поднимает importance_score при каждом обращении — аналогичная идея, но проще.

---

### LangMem (Python)

**Уникальная фича LangMem:** Procedural memory — LLM анализирует feedback агента и **напрямую редактирует system prompt**, обучая агента новому поведению без fine-tuning. Мы не поддерживаем это.

**Цель Go-миграции:** Добавить `ProcedureMemory` тип — хранить as JSONB в Postgres обновлённые "инструкции" агента. Читать при search и инжектировать в system prompt через API ответ.

---

## MinConfidence

```go
const MinConfidence = 0.65
```

Факты с `confidence < 0.65` отбрасываются до вставки в БД. Снижает шум при неопределённых утверждениях.

---

## Биtempoральная модель (valid_at)

Каждый извлечённый факт содержит `ValidAt` — ISO-8601 время, когда факт стал истинным (если определимо из диалога). Пайплайн `add_fine.go` использует `valid_at` как `created_at` узла, позволяя правильно датировать исторические факты.

Пример:
> Пользователь: "Три года назад я переехал в Берлин"
> → `valid_at: "2021-01-15T00:00:00"` (если текущая дата 2024-01-15)
