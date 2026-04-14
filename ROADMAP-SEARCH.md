# Search Pipeline Roadmap

> Качество поиска — главный дифференциатор MemDB. Цель: LoCoMo > 75 (обогнать MemOS 73.31).
>
> _Составлен: февраль 2026, обновлён март 2026._

---

## Реализовано ✅

### Real MMR (февраль 2026)

| Фича | Реализация | Файлы |
|------|-----------|-------|
| **queryVec relevance** | `querySimCache[i] = cosine(item, queryVec)` — обе метрики MMR используют одну метрику | `dedup.go` |
| **Phase 1 prefill по cosine** | Prefill сортирует по `querySimCache`, а не по смешанному `item.Score` | `dedup.go` |
| **Lambda 0.7** | 70% relevance / 30% diversity. Configurable через `SearchParams.MMRLambda` | `config.go`, `service.go` |
| **Exponential penalty** | `exp(alpha*(sim−0.9))`, alpha=5.0. Soft block для почти-дубликатов | `dedup.go` |
| **Text similarity guard** | dice+tfidf+bigram — ловит перефразированные дубли, которые cosine не ловит | `dedup.go` |
| **Bucket-aware MMR** | text и preference memory — отдельные квоты внутри одного MMR прохода | `dedup.go` |
| **WorkingMemory → ActMem** | Параллельный горутин, cosine rerank, soft threshold (relativity−0.10) | `service.go`, `postgres.go` |
| **Fulltext+Vector hybrid** | BM25 + halfvec HNSW merge | `service.go`, `search_queries.go` |
| **Graph recall** | AGE Cypher по ключам и тегам | `service.go` |
| **HNSW session params** | `iterative_scan=relaxed_order`, `ef_search=100` | `postgres.go` |
| **halfvec search queries** | `embedding::halfvec(1024)` — HNSW index вместо seq scan | `search_queries.go` |

### Temporal decay (февраль 2026)

| Фича | Реализация |
|------|-----------|
| **Score decay** | `score *= exp(-α * days)` — configurable `DecayAlpha` per profile |
| **Recency boost** | через `importance_score` + retrieval tracking |
| **Search profiles** | `inject` (alpha=0.01, slow decay), `deep` (alpha=0.002, very slow) |

---

## Конкурентный анализ: MMR

> Deep-анализ `langchain-ai/langchain` (`vectorstores/utils.py`) и `MemTensor/MemOS` (`modules/retrieval/rerank/mmr_reranker.py`).

| Фича | LangChain | MemOS | **MemDB** |
|------|-----------|-------|-----------|
| Relevance = cosine(item, query) | ✅ | ✅ | ✅ |
| Diversity = max cosine(item, selected) | ✅ | ✅ | ✅ |
| Lambda configurable | ✅ `fetch_k` | ✅ `lambda_mult` | ✅ `MMRLambda` |
| Exponential penalty | ❌ | ❌ | ✅ |
| Phase 1 prefill | ❌ | ❌ | ✅ top-2 |
| Text similarity guard | ❌ | ❌ | ✅ dice+tfidf+bigram |
| Bucket логика | ❌ | ❌ | ✅ text vs preference |
| NxN матрица | lazy O(k·n) | lazy | upfront O(n²) |
| fetch_k / inflate | ✅ `fetch_k >> k` | ❌ | ✅ `InflateFactor=5` |
| WorkingMemory | ❌ | ❌ | ✅ parallel goroutine |
| BM25+Vector hybrid | ❌ | ❌ | ✅ |
| Graph recall | ❌ | ❌ | ✅ AGE Cypher |
| Latency | Python ~200ms | Python ~300ms | **Go ~15-30ms** |

### Уникальные преимущества MemDB (нет у конкурентов)

1. **Text similarity guard** — dice+tfidf+bigram ловит перефразы, которые cosine пропускает
2. **Exponential penalty** — sim=0.95 получает реальный штраф (конкуренты: линейный maxSim)
3. **Bucket-aware MMR** — text и pref отдельными квотами в одном проходе
4. **Phase 1 prefill** — детерминированный seed из top-2 перед итерацией
5. **WorkingMemory separation** — session context не замусоривает LTM
6. **Go backend** — x5-10 быстрее (15ms vs 200ms)

---

## Benchmarks (LoCoMo, февраль 2026)

| Система | Score | Backend | Search |
|---------|-------|---------|--------|
| **MemOS** | **73.31** | Python | VEC_COT |
| **mem0** | 66.90 | Python | basic |
| LangMem | 58.10 | Python | — |
| OpenAI Memory | 52.75 | — | — |
| **MemDB** (цель) | **> 75** | **Go** | **HNSW+BM25+Graph** |

### Путь к лидерству

```
Текущий оценочный score MemDB: ~68-70
  + VEC_COT search              → +5-7 points
  + Prompt quality gaps (1.5)   → +3-5 points
  + Sparse vector prefs         → +1-2 points
─────────────────────────────────────────
Цель: > 75 (превзойти MemOS 73.31)
```

Уже получили (включено в оценку ~68-70):
- Real MMR → +3-4 points
- Temporal decay → +1-2 points
- Dedup-before-insert → +2-3 points
- Memory Reorganizer → +3-5 points

---

## Фаза 1 — VEC_COT Search ❌ НЕ НАЧАТО

**Impact:** +5-7 points LoCoMo — самый большой оставшийся рычаг.
**Источник:** MemOS v2.0.7 (`mem_search_prompts.py`, `searcher.py`). Единственная значимая фича поиска, где MemOS лучше нас.

**Суть:** LLM декомпозирует сложный запрос на sub-queries, параллельный поиск, merge+rerank.

```go
// internal/search/vec_cot.go
// Когда mode="smart":
// 1. LLM COT_PROMPT: "What does Alice like about travel?"
//    → {is_complex: true, sub_questions: [
//        "What destinations does Alice prefer?",
//        "What travel activities does Alice enjoy?",
//        "What are Alice's travel companions?"
//    ]}
// 2. Параллельный search по каждому sub-query (горутины)
// 3. Merge + dedup + rerank финального списка
//
// func CotDecompose(ctx, llmClient, query) ([]string, error)
// func SearchWithCot(ctx, params) (*SearchResult, error)
```

Добавить `mode` параметр в SearchParams: `fast` (текущий), `smart` (VEC_COT).

**Effort:** M (1-2 недели)
**Метрика:** LoCoMo > 73 на complex multi-hop queries.

---

## Фаза 1.5 — Prompt Quality Gaps (MemOS parity) 🔴 ВЫСОКИЙ ПРИОРИТЕТ

**Impact:** +3-5 points LoCoMo — второй по важности рычаг после VEC_COT.
**Источник:** Deep audit MemOS upstream (март 2026). Основной разрыв — качество промптов.

### 1.5.1 Post-retrieval memory enhancement

**Суть:** Перед передачей в chat LLM — LLM-pass для disambiguation+fusion retrieved memories.

```go
// internal/search/enhance.go
// Resolve: "she" → "Caroline", "last night" → "November 25, 2025"
// Merge: related entries
// Filter: keep only query-relevant
```

**MemOS эквивалент:** `MEMORY_RECREATE_ENHANCEMENT_PROMPT`
**Effort:** S (1 новый файл, 1 LLM вызов)

### 1.5.2 Stage-aware iterative retrieval

**Суть:** Заменить flat expansion prompt на 3-stage pipeline:
- Stage 1: Entity-anchored discriminative noun phrases
- Stage 2: Pronoun/temporal resolution + canonicalization
- Stage 3: Hypothesis generation when still unanswerable

**MemOS эквивалент:** `STAGE1/2/3_EXPAND_RETRIEVE_PROMPT`
**Effort:** S (замена промпта в `iterative_retrieval.go`)

### 1.5.3 Query rewriting before embedding

**Суть:** Для multi-turn sessions — rewrite query to be self-contained.
"What did he say about it?" → "What did John say about the project deadline?"

**MemOS эквивалент:** `QUERY_REWRITE_PROMPT`
**Effort:** S (1 новая функция, вызов перед embedding)

### 1.5.4 Post-retrieval relevance filter

**Суть:** LLM-фильтр после vector search — отсечь keyword-matched но context-unrelated memories.

**MemOS эквивалент:** `MEMORY_FILTERING_PROMPT` + `MEMORY_REDUNDANCY_FILTERING_PROMPT`
**Effort:** S (опциональный LLM pass)

---

## Фаза 2 — Sparse Vectors для Preferences ❌ НЕ НАЧАТО

**Impact:** +1-2 points LoCoMo.

**Суть:** Qdrant 1.16+ поддерживает sparse vectors. Hybrid dense+sparse для preference search.

```go
// Qdrant: named vectors {"dense": [...], "sparse": {"indices": [...], "values": [...]}}
// BM25-style sparse encoding для preference keywords
// Fusion: RRF(dense_rank, sparse_rank)
```

**Effort:** M
**Зависимость:** Qdrant 1.16.2 уже развёрнут.

---

## Что брать из конкурентов

### Из MemOS:
- ✅ **Memory Reorganizer** — реализован
- ❌ **VEC_COT** → Фаза 1
- ❌ **Prompt quality gaps** → Фаза 1.5
- ❌ **MEMORY_RECREATE_ENHANCEMENT** → 1.5.1
- ❌ **3-stage iterative retrieval** → 1.5.2
- ❌ **Query rewriting** → 1.5.3
- ❌ **Post-retrieval filter** → 1.5.4

### Из mem0:
- ✅ **LLM entity extraction** — реализован (entity linking в add_fine)
- ✅ **Dedup-before-insert** — реализован (3-layer dedup)
- ✅ **Unified API** — `add/search/get_all/delete`

---

## Фаза 3 — NLI Reranker (optional) ❌ НЕ НАЧАТО

**Impact:** +1-2 points LoCoMo.
**Источник:** MemOS (`extras/nli_model/`). Опциональный lightweight NLI model server.

**Суть:** Natural Language Inference определяет entailment/contradiction между запросом и памятью — точнее cosine similarity.

**Effort:** L (требует отдельный model server, ONNX NLI модель)
**Статус:** Отложено. LLM rerank (`llm_rerank.go`) покрывает большую часть этой функциональности.

---

## Функциональное сравнение: Go vs Python vs MemOS (март 2026)

| Фича | Go (MemDB) | Python (legacy) | MemOS v2.0.7 |
|------|------------|-----------------|--------------|
| Real MMR (lambda, exp penalty, buckets) | ✅ | ❌ (text dedup) | ❌ |
| RRF merge (k=60) | ✅ | ❌ (max-score) | ❌ |
| Temporal decay (180d half-life) | ✅ | ❌ (commented out) | ❌ |
| Graph scoring (key/tag/contradict) | ✅ | ❌ | ❌ |
| Named profiles (inject/deep) | ✅ | ❌ | ❌ |
| Iterative retrieval + caching | ✅ (2min TTL) | ✅ (no cache) | ✅ (no cache) |
| LLM rerank + caching | ✅ (5min TTL) | ✅ (no cache) | ✅ (no cache) |
| Relativity threshold filtering | ✅ (0.5 default) | ❌ | ❌ |
| BM25+Vector hybrid | ✅ | ❌ | ❌ |
| **CoT query decomposition** | ❌ | ❌ | ✅ |
| **NLI reranker** | ❌ | ❌ | ✅ (optional) |
| `search_priority` dict | ❌ | ❌ | ✅ |
| Latency | **15-30ms** | ~200ms | ~300ms |

---

## Что НЕ делаем

- ❌ Graph traversal search mode (AGE traversal слишком медленный для real-time, используем graph recall как boost)
- ❌ Параметрическая память / LoRA (требует GPU)
