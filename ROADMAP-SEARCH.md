# Search Pipeline Roadmap

> Качество поиска — главный дифференциатор MemDB. Цель: LoCoMo > 75 (обогнать MemOS 73.31).
>
> _Составлен: февраль 2026, обновлён апрель 2026 (M6 prompt ablation)._

---

## ✅ 2026-04-25 (M7 compound lift) — ЗАВЕРШЕНО

**Контекст:** M6 ablation (2026-04-24) показала что default `cloudChatPromptEN` — **#1 quality bottleneck**, +51% aggregate F1 (0.053 → 0.080) от одного 10-строчного prompt override. Экспериментальная ветка `exp/locomo-qa-prompt` содержит рабочий Fix 1, но не мерджено — решено переносить серверно и compound'ить с ingest-фиксом, затем измерить единый lift.

### Приоритеты на завтра (по ROI × effort)

**P1 — Port QA prompt в Go как `answer_style: "factual"`** (1-2h, high ROI)
- Добавить `AnswerStyle *string` в `handlers.fullAddRequest` / chat complete payload
- Новая константа `factualQAPromptEN` в `chat_prompt_tpl.go`
- Switch в `buildSystemPrompt`: если `answer_style == "factual"` → использовать фактологический шаблон
- Юнит-тест: шаблон выбирается по param
- Ветка `feat/answer-style-factual`, PR → main
- После merge — harness передаёт `{..., "answer_style": "factual"}` вместо `system_prompt`, получаем те же +51% F1 без завязки на Python-специфичный prompt

**P2 — Ingest mode=raw для LoCoMo** (30m, high ROI)
- Изменить `evaluation/locomo/ingest.py`: `"mode": "fast"` → `"mode": "raw"`
- Это даст per-message granularity (~100 memories per session vs 3 windows сейчас)
- Ожидаемо: **hit@k 0.000 → 0.3-0.5** (восстанавливается retrieval)
- Замер на 50 QA сэмпле + compare vs Fix 1
- **Compound hypothesis**: prompt fix + raw ingest = aggregate F1 0.15-0.20 (3× baseline)

**P3 — Диагностика 4096-char sliding window как product decision** (1h, medium ROI)
- `add_windowing.go:windowChars = 4096` — hardcoded константа
- Вопрос: зачем он для QA-type workloads? LoCoMo evidence refers to 1 turn, не 15 exchanges
- Варианты:
  - Сделать configurable per-request (`window_chars` param в add request)
  - Или per-mode: `fast` остается 4096, появляется `fast-qa` с 512
  - Или дефолт снижаем до 1024 (~4 messages), sliding более "гранулярный"
- Решение нужно будет обсудить до коммита — влияет на все существующие клиенты (vaelor, go-nerv)

**P4 — Full LoCoMo прогон после P1+P2** (4-8h runtime, low effort)
- Если P1+P2 подтвердят compound lift на 50 QA → запустить `LOCOMO_FULL=1`
- 10 convs × 200 QA ≈ 2000 QA даст statistically-sound числа для сравнения с Mem0/Zep/MemOS
- Цель: пройти 0.15 aggregate F1 barrier, что ставит нас в один ряд с MemOS

### Что НЕ трогать завтра

- Модель (gemini-flash-lite → pro/3.x): прирост mini (~5%) относительно prompt+ingest fixes (~150%), отложить
- Embedder swap на BGE-M3 / Voyage: нет bottleneck — search работает, проблема в prompt/granularity
- CE rerank: план 2026-04-20 ещё актуален, но compound lift от P1+P2 может его закрыть, — мерить после
- Semantic tier promotion tuning (D3): ждёт естественной аккумуляции multi-topic данных

### Definition of done на день

- [x] `feat/answer-style-factual` merged в main (`c57cc904`)
- [x] LoCoMo harness обновлён на `mode=raw` + `answer_style=factual` (`52938d14`)
- [x] Прогон на 50 QA сэмпле — aggregate F1 0.238, hit@k 0.769 (`84e99d72`)
- [x] Решение по sliding window: configurable `window_chars` per-request (`841febc2`)
- [x] full-data прогон запущен в фоне (`chore/m7-stage3-full-locomo`, ~6h ETA)

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

## Benchmarks (LoCoMo, февраль 2026; обновлено апрель 2026)

> Метрика LLM Judge Score (gpt-4o, 1=correct). Источники: arXiv:2504.19413 (mem0 paper), memobase benchmark README (github.com/memodb-io/memobase), ROADMAP-SEARCH.md ранее.

| Система | Score | Single-Hop | Multi-Hop | Open Domain | Temporal | Backend | Search |
|---------|-------|------------|-----------|-------------|----------|---------|--------|
| **Memobase v0.0.37** | **75.78** | 70.92 | 46.88 | 77.17 | **85.05** | Python | profile-structured |
| **Zep*** | **75.14** | 74.11 | 66.04 | 67.71 | 79.79 | closed | Graphiti |
| **MemOS** | 73.31 | — | — | — | — | Python | VEC_COT |
| mem0-Graph | 68.44 | 65.71 | 47.19 | 75.71 | 58.13 | Python | vector+graph |
| **mem0** | 66.88 | 67.13 | 51.15 | 72.93 | 55.51 | Python | basic vector |
| Zep (original) | 65.99 | 61.70 | 41.35 | 76.60 | 49.31 | closed | — |
| Memobase v0.0.32 | 70.91 | 63.83 | 52.08 | 71.82 | 80.37 | Python | profile |
| LangMem | 58.10 | 62.23 | 47.92 | 71.12 | 23.43 | Python | — |
| OpenAI Memory | 52.75 | 63.79 | 42.92 | 62.29 | 21.71 | — | — |
| **MemDB M7** | **~70** | — | — | — | — | **Go** | **HNSW+BM25+CE+Graph** |
| **MemDB (цель)** | **> 76** | — | — | — | — | **Go** | **+VEC_COT+profile** |

> *Zep* = updated numbers from Zep team, issue #101 in memobase repo. Original Zep = 65.99.
> MemDB M7 = estimated from aggregate F1 0.238 hit@k 0.769 on 50-QA sample (not direct LLM Judge comparison; scale differs).

### Путь к лидерству

```
Текущий оценочный score MemDB: ~68-70
  + Cross-encoder rerank (apr 2026)  → +3-5 points  ← NEW
  + VEC_COT search                    → +5-7 points
  + Prompt quality gaps (1.5)         → +3-5 points
  + Sparse vector prefs               → +1-2 points
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

## Фаза 1.5 — Prompt Quality Gaps (MemOS parity) ✅ ЗАКРЫТА (v2.0.0, апрель 2026)

**Impact (planned):** +3-5 points LoCoMo — второй по важности рычаг после VEC_COT.
**Источник:** Deep audit MemOS upstream (март 2026). Основной разрыв — качество промптов.
**Результат:** Все 5 sub-items shipped в v2.0.0 Phase D. Production metrics — см. `evaluation/locomo/MILESTONES.md`. LoCoMo hit@20 = 0.700 (выше Mem0/MemOS).

### 1.5.1 Post-retrieval memory enhancement ✅ shipped as D10

- File: `internal/search/answer_enhance.go` (+ `answer_enhance_test.go`)
- PR: #42, commit `7338dd25`
- Env: `MEMDB_SEARCH_ENHANCE=true`
- Verified live: `EnhancedAnswer="counseling or mental health"` surface at rank 0 for "What does Caroline do for work?"

### 1.5.2 Stage-aware iterative retrieval ✅ shipped as D5

- File: `internal/search/staged_retrieval.go` (+ test)
- PR: #45, commit `37e78273`
- Env: `MEMDB_SEARCH_STAGED=true`
- Pipeline: coarse (vector+CE) → refine (LLM top-10) → justify (LLM drop IRRELEVANT)

### 1.5.3 Query rewriting before embedding ✅ shipped as D4

**Суть:** Для multi-turn sessions — rewrite query to be self-contained.
"What did he say about it?" → "What did John say about the project deadline?"

**MemOS эквивалент:** `QUERY_REWRITE_PROMPT`
**Effort:** S (1 новая функция, вызов перед embedding)

- File: `internal/search/query_rewrite.go` (+ test)
- PR: #44, commit `95d6f1c0`
- Env: `MEMDB_QUERY_REWRITE=true`
- Verified live: `"What does Caroline do for work?"` → `"Caroline's occupation"` (conf 0.9)

### 1.5.4 Post-retrieval relevance filter ✅ shipped as part of D5

Stage 3 of D5 staged retrieval includes the "drop IRRELEVANT" step, which is exactly this filter. Single LLM pass produces both justification + relevance flag per shortlist item.

### 1.5.5 Bonus — not originally in plan but shipped

- **D7 CoT query decomposition** — multi-part queries split into atomic sub-questions + union-by-id merge. PR #47.
- **D3 Hierarchical reorganizer** — port of Python `tree_text_memory/organize/` (4 modules ~1500 LOC). Raw → episodic → semantic tiers with LLM relation detector. PR #40.
- **D6 Pronoun + temporal resolution in EXTRACTION** (not just retrieval). Stored memories are already resolved at write time. PR #48.
- **D8 Third-person + 22-category preference taxonomy** in extractor. PR #48.
- **D1 Temporal decay + importance scoring** in rerank (MemOS has decay; we add access_count boost). PR #34.
- **D2 Multi-hop AGE graph retrieval** via recursive CTE on `memory_edges`. PR #36.

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
- ✅ **Tree hierarchical memory (raw→episodic→semantic)** — D3 (v2.0.0)
- ✅ **Relation detector (CAUSES/CONTRADICTS/SUPPORTS/RELATED)** — D3 (v2.0.0)
- ✅ **MEMORY_RECREATE_ENHANCEMENT** — D10 (v2.0.0)
- ✅ **3-stage iterative retrieval** — D5 (v2.0.0)
- ✅ **Query rewriting** — D4 (v2.0.0)
- ✅ **Post-retrieval filter** — D5 stage 3 (v2.0.0)
- ✅ **Pronoun/temporal resolution in extraction** — D6 (v2.0.0)
- ✅ **Preference taxonomy (22 categories)** — D8 (v2.0.0)
- ✅ **Multi-hop graph retrieval** — D2 (v2.0.0)
- ✅ **CoT query decomposition** — D7 (v2.0.0)
- ❌ **VEC_COT** → Фаза 1 (отдельная задача, не покрыта Phase D)

### Из mem0:
- ✅ **LLM entity extraction** — реализован (entity linking в add_fine)
- ✅ **Dedup-before-insert** — реализован (3-layer dedup)
- ✅ **Unified API** — `add/search/get_all/delete`

---

## Фаза 3 — Cross-Encoder Reranker ✅ Реализовано (апрель 2026)

**Impact:** +3-5 points LoCoMo — перераспределяет большую часть работы с LLM rerank на специализированный cross-encoder, быстрее и дешевле.
**Источник:** Концептуальный аналог MemOS NLI reranker (`extras/nli_model/`). Реализовано на базе BGE-reranker-v2-m3 через `embed-server` и Cohere-совместимый endpoint `/v1/rerank` — лучшая модель и эксплуатационные характеристики, чем у NLI.

**Суть:** Cross-encoder оценивает entailment/relevance между запросом и памятью прямым совместным forward-pass — на голову точнее cosine similarity, на порядок дешевле LLM rerank (~100-400ms против ~3-4s).

**Реализация:**
- `memdb-go/internal/search/cross_encoder_rerank.go` — HTTP клиент + reorder логика (best-effort: любая ошибка → input без изменений).
- `memdb-go/internal/search/service.go` — step 6.05, между cosine rerank и LLM rerank. Применяется только к `text_mem` (skill/tool/pref дёшево сортируются cosine). Гейтится на `s.CrossEncoder.URL != ""`, отдельного `SearchParams` флага нет — env = kill-switch.
- `memdb-go/internal/config/config.go` — env vars `CROSS_ENCODER_URL` / `_MODEL` / `_TIMEOUT_MS` / `_MAX_DOCS` (defaults: `http://embed-server:8082`, `bge-reranker-v2-m3`, 2000ms, 50 docs).
- Защита: `MaxDocs` (default 50) ограничивает payload на embed-server. Тоже сохраняет items без `memory` ключа — они передаются через как есть.
- Логирование: `slog.Warn` при ошибках (в отличие от silent-fallback LLMRerank; CE ошибки редки и требуют ops-visibility).
- Метаданные: `metadata.relativity` перезаписывается CE score; выставляется `metadata.cross_encoder_reranked = true`.
- Timing: `ce_rerank` появляется в pipeline timing log рядом с `llm_rerank` и `iterative`.

**Верифицировано (smoke test на реальном embed-server):**
```json
Query: "what is a cat"
  "a cat is a small domestic feline mammal" → 5.77
  "cats purr when content" → -5.48
  "pasta comes in many shapes" → -11.00
```
Spread ~17 пунктов — модель чисто разделяет релевантное и нерелевантное.

**Тестовое покрытие:**
- 7 unit-тестов в `cross_encoder_rerank_test.go`: reorder-by-score, empty input, пустой URL, HTTP 5xx, timeout, `MaxDocs` cap, items без text-ключа.
- 3 integration-теста в `cross_encoder_step_test.go`: вызов через `postProcessResults` с реальным `httptest` сервером; skip при пустом URL; `ceRerankDur > 0` при активном CE.
- 3 config-теста в `internal/config/config_test.go`: defaults, env overrides, empty-env fallback.

**Почему лучше NLI из MemOS:**
- BGE-reranker-v2-m3 — state-of-the-art cross-encoder (MTEB), покрывает entailment/relevance одним сигналом.
- Переиспользует существующий `embed-server` — не требует нового model server.
- Cohere-совместимый API (`/v1/rerank`) — легко заменяется на commercial rerank (Cohere, Jina) без изменений кода.

---

## Функциональное сравнение: Go vs Python vs MemOS vs Memobase vs Graphiti (апрель 2026)

> Расширено по результатам M8 competitive survey (docs/competitive/2026-04-26-memory-frameworks-survey.md).

| Фича | Go (MemDB) | MemOS v2.0.7 | Memobase v0.0.37 | Graphiti | mem0 | LangMem |
|------|------------|--------------|-----------------|----------|------|---------|
| Real MMR (lambda, exp penalty, buckets) | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| RRF merge (k=60) | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Temporal decay (180d half-life) | ✅ | ❌ | ❌ | ✅ (valid_at) | ❌ | ❌ |
| Graph scoring (key/tag/contradict) | ✅ | ❌ | ❌ | ✅ (Neo4j) | ✅ (Neo4j opt) | ❌ |
| Named profiles (inject/deep) | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Iterative retrieval + caching | ✅ (2min TTL) | ✅ (no cache) | ❌ | ❌ | ❌ | ❌ |
| LLM rerank + caching | ✅ (5min TTL) | ✅ (no cache) | ❌ | ❌ | ❌ | ❌ |
| Relativity threshold filtering | ✅ (0.5 default) | ❌ | ❌ | ❌ | ❌ | ❌ |
| BM25+Vector hybrid | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Cross-encoder rerank** | ✅ BGE-v2-m3 | ✅ NLI opt | ❌ | ✅ openai/gemini | ❌ | ❌ |
| **CoT query decomposition** | ✅ (D7, union-by-id) | ✅ (VEC_COT) | ❌ | ❌ | ❌ | ❌ |
| **VEC_COT (sub-question embeddings as probes)** | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ |
| `search_priority` / boost weights | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ |
| **Profile taxonomy extraction** | ✅ 22-cat (D8) | ❌ | ✅ 8 topics | ❌ | ❌ | ❌ |
| **Profile context injection at extraction** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| **Expired_at soft-delete** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| **Embedding Redis cache** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Temporal LoCoMo (category 2) | — | — | **85.05%** | 79.79% (Zep*) | 55.51% | 23.43% |
| Latency | **15-30ms** | ~300ms | ~200ms | ~500ms | ~200ms | ~300ms |

---

---

### 2026-04-25 — Threshold field consistency + recall lift

- Fixed: `nativeChatRequest.Threshold` was bypassed when LoCoMo harness sent `relativity` (chat-endpoint reads `threshold`, search-endpoint reads `relativity`). Now both wired through `LOCOMO_RETRIEVAL_THRESHOLD`.
- Result: hit@k recovered from 0.000 to 0.769 on conv-26 (with raw-mode ingest + threshold=0.0 in eval).
- Architecturally: search uses `Relativity` (raw cosine threshold pre-filter), chat uses `Threshold` (post-filter over already-retrieved memories with default 0.30). Distinct semantics; both should match in eval setups.

---

## Что НЕ делаем

- ❌ Graph traversal search mode (AGE traversal слишком медленный для real-time, используем graph recall как boost)
- ❌ Параметрическая память / LoRA (требует GPU)
