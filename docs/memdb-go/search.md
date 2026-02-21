# Search — Поисковый пайплайн

Пакет `internal/search/` реализует унифицированный поисковый сервис, используемый как REST-обработчиком, так и MCP-сервером.

## SearchService

```go
type SearchService struct {
    postgres    *db.Postgres
    qdrant      *db.Qdrant
    embedder    embedder.Embedder
    LLMReranker LLMRerankConfig
    Iterative   IterativeConfig
    Profiler    *scheduler.Profiler
}
```

**Создание:** `NewSearchService(pg, qd, emb, logger)`  
**Проверка готовности:** `CanSearch()` — возвращает `true` если `embedder != nil && postgres != nil`

---

## Полный поисковый пайплайн `Search(ctx, SearchParams)`

### SearchParams

| Поле | По умолчанию | Описание |
|---|---|---|
| `Query` | обязательно | Поисковый запрос |
| `UserName` | обязательно | user_name в БД (= cube_id для дефолтного куба) |
| `CubeID` | — | ID куба для ответа |
| `AgentID` | — | Фильтр по agent_id |
| `TopK` | 10 | Бюджет text_mem |
| `SkillTopK` | 3 | Бюджет skill_mem |
| `PrefTopK` | 6 | Бюджет pref_mem |
| `ToolTopK` | 6 | Бюджет tool_mem |
| `Dedup` | `"no"` | Алгоритм: `no`, `sim`, `mmr` |
| `MMRLambda` | 0.7 | Вес релевантности в MMR (0=diversity, 1=relevance) |
| `DecayAlpha` | 0.0039 | Коэффициент временного спада (half-life ~180 дней) |
| `Relativity` | 0 | Минимальный порог score (0 = отключён) |
| `NumStages` | 0 | Этапы итеративного расширения (0=выкл, 2=fast, 3=fine) |

---

## Пошаговый разбор пайплайна

### Шаг 1: Векторизация запроса

```go
embeddings, _ := s.embedder.Embed(ctx, []string{p.Query})
queryVec := embeddings[0]
```

### Шаг 2: Токенизация и временная метка

- `TokenizeMixed(query)` — извлекает токены для fulltext search
- `BuildTSQuery(tokens)` — строит PostgreSQL `tsquery`
- `DetectTemporalCutoff(query)` — определяет временной срез из запроса
  - Например: "вчера", "на прошлой неделе", "в январе" → timestamp cutoff

### Шаг 3: Параллельный поиск (errgroup)

Все источники опрашиваются **параллельно**:

| Источник | Метод | Условие |
|---|---|---|
| text vector | `VectorSearch` / `VectorSearchWithCutoff` | всегда |
| text fulltext | `FulltextSearch` / `FulltextSearchWithCutoff` | если есть tsquery |
| skill vector | `VectorSearch(SkillScopes)` | если `IncludeSkill` |
| skill fulltext | `FulltextSearch(SkillScopes)` | если `IncludeSkill` + tsquery |
| tool vector | `VectorSearch(ToolScopes)` | если `IncludeTool` |
| tool fulltext | `FulltextSearch(ToolScopes)` | если `IncludeTool` + tsquery |
| pref (Qdrant) | `SearchByVector(explicit_preference, implicit_preference)` | если `IncludePref && qdrant != nil` |
| WorkingMemory | `GetWorkingMemory` (last 20 nodes) | всегда |
| graph by keys | `GraphRecallByKey(tokens)` | если len(tokens) > 0 |
| graph by tags | `GraphRecallByTags(tokens)` | если len(tokens) >= 2 |

**TopK inflation** для dedup-режимов: `TopK * 5` (InflateFactor) чтобы было больше кандидатов.

### Шаг 3.5: BFS graph traversal

После параллельного поиска (серийно):
- Берёт top-5 text_vec результатов как seed-узлы
- `GraphBFSTraversal(seedIDs, depth=2)` — обход графа памяти на 2 уровня вглубь
- Нефатальный: ошибки логируются, пайплайн продолжает с пустым BFS

### Шаг 4: Слияние результатов

- `MergeVectorAndFulltext(vec, ft)` — объединяет vector + fulltext по ID, дедупликация + score fusion
- `MergeGraphIntoResults(merged, graph)` — добавляет graph recall результаты

### Шаг 5: Форматирование

`FormatMergedItems(items, includeEmbedding)` — конвертирует в `[]map[string]any` + строит `embeddingByID` карту для последующего rerank.

### Шаг 6: Cosine rerank

`ReRankByCosine(queryVec, items, embByID)` — заменяет сжатые scores из PolarDB прямым cosine similarity с query вектором.

### Шаг 6.1: LLM rerank (опциональный)

`LLMRerank(ctx, query, items, cfg)` — если `LLMReranker.APIURL != ""` и items > 1:
- Отправляет query + top memories в LLM
- LLM выдаёт переупорядочённый список
- Результат кэшируется (в памяти, TTL 2 минуты)

### Шаг 6.2: Итеративное расширение (опциональный)

`IterativeExpand(ctx, query, items, embedFn, cfg)` — порт MemOS AdvancedSearcher:
- Если `NumStages > 0 && LLM настроен`:
  1. LLM анализирует текущие результаты: достаточно ли для ответа?
  2. Если нет → генерирует 1-3 `retrieval_phrases` (sub-queries)
  3. Для каждой фразы: embed → vector search → merge новых результатов
  4. Повторяет до `NumStages` раз или пока LLM не скажет `can_answer=true`
- Результаты expansion кэшируются (TTL 2 мин) по SHA256 от (query + stage + node IDs)
- Нефатальный: ошибки возвращают исходный список без изменений

### Шаг 6.5: Temporal decay

`ApplyTemporalDecay(items, now, alpha)` — взвешенная комбинация:
```
final_score = 0.75 * cosine + 0.25 * recency
recency = exp(-alpha * days_since_last_access)
```
- `alpha = 0.0039` → half-life ~180 дней
- WorkingMemory (ActMem) исключён — для него важна именно свежесть
- Применяется к text, skill, tool (не к pref — у них нижние scores)

### Шаг 7: Фильтрация по relativity

`FilterByRelativity(items, threshold)` — удаляет items с score < threshold.  
Для pref применяется смягчённый порог: `threshold - 0.10`.

### Шаг 8: Фильтрация pref по качеству

`FilterPrefByQuality(items)` — удаляет слишком короткие preferences (`len < 30` символов).

### Шаг 9: Дедупликация

| Режим | Метод | Описание |
|---|---|---|
| `no` | `DedupByText` | Exact-text dedup (safety net для vector+fulltext merge) |
| `sim` | `DedupSim(items, topK)` | Greedy similarity — убирает items с cosine > 0.85 к уже выбранным |
| `mmr` | `DedupMMR(combined, topK, prefTopK, queryVec, lambda)` | Maximal Marginal Relevance — балансирует релевантность и разнообразие |

### Шаг 10: Cross-source dedup

`CrossSourceDedupByText(text, skill, tool, pref)` — удаляет из skill/tool/pref дубли текстов, уже присутствующих в text_mem (text имеет приоритет).

### Шаг 11: Trim по бюджету

`TrimSlice(items, k)` — оставляет только первые `k` results каждого типа.

### Шаг 12: WorkingMemory → ActMem

- Берёт последние 20 WM узлов, считает cosine с queryVec
- Применяет смягчённый relativity порог (`threshold - 0.10`)
- Возвращает как `act_mem` в ответе (сессионный контекст)

### Шаг 12.5: User profile injection

Если `Profiler != nil`: `Profiler.GetProfile(ctx, cubeID)` — читает кэшированный профиль из Redis (~0ms), добавляет в `profile_mem` ответа.

### Шаг 13: Async retrieval_count increment

Fire-and-forget goroutine: `postgres.IncrRetrievalCount(ids, now)` — увеличивает счётчик обращений и `importance_score` для всех возвращённых узлов.

---

## Структура ответа

```json
{
  "text_mem":  [{"cube_id": "...", "memories": [...], "total_nodes": N}],
  "skill_mem": [{"cube_id": "...", "memories": [...], "total_nodes": N}],
  "pref_mem":  [{"cube_id": "...", "memories": [...], "total_nodes": N}],
  "tool_mem":  [{"cube_id": "...", "memories": [...], "total_nodes": N}],
  "act_mem":   [...],
  "para_mem":  [],
  "profile_mem": "..." 
}
```

---

## Алгоритмы dedup

### DedupSim (`dedup.go`)

Greedy-алгоритм:
1. Сортировка по score (убывание)
2. Выбираем item, если `max(cosine(item, already_selected)) < threshold` (0.85)
3. Продолжаем до `topK` выбранных

### DedupMMR (`dedup.go`)

Maximal Marginal Relevance:
```
MMR_score(item) = lambda * cosine(item, query) - (1-lambda) * max(cosine(item, selected))
```
- `lambda = 0.7` (70% релевантность, 30% разнообразие)
- Экспоненциальный штраф для similarity > 0.9: `exp(alpha * (sim - 0.9))`
- Одновременно обрабатывает text + pref items в едином пространстве

### CosineSimilarity / CosineSimilarityMatrix

L2-нормализация + dot product. Матрица NxN для MMR строится один раз.

---

## Конфигурация (`config.go`)

| Константа | Значение | Описание |
|---|---|---|
| `DefaultTextTopK` | 10 | Бюджет text |
| `DefaultSkillTopK` | 3 | Бюджет skill |
| `DefaultPrefTopK` | 6 | Бюджет pref |
| `DefaultToolTopK` | 6 | Бюджет tool |
| `InflateFactor` | 5 | Множитель TopK для dedup-режимов |
| `DefaultMMRLambda` | 0.7 | Вес релевантности в MMR |
| `DefaultMMRAlpha` | 5.0 | Exponential penalty multiplier |
| `DefaultDecayAlpha` | 0.0039 | half-life ~180 дней |
| `DecaySemanticWeight` | 0.75 | Вес cosine в итоговом score |
| `DecayRecencyWeight` | 0.25 | Вес recency в итоговом score |
| `MaxDecayAgeDays` | 730 | Максимальный возраст (2 года) для decay |
| `GraphRecallLimit` | 50 | Максимум кандидатов из graph recall |
| `WorkingMemoryLimit` | 20 | Максимум WM узлов |
| `CacheTTL` | 30s | TTL кэша поисковых результатов |

---

## Memory scopes

| Scope | Типы | Используется в |
|---|---|---|
| `TextScopes` | LongTermMemory, UserMemory, EpisodicMemory | text_mem |
| `SkillScopes` | SkillMemory | skill_mem |
| `ToolScopes` | ToolSchemaMemory, ToolTrajectoryMemory | tool_mem |
| `GraphRecallScopes` | LTM, UserMemory, SkillMemory, EpisodicMemory | graph recall |
| `PrefCollections` | explicit_preference, implicit_preference | pref_mem (Qdrant) |

---

---

## Сравнение с конкурентами: Search Pipeline

### mem0 (Python)

| Аспект | mem0 | memdb-go |
|---|---|---|
| Поиск | Только vector (pgvector / Qdrant) | **Vector + Fulltext + Graph BFS** |
| Параллельность | asyncio (GIL) | **errgroup goroutines** |
| Temporal decay | ❌ Нет | ✅ exp(-alpha * days), half-life 180d |
| Dedup в поиске | Нет | ✅ sim / mmr |
| Graph memory | ✅ Neo4j (entity triplets) | ⚠️ Только BFS по memory_edges |
| Cross-source dedup | ❌ Нет | ✅ text > skill > tool > pref |
| Profile injection | ❌ Нет | ✅ Memobase-style `profile_mem` |
| Working Memory | ❌ Нет отдельного слоя | ✅ `act_mem` с cosine rerank |
| LLM rerank | ❌ Нет | ✅ Опциональный |
| Iterative expansion | ❌ Нет | ✅ MemOS AdvancedSearcher port |

**Где mem0 сильнее:** Entity-graph поиск — при запросе "Что говорил Алексей о проекте, которым руководит Мария?" mem0g traverses Neo4j граф по entity-отношениям. Наш BFS работает по плоским `memory_edges`, не понимая entity-семантику.

**Цель Go-миграции:** Построить entity-relation extraction в `add_fine.go` → писать (subject, relation, object) рёбра в AGE → в поиске добавить entity-graph traversal наряду с BFS.

---

### Graphiti/Zep (Python + Go Cloud)

| Аспект | Graphiti | memdb-go |
|---|---|---|
| Архитектура памяти | Temporal KG: episodes + entities + communities | Flat nodes + memory_edges |
| Community detection | ✅ Кластеризация entity в сообщества, суммаризация | ❌ Нет |
| Conflict detection | ✅ Temporal edges + contradiction check | ✅ LLM action=delete |
| Search | Hybrid: semantic + keyword + graph | **Hybrid + temporal cutoff + iterative** |
| Retrieval latency | Sub-200ms (заявлено) | ~1–5ms VSET + ~50ms pgvector |
| Язык сервера | Python | **Go** |

**Где Graphiti сильнее:** Community summaries — периодически Graphiti кластеризует связанные entity в "сообщества" и генерирует их суммари. Это даёт ответы на абстрактные вопросы ("Расскажи о работе этого человека") без точного vector match. У нас есть `EpisodicMemory` для сессий, но нет cross-session community summaries.

**Цель Go-миграции:** В `reorganizer_consolidate.go` добавить community detection — Union-Find уже реализован, нужно добавить суммаризацию кластеров и хранить как `CommunityMemory` тип.

---

### MemOS (Python) — AdvancedSearcher

| Аспект | MemOS AdvancedSearcher | memdb-go IterativeExpand |
|---|---|---|
| Принцип | Итеративное расширение через LLM sub-queries | ✅ Портирован |
| BM25 | ✅ Нативный BM25 в поиске | ✅ PostgreSQL fulltext (ts_rank) |
| Mixture search | ✅ Weighted ensemble | ⚠️ MergeVectorAndFulltext (простое слияние) |
| Graph recall | ✅ | ✅ |
| Кэш expansion фраз | ❌ | ✅ SHA256 TTL 2 мин |

**Где MemOS сильнее:** Настоящий weighted mixture search с обученными весами для разных типов запросов. Наш `MergeVectorAndFulltext` использует фиксированные коэффициенты без ML-оптимизации.

**Цель Go-миграции:** Реализовать `WeightedMerge` с настраиваемыми весами (vector/ft/graph) через конфиг вместо hardcoded логики.

---

### LangMem (Python)

**Где LangMem сильнее:** Storage-agnostic архитектура через `LangGraph BaseStore` — любой бэкенд (in-memory, PostgreSQL, Redis, Chroma). Мы жёстко привязаны к PolarDB + Qdrant.

**Цель Go-миграции:** Вынести интерфейс `VectorStore` (аналог BaseStore) для поиска, чтобы поддерживать pluggable backends без изменения SearchService.

---

## Temporal detection (`temporal.go`)

`DetectTemporalCutoff(query)` — определяет временной срез из запроса на естественном языке:
- "yesterday", "last week", "in January" → `time.Time`
- Если временная метка найдена — поиск выполняется с `VectorSearchWithCutoff` (добавляет WHERE `created_at >= cutoff`)
