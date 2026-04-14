# Add Pipeline Excellence Roadmap

> Конкурентный анализ (март 2026): mem0 22k★, Zep/Graphiti 4k★, Letta 12k★, LangMem 2k★, Motorhead (deprecated).
> Цель: довести add pipeline до лучшего в классе, опережая всех конкурентов.

---

## Текущее состояние: что у нас уже лучше конкурентов

| Фича | MemDB | Лучший конкурент |
|-------|-------|-----------------|
| Multi-mode pipeline (fast/fine/buffer/async/feedback) | 5 режимов | Graphiti: 2 (normal/bulk) |
| Content classifier (skip trivial/code-only) | Есть | Ни у кого нет |
| Buffer batching (сокращает LLM вызовы ~80%) | Есть | Motorhead (deprecated) |
| Hallucination filter | Есть | Ни у кого нет |
| VSET hot cache (Redis HNSW, ~1-5ms) | Есть | Все ходят в основной store |
| Bi-temporal model (valid_at) | Есть | Только Graphiti |
| Content-hash dedup | Есть | redis/agent-memory-server |
| importance_score + retrieval_count decay | Есть | A-MEM (только retrieval_count) |
| Multi-layer dedup (hash → cosine → near-dup) | 3 слоя | mem0: 2 (cosine + LLM) |
| Python proxy для /product/add | Удалён ✅ | — |

---

## Ранее реализовано ✅ (февраль 2026)

> Из конкурентного анализа mem0 47k★, MemOS 5.6k★, redis/agent-memory-server, A-MEM, SimpleMem.

| Улучшение | Источник | Реализация |
|-----------|----------|-----------|
| **Content-hash dedup** | redis/agent-memory-server | `textHash()` → `FilterExistingContentHashes` перед insert. Exact дубликаты отсекаются без LLM |
| **retrieval_count tracking** | A-MEM | Async `IncrRetrievalCount` в SearchService после формирования результатов |
| **importance_score + decay** | MemOS + SimpleMem | `importance *= 0.95` в periodicReorgLoop, soft-delete при < 0.1, `+0.15` при retrieval |
| **Periodic compaction** | redis/agent-memory-server | `periodicReorgLoop` (6h) — reorganizer по таймеру для всех кубов |
| **Graph edges** | mem0 (Neo4j) | `MERGED_INTO`, `EXTRACTED_FROM`, `MENTIONS_ENTITY` edges в Apache AGE |
| **Contradiction detection** | mem0 | `"relation": "contradiction"` в consolidation → `SoftDeleteContradicted` |
| **Entity linking** | redis/agent-memory-server | Batch embed entity names → upsert entity_nodes → `MENTIONS_ENTITY` edges |
| **Memory lifecycle** | MemOS | `activated → merged` + `merged_into_id` (SoftDeleteMerged) |
| **Python proxy удалён** | — | HTTP 422/500 вместо proxy fallback в NativeAdd (март 2026) |

---

## Улучшения из конкурентного анализа (март 2026)

### 1. Soft-delete / temporal invalidation 🔴 Высокий приоритет

**Источник:** Graphiti (`expired_at` на edges и nodes)

**Проблема:** DELETE/UPDATE в `applyFineActions` делают hard-delete (`DeleteByPropertyIDs`).
Данные теряются безвозвратно. Нет аудита, нет undo, нет point-in-time запросов.

**Решение:** `expired_at` timestamp вместо hard-delete.

```go
// Вместо DeleteByPropertyIDs:
// UPDATE SET status = 'expired', expired_at = $now WHERE id = $id

// Query фильтрация:
// WHERE status = 'activated' AND (expired_at IS NULL OR expired_at > $query_time)

// Cleanup (опционально, по cron):
// DELETE WHERE status = 'expired' AND expired_at < now() - interval '30 days'
```

**Что меняется:**
- `add_fine.go: applyDeleteAction()` → soft-delete с `expired_at`
- `add_fine.go: applyUpdateAction()` → expire old + insert new (вместо UPDATE)
- `db/queries/` → новый SQL для soft-delete
- `search/` → WHERE фильтр по `expired_at`
- `scheduler/reorganizer.go` → использовать soft-delete при merge

**Effort:** M (5-7 файлов, SQL миграция)
**Метрика:** 0 hard-deletes в add pipeline. Point-in-time query работает.

---

### 2. Embedding cache 🟡 Средний приоритет

**Источник:** Letta (`@async_redis_cache(key_func=lambda text, model, endpoint: ...)`)

**Проблема:** MemDB re-embed'ит одинаковые тексты при каждом запросе.
ONNX локальный (~5-20ms), но при buffer flush одни и те же фразы встречаются повторно.

**Решение:** Redis cache по хэшу текста.

```go
// internal/embedder/cache.go
type CachedEmbedder struct {
    inner  Embedder
    redis  *redis.Client
    ttl    time.Duration  // 24h default
}

// Key: "emb:" + SHA256(text)[:16]
// Value: []byte (binary float32 slice)
// Flow: cacheGet → miss → inner.Embed() → cacheSet → return
```

**Effort:** S (1 новый файл, wrapper вокруг существующего embedder)
**Метрика:** cache hit rate > 15% при buffer flush. ~20% экономия ONNX inference.

---

### 3. OTel tracing на add pipeline 🟡 Средний приоритет

**Источник:** Letta (`@trace_method` на каждом service method), Graphiti (configurable `Tracer`)

**Проблема:** Отладка latency только через slog. Нет breakdown по стадиям pipeline.
Непонятно, где bottleneck: LLM extraction? Embedding? DB insert? Background tasks?

**Решение:** OpenTelemetry spans на каждой стадии.

```go
// Span hierarchy:
// NativeAdd (total)
//   ├── classify_content
//   ├── fetch_candidates
//   │   ├── vset_search
//   │   └── postgres_search
//   ├── llm_extract_and_dedup
//   ├── filter_hallucinated
//   ├── embed_facts (batch)
//   ├── apply_actions
//   │   ├── insert_nodes
//   │   └── vset_write
//   └── background (fire-and-forget, linked)
//       ├── episodic_summary
//       ├── skill_extraction
//       ├── tool_trajectory
//       ├── entity_linking
//       └── profile_refresh
```

**Что меняется:**
- `internal/server/` → OTel TracerProvider init (OTLP exporter)
- `internal/handlers/add_fine.go` → span Start/End на каждой стадии
- `internal/handlers/add_fast.go` → аналогично (меньше стадий)
- `docker-compose.yml` → Grafana Tempo / Jaeger контейнер (опционально)

**Effort:** M (интеграция OTel SDK, 3-4 файла)
**Метрика:** p95 latency breakdown по стадиям. Bottleneck identification < 5 min.

---

### 4. LLM call semaphore 🟡 Средний приоритет

**Источник:** Graphiti (`semaphore_gather(max_concurrency=N)`)

**Проблема:** Background goroutines fire-and-forget без лимита:
- episodic summary (45s timeout)
- skill extraction (90s timeout)
- tool trajectory (90s timeout)
- entity linking (15s timeout)

При burst'е 10 add-запросов → 40 параллельных LLM вызовов → CLIProxyAPI rate limit / OOM.

**Решение:** Shared semaphore для всех background LLM вызовов.

```go
// internal/handlers/handler.go
type Handler struct {
    // ...existing fields...
    llmSem *semaphore.Weighted  // max concurrent background LLM calls
}

// Config: MEMDB_LLM_MAX_CONCURRENT=8 (default)

// Usage in add_fine.go, add_skill.go, add_episodic.go:
// if !h.llmSem.TryAcquire(1) {
//     h.logger.Debug("skipping background task: LLM semaphore full")
//     return
// }
// defer h.llmSem.Release(1)
```

**Effort:** S (1 поле в Handler, ~5 точек вставки Acquire/Release)
**Метрика:** max concurrent LLM calls ≤ 8 при любой нагрузке.

---

### 5. Inline preference extraction 🟡 Средний приоритет

**Источник:** Анализ миграции (март 2026). Не существует ни в Python, ни в MemOS upstream — новая фича.

**Проблема:** Preference extraction работает только через explicit `pref_add` scheduler task.
Пользовательские предпочтения из обычных разговоров (add pipeline) не извлекаются автоматически.

**Решение:** 5-й background goroutine в `add_fine.go`.

```go
// internal/handlers/add_pref.go
// Два типа:
// 1. Explicit: "используй snake_case", "я предпочитаю тёмную тему"
// 2. Implicit: user consistently corrects AI on X → infer preference
//
// Flow: detect preference signals → LLM extract → vector dedup → ADD/UPDATE
// Gate: conversation has user messages with opinion/preference patterns
```

**Effort:** M (LLM prompt, dedup, integration)
**Метрика:** Preferences извлекаются из обычных add-запросов, не только из explicit pref_add.

---

### 6. Skill extraction: chat_history support 🟢 Низкий приоритет

**Источник:** Python/MemOS имеют `whether_use_chat_history` + `content_of_related_chat_history` в skill extraction.

**Проблема:** Go skill extractor не использует историю предыдущих бесед для обогащения skill контекста.

**Решение:** Добавить optional chat history в `ExtractSkill()`.

**Effort:** S (расширение промпта + параметра)
**Метрика:** Skill descriptions более полные при наличии истории.

---

### 7. Periodic reorganizer interval tuning 🟡 Средний приоритет

**Источник:** Python/MemOS reorganize каждые 100s, Go — каждые 6h (60x реже).

**Проблема:** Near-duplicates и мёртвые memories накапливаются 6 часов до cleanup.

**Решение:** Configurable interval через env var.

```go
// MEMDB_REORG_INTERVAL=30m (default, compromise between 100s и 6h)
// Или: event-driven — trigger после N новых add операций
```

**Effort:** S (1 env var, 1 строка конфига)
**Метрика:** Duplicates cleaned up within 30min of creation.

---

### 8. trustcall-style PATCH для UserMemory 🟢 Низкий приоритет

**Источник:** LangMem (JSON Patch RFC 6902 на существующих записях)

**Проблема:** При обновлении UserMemory/PreferenceMemory текущий flow:
LLM extract → cosine dedup → UPDATE if similar, ADD if new.
Дубликаты всё ещё проскакивают, т.к. cosine similarity не ловит семантические пересечения.

**Решение:** LLM видит существующие записи и возвращает PATCH operations.

**Effort:** L
**Статус:** Отложено. Рассмотреть после Python Deprecation.

---

### 9. Extraction prompt quality (MemOS parity) 🔴 Высокий приоритет

**Источник:** Deep audit MemOS upstream (март 2026). 30+ промптов vs наши ~10.

**Проблема:** `unifiedSystemPrompt` не enforce'ит:
- Third-person perspective ("The user..." вместо "I...")
- Temporal resolution (relative→absolute dates)
- Pronoun resolution ("she"→"Caroline")
- Source attribution ([user viewpoint] vs [assistant viewpoint])

**Решение:** Расширить extraction prompt + добавить post-extraction enhancement.

**Effort:** S (промпт-изменения, без architectural changes)
**Метрика:** 0 first-person pronouns в stored memories. Relative dates resolved.

---

### 10. Source attribution tagging 🟡 Средний приоритет

**Источник:** MemOS `STRATEGY_STRUCT_MEM_READER_PROMPT`

**Проблема:** Все extracted facts хранятся одинаково — нельзя отличить user fact от assistant inference.
Chat prompt имеет "Four-Step Verdict" но не может отфильтровать AI views, т.к. тегов нет.

**Решение:** Добавить `source` field в extracted facts: "user" | "assistant" | "inferred".

**Effort:** S (новое поле в промпте + DB column)
**Метрика:** Chat prompt может отфильтровать assistant inferences.

---

### 11. Strategy-based chunking 🟡 Средний приоритет

**Источник:** MemOS `mem_reader_strategy_prompts.py`

**Проблема:** Go use фиксированные 10-message windows. MemOS поддерживает content_length vs message_count стратегии.

**Решение:** Configurable chunking strategy через `AddParams.ChunkStrategy`.

**Effort:** M (new chunking logic in add pipeline)
**Метрика:** Длинные диалоги (>10 messages) корректно разбиваются.

---

## Порядок реализации

```
Phase 0 (critical prompt fixes, 1 день):
  9. Extraction prompt quality (S) ← БЛОКЕР КАЧЕСТВА

Phase 1 (quick wins, 1-2 дня):
  4. LLM semaphore (S)
  2. Embedding cache (S)
  7. Periodic interval tuning (S)

Phase 2 (architecture, 1-2 недели):
  1. Soft-delete / temporal invalidation (M)
  5. Inline preference extraction (M)
  10. Source attribution (S)
  11. Strategy-based chunking (M)

Phase 3 (observability, 1 неделя):
  3. OTel tracing (M)

Deferred:
  6. Skill chat_history (S) — низкий impact
  8. trustcall PATCH (L) — после Python Deprecation
```

---

## Конкурентный контекст (март 2026)

### Сводная таблица add pipeline

| Измерение | MemDB | mem0 | Graphiti | Letta | LangMem |
|-----------|-------|------|----------|-------|---------|
| LLM вызовов на add | 1 (unified) | 2 | 4-6 | 0 | 1 |
| Стадий pipeline | 10+ | 2 | 8 | 1 | 1 |
| Dedup слоёв | 3 (hash+cosine+near-dup) | 2 (cosine+LLM) | 2 (cosine+LLM+union-find) | 1 (hash) | 1 (PATCH) |
| Content classifier | Есть | Нет | Нет | Нет | Нет |
| Buffer batching | Есть | Нет | bulk API (no dedup) | Нет | Background executor |
| Hallucination filter | Есть | Нет | Нет | Нет | Нет |
| Temporal model | valid_at | Нет | expired_at (полный) | Нет | Нет |
| Memory types | 7 (LTM/WM/UM/Skill/Tool/Episodic/Pref) | 3 | 3 (Episode/Entity/Community) | 3 | 3 |
| Async ingestion | Redis Streams + semaphore | asyncio | semaphore_gather | Нет | ThreadPool |
| Hot cache | Redis VSET HNSW (~1ms) | Нет | Нет | Redis embed cache | Нет |
| Observability | slog → **OTel (planned)** | PostHog | OTel Tracer | OTel @trace_method | LangSmith |
| Soft-delete | merged only → **full (planned)** | SQLite audit | expired_at (полный) | Нет | Нет |
| Rate limiting | addSem (sync) → **llmSem (planned)** | Нет | semaphore_gather | Нет | Нет |
| Backend latency | Go ~15-30ms | Python ~200ms | Python ~300ms | Python ~100ms | Python ~200ms |

### Ключевые паттерны конкурентов (reference)

| Паттерн | Конкурент | Файл | Применимость |
|---------|-----------|------|-------------|
| Two-phase LLM extract+update | mem0 | `mem0/memory/main.py` | Наш unified prompt лучше (1 вызов vs 2) |
| Temporal invalidation (expired_at) | Graphiti | `edge_operations.py` | **Брать** → п.1 |
| Union-find canonical merge | Graphiti | `bulk_utils.py` | Уже есть в reorganizer |
| trustcall JSON Patch | LangMem | `extraction.py` | Отложено → п.8 |
| Background async reflection | LangMem | `reflection.py` | Уже есть (goroutines) |
| Batch embedding + hash dedup | Letta | `connectors.py` | Уже есть (content_hash) |
| semaphore_gather | Graphiti | `helpers.py` | **Брать** → п.4 |
| Redis embedding cache | Letta | `passage_manager.py` | **Брать** → п.2 |
| @trace_method (OTel) | Letta | service methods | **Брать** → п.3 |
