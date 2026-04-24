# MemDB Roadmap: LLM Cost Optimization & Quality

> Цель: уделать конкурентов по качеству при меньших затратах LLM-токенов.
> Составлен: февраль 2026. Обновлён: апрель 2026 (v2.0.0).
>
> **Статус на v2.0.0 (апрель 2026)**: LoCoMo hit@20 = **0.700** — выше Mem0 (0.65) и MemOS (0.60), на уровне Claude-3-Opus+RAG (0.72). Полный Phase D quality-stack shipped — см. [ROADMAP-SEARCH.md](../../ROADMAP-SEARCH.md), [ROADMAP-GO-MIGRATION.md](../../ROADMAP-GO-MIGRATION.md), [evaluation/locomo/MILESTONES.md](../../evaluation/locomo/MILESTONES.md). LLM cost optimization часть ниже — отдельный long-term трек, не перекрывается с v2.0.0.

---

## Текущее состояние

### LLM-вызовы MemDB (февраль 2026)

| Вызов | Путь | Файл | ~Токены/вызов | Кэш |
|-------|------|------|---------------|-----|
| ExtractAndDedup | HOT (каждый fine-add) | `llm/extractor.go` | 700-2800 | Нет |
| LLMRerank | HOT (каждый search) | `search/llm_rerank.go` | 150-450 | 5 мин |
| IterativeExpand | HOT (опц. search) | `search/iterative_retrieval.go` | 250-600/stage | 2 мин |
| EpisodicSummary | COLD (background) | `handlers/add_episodic.go` | 400-1800 | Нет |
| Consolidation | COLD (periodic) | `scheduler/reorganizer_consolidate.go` | 300-800 | Нет |
| WM Compaction | COLD (periodic) | `scheduler/reorganizer_wm_compact.go` | 650-1900 | Нет |
| UserProfile | COLD (background) | `scheduler/profiler.go` | 350-1200 | 1ч Redis |
| FeedbackAnalysis | COLD (on event) | `scheduler/reorganizer_feedback.go` | 250-650 | Нет |
| PrefExtraction | COLD (on event) | `scheduler/reorganizer_prefs.go` | 300-800 | Нет |

**Оценка при 100 юзерах x 10 adds/день: ~3.5M токенов/день**

### LLM-вызовы конкурентов (сравнение)

| Операция | MemDB | mem0 | Zep/Graphiti | Letta | LangMem | Cognee |
|----------|-------|------|-------------|-------|---------|--------|
| Add (write) | **1** | 1-3 | **3-5** | 1+ | 1 | 2/chunk |
| Search (read) | 0-2 | 0-1 | 0 | 1 | 0 | 0 |
| Dedup | В рамках add | +1/match | +1 | Нет | Debounce | Cache |
| Embedding | **Local ONNX** | API ($) | API ($) | API ($) | API ($) | API ($) |

### Наши преимущества (уже реализовано)

- **Local ONNX embedder** — zero cost embeddings (конкуренты платят за API)
- **Unified Postgres + AGE** — граф+вектор+SQL в одной БД (mem0/Zep требуют Neo4j)
- **1 LLM-вызов на add** — unified ExtractAndDedup (mem0: 1-3, Zep: 3-5)
- **Temporal decay без LLM** — только Zep имеет temporal, но ценой 3-5 LLM-вызовов
- **Search profiles** — серверные пресеты (inject/default/deep) для разных клиентов
- **Real MMR** — text guard + exponential penalty + bucket logic (нет у конкурентов)
- **Go backend** — 5-10x быстрее Python (15-30ms vs 200-300ms)

---

## Фаза 6.5 — Chat Pipeline Migration to Go (1 неделя)

> Перенос chat endpoints с Python на нативный Go. Устраняет 200-300ms proxy overhead.

### Перенесено
- `POST /product/chat/complete` — non-streaming RAG chat
- `POST /product/chat/stream` — SSE streaming RAG chat
- `POST /product/chat` — alias for stream
- Streaming LLM client (`llm/stream.go`)
- Language detection (en/zh/ru)
- Prompt templates (Four-Step Memory Safety Verdict)
- `<think>` tag parsing for reasoning extraction
- Post-chat memory addition (fire-and-forget)

### Осталось на Python (deferred)
- `POST /product/chat/stream/playground` — enhanced two-stage search, references, suggestions, timing
- `POST /product/suggestions` — suggestion generation
- `POST /product/feedback` — feedback analysis

### Проверено (2026-03-02)
- Live test: chat/complete — нативный Go, 200ms LLM latency, think tags parsing OK
- Live test: chat/stream — SSE streaming, reasoning/text segments OK
- Live test: validation — proper 400 errors for missing fields
- Live test: auth — 401/403 without credentials, OK with internal service secret
- Live test: history — 20-msg truncation, context carryover working
- Live test: add_message_on_answer — fire-and-forget post-add, no errors in logs
- Lint: все 5 issues исправлены (cognitive complexity, goconst, magic numbers)
- Tests: +18 новых (thinkParser, parseThinkTags, detectLang, buildSystemPrompt, filterMemories)

### Потенциальные баги (найдены при анализе Python)
1. **OuterMemory strict filter** — Go удаляет ВСЕ OuterMemory из chat context;
   Python гибче — держит их при фильтрации, гарантируя minNum personal. Если internet
   search включён (не playground), Go потеряет web-контент. Пока не критично — internet
   search в chat/complete и chat/stream не используется.
2. **Multi-cube search** — Go использует `ReadableCubeIDs[0]` для search (только первый
   куб). Python аналогично — оба ищут только в первом кубе. Для multi-cube chat нужна
   агрегация результатов из нескольких кубов.
3. **Dedup strategy** — Go хардкодит `Dedup: "no"` (без MMR dedup в chat context).
   Python использует дефолт search handler. Разница в поведении при дублирующихся
   воспоминаниях.

### Playground migration scope (для будущей фазы)
Playground — ~1400 LOC Python с 12 стадиями:
1. Fast search (top_k=20)
2. Preference markdown rendering
3. Goal parser (query rephrase + internet_search decision)
4. Beginner guide mode
5. Deep search (top_k=100) with rephrased query
6. Memory dedup & supplement (target 50 memories)
7. Enhanced prompt (P/O blocks with IDs, timestamps, tone/verbosity)
8. Streaming with reference tracking (`[refid:memoryID]`)
9. Timing metrics (speed_improvement %)
10. Further suggestions (LLM-generated follow-ups)
11. Post-processing (reference extraction, DingDing, scheduler)
12. Memory addition (query + response)

---

## Фаза 7 — LLM Cost Reduction: Quick Wins (1-2 недели)

> Снизить LLM-расходы на 50-60% без потери качества.

### 7.1 Classifier gate перед ExtractAndDedup

**Проблема:** каждый `POST /product/add` вызывает LLM для extraction. 60-70% сообщений
("ok", "thanks", чистый код, логи) не содержат запоминаемых фактов.

**Решение:** маленькая модель (ONNX BERT или rule-based classifier) определяет
"стоит ли вызывать LLM" до ExtractAndDedup.

```
handlers/add_fine.go:
  1. Parse messages
  2. NEW: classifyWorthExtracting(messages) → bool
  3. If false → skip LLM, fast-path embed + store as-is
  4. If true → ExtractAndDedup (как сейчас)
```

**Варианты реализации (от простого к сложному):**
1. **Rule-based** — длина < 50 chars, regex на casual patterns, ratio code/prose → skip
2. **TF-IDF classifier** — обучить на наших данных (labeled: has_facts / no_facts)
3. **Small ONNX model** — distilbert-base fine-tuned, ~5ms inference

**Ожидаемый эффект:** ~60% reduction в ExtractAndDedup LLM-вызовах.

**Файлы:** `internal/handlers/add_classifier.go` (NEW), `add_fine.go` (gate before LLM)

### 7.2 Embedding-only dedup (без LLM для очевидных случаев)

**Проблема:** dedup-решения сейчас все идут через LLM (ExtractAndDedup unified call).
Но 80-90% dedup-кейсов — очевидные: cosine > 0.95 = точный дубль, cosine < 0.80 = точно новый.

**Решение:** трёхзонная маршрутизация:
- cosine > 0.95 → auto-skip (дубль), **без LLM**
- cosine < 0.80 → auto-add (новый), **без LLM**
- 0.80-0.95 → LLM decides (как сейчас)

```
llm/extractor.go:
  1. Embed new facts
  2. VectorSearch top-5 candidates
  3. For each: if cosine > 0.95 → skip; if cosine < 0.80 → add
  4. Only ambiguous zone → LLM ExtractAndDedup
```

**Ожидаемый эффект:** 80-90% dedup-решений без LLM.

**Файлы:** `internal/llm/extractor.go` (pre-filter), `handlers/add_fine.go` (routing)

### 7.3 Conditional LLM rerank

**Проблема:** LLMRerank вызывается на каждом search, даже когда не нужен.

**Решение:** skip rerank когда:
- Результатов <= 3 (нечего переранжировать)
- Все результаты > 0.92 cosine (уже высокое качество)
- Profile = "inject" (latency-sensitive)

```
search/service.go:
  if p.LLMRerank && s.LLMReranker.APIURL != "" && len(textFormatted) > 3 {
    topScore := getTopCosineScore(textFormatted)
    if topScore < 0.92 {  // rerank only when quality is uncertain
      textFormatted = LLMRerank(...)
    }
  }
```

**Ожидаемый эффект:** ~40% reduction в LLMRerank вызовах.

**Файлы:** `internal/search/service.go` (conditional gate)

### 7.4 Episodic summarization sampling

**Проблема:** каждый fine-add генерирует episodic summary. Короткие сессии (1-2 turns)
и технические сессии (чистый код) тратят LLM впустую.

**Решение:** skip episodic summary когда:
- Session < 3 turns
- Content < 200 chars
- > 80% content is code blocks

**Ожидаемый эффект:** ~30% reduction в episodic LLM-вызовах.

**Файлы:** `internal/handlers/add_episodic.go` (gate)

---

## Фаза 8 — ColBERT Reranking: Zero-Cost Quality (2-3 недели)

> Заменить LLM rerank на локальную модель. Качество ≈ LLM, стоимость = 0.

### 8.1 ColBERT/Cross-encoder reranker (ONNX)

**Проблема:** LLMRerank тратит 150-450 токенов на каждый search. При 1000 searches/день
= 300K токенов/день только на reranking.

**Решение:** Jina-ColBERT-v2 или BGE-reranker-v2-m3 как ONNX модель.
Late interaction scoring: ~5ms inference, multilingual, качество на уровне GPT-3.5 reranking.

```
internal/search/rerank_colbert.go (NEW):
  type ColBERTReranker struct {
    session *onnxruntime.Session
  }
  func (r *ColBERTReranker) Rerank(query string, docs []string) []float64
```

**Интеграция:**
- `SearchParams.LLMRerank` → `SearchParams.Reranker` (enum: "none", "colbert", "llm")
- Default: "colbert" (бесплатно), "llm" только для deep profile
- Fallback: если ColBERT unavailable → LLM → cosine

**Файлы:**
- `internal/search/rerank_colbert.go` (NEW)
- `internal/search/service.go` (switch reranker)
- `internal/embedder/colbert.go` (NEW — ONNX session management)

### 8.2 Query complexity classifier

**Проблема:** IterativeExpand (multi-stage LLM) вызывается для всех запросов с NumStages > 0.
Простые запросы ("как зовут мою собаку?") не нуждаются в multi-hop.

**Решение:** embedding-based complexity classifier:
- query embedding → cosine with "complex query" cluster centroid
- Simple queries (single-entity lookup) → skip expansion
- Complex queries (multi-hop, temporal, comparative) → expand

**Ожидаемый эффект:** ~50% reduction в IterativeExpand LLM-вызовах.

**Файлы:** `internal/search/query_classifier.go` (NEW)

---

## Фаза 9 — Distilled Extraction Model (1-2 месяца)

> Убрать зависимость от внешних LLM API для extraction. Zero API cost per add.

### 9.1 Fine-tune small model для extraction

**Проблема:** ExtractAndDedup — самый дорогой LLM-вызов (700-2800 токенов, каждый add).
Все конкуренты зависят от GPT/Gemini API. Кто первый уйдёт на local model — выиграет по cost.

**Подход:**
1. Собрать dataset: 5-10K пар (conversation → extracted facts) из наших production данных
2. Fine-tune Gemma-3n (2B effective) или Phi-4-mini (3.8B) на extraction task
3. Quantize GGUF Q4/Q8 для inference на CPU
4. Deploy как ONNX или llama.cpp sidecar

**Целевое качество:** 90%+ F1 vs Gemini Flash extraction на наших данных.

**Архитектура:**
```
memdb-go → local extraction model (sidecar, :8082)
         → Gemini API (fallback for complex cases)

Routing: confidence < 0.7 from local model → retry with Gemini
```

**Файлы:**
- `internal/llm/local_extractor.go` (NEW — local model client)
- `internal/llm/extractor.go` (routing: local → API fallback)
- `scripts/training/` (NEW — dataset prep + fine-tuning scripts)
- `cmd/extractor/` (NEW — standalone extraction model server)

### 9.2 Inter-Cascade: transfer знаний от дорогих моделей

**Идея (из академии):** сохранять "reasoning strategies" от Gemini Pro/GPT-4 и использовать
для augmentation дешёвых моделей (Flash/local). Снижает дорогие API вызовы на ~48%.

```
internal/llm/cascade.go (NEW):
  type Cascade struct {
    strategies map[string]string  // cached reasoning patterns
    expensive  *Client            // Gemini Pro
    cheap      *Client            // Gemini Flash / local
  }
  // 1. Try cheap model
  // 2. If confidence < threshold → try expensive + cache strategy
  // 3. Next similar query → augment cheap with cached strategy
```

---

## Фаза 10 — Procedural Memory (2-3 месяца)

> Blue ocean: ни один конкурент не учит паттерны поведения. Это $150M feature.

### 10.1 Pattern detection из corrections

**Проблема (цитата HN, Feb 2026):** "Mem0 stores memories but doesn't learn user patterns."
Все конкуренты извлекают факты. Никто не учит поведенческие паттерны из corrections.

**Пример:** пользователь 3 раза поправляет "используй snake_case" →
MemDB автоматически создаёт procedural memory: "User prefers snake_case naming convention."

**Архитектура:**
```
internal/scheduler/pattern_detector.go (NEW):
  type PatternDetector struct {
    postgres *db.Postgres
    llm      *llm.Client
  }

  // 1. Track corrections (feedback with action=update)
  // 2. Cluster by topic (embedding similarity)
  // 3. If cluster.size >= 3 → LLM: extract rule
  // 4. Store as ProceduralMemory node
  // 5. Inject in search results with high priority
```

**Типы паттернов:**
- Naming conventions (snake_case, camelCase)
- Code style preferences (tabs vs spaces, bracket placement)
- Communication preferences ("be concise", "explain in detail")
- Tool preferences ("use vim", "use VS Code")
- Workflow patterns ("always run tests before commit")

### 10.2 Proactive memory (ProMem pattern)

**Идея (из arxiv 2601.04463):** iterative self-questioning для полноты extraction.
Вместо одного прохода — LLM задаёт себе вопросы: "что ещё важного в этом диалоге?"

**Когда включать:** только для deep profile / длинных сессий (> 10 turns).
Для inject profile — overhead не оправдан.

```
internal/llm/proactive.go (NEW):
  func ProactiveExtract(messages []Message) []Fact {
    facts := ExtractFacts(messages)           // pass 1
    questions := GenerateProbes(facts)         // "what else?"
    additionalFacts := ExtractWithProbes(messages, questions)  // pass 2
    return deduplicate(facts, additionalFacts)
  }
```

### 10.3 Memory-R1: RL-trained memory manager

**Идея (из arxiv 2508.19828):** вместо rule-based логики (add/update/delete) —
обученный через RL агент, который УЧИТСЯ когда и что запоминать.

**Длинный горизонт.** Требует:
- RL training infrastructure
- Reward signal from downstream task performance
- A/B testing framework

---

## Фаза 11 — Enterprise Features (3-6 месяцев)

> Для $150M valuations нужны enterprise capabilities.

### 11.1 Federated memory

Privacy-preserving memory sharing между агентами без раскрытия содержимого.
Агент A знает что Агент B имеет релевантную память, но видит только embedding similarity,
не текст. Запрос на доступ → owner approves → memory shared.

### 11.2 Memory audit trail

Полный audit log: кто, когда, что добавил/изменил/удалил. SOC 2 compliance.
Apache AGE edges с timestamps уже поддерживают историю.

### 11.3 Multi-tenant isolation

Полная изоляция данных между тенантами. Row-level security в Postgres.
Отдельные Qdrant collections per tenant.

### 11.4 Memory analytics dashboard

- Token consumption per user/cube
- Memory quality metrics (dedup rate, contradiction rate)
- Search quality metrics (click-through, relevance feedback)
- Cost attribution per feature (extraction, rerank, expansion)

---

## Метрики успеха

| Фаза | KPI | Текущее | Цель |
|------|-----|---------|------|
| 7 | LLM tokens/day (100 users) | ~3.5M | **< 1.5M** (−57%) |
| 7 | ExtractAndDedup calls skipped | 0% | **60%** |
| 8 | Rerank cost | 300K tok/day | **0** (ColBERT) |
| 8 | Search quality (LoCoMo) | ~70 | **> 75** |
| 9 | External API dependency | 100% | **< 20%** (local model) |
| 10 | Pattern detection rate | 0% | **> 50%** of repeated corrections |
| 11 | Enterprise readiness | MVP | SOC 2, multi-tenant, audit |

---

## Конкурентная позиция после всех фаз

| Feature | MemDB (target) | mem0 | Zep | Letta |
|---------|----------------|------|-----|-------|
| LLM calls per add | **0-1** (local + classifier) | 1-3 | 3-5 | 1+ |
| LLM calls per search | **0** (ColBERT) | 0-1 | 0 | 1 |
| Embedding cost | **$0** (local ONNX) | API ($) | API ($) | API ($) |
| Dedup strategy | **Embedding + LLM hybrid** | LLM only | LLM only | None |
| Graph backend | **Postgres/AGE (unified)** | Neo4j (separate) | Neo4j (required) | None |
| Temporal awareness | **Native (decay + valid_at)** | None | Bi-temporal | None |
| Pattern learning | **Procedural memory** | None | None | None |
| Offline capable | **Full** | Ollama only | No | BYOK |
| Latency (search p95) | **< 50ms** | ~200ms | ~300ms | ~500ms |

---

## Инсайты от конкурентов (февраль 2026)

> Анализ mem0 (mem0ai/mem0) и Memobase (memodb-io/memobase) — что стоит взять.

### От Memobase

| Идея | Суть | Куда встраивать | Приоритет |
|------|------|-----------------|-----------|
| **Buffer zone** | Копить сообщения в буфер, flush через LLM только по порогу (N сообщений / T секунд). Снижает LLM-вызовы при частых коротких add'ах. | Фаза 7: gate перед ExtractAndDedup | Высокий |
| **Schema-guided extraction** | YAML-конфиг с темами/подтемами для extraction (`topics: [{topic: "work", subtopics: ["company", "role"]}]`). LLM извлекает только по схеме — точнее, меньше hallucinations. | Фаза 9: как альтернатива fine-tune | Средний |
| **Profile delta tracking** | Сохранять diff каждого processing event (что добавлено/обновлено/удалено). Для аудита и отладки. | Фаза 11.2: memory audit trail | Низкий |
| **CLI reprocess** | CLI-утилиты для ре-обработки старых данных (`memobase reprocess`). | ✅ Уже есть: `POST /product/admin/reprocess` | Готово |

### От mem0

| Идея | Суть | Куда встраивать | Приоритет |
|------|------|-----------------|-----------|
| **Custom extraction prompts** | Пользователь задаёт свой system prompt для extraction. Позволяет настроить под домен (медицина, юриспруденция, код). | Фаза 7/9: параметр `extraction_prompt` в configure | Средний |
| **Graph memory (Neo4j)** | Structured entity-relation graph (`User --WORKS_AT--> Company`). Полезен для multi-hop queries. | ✅ Уже есть: Apache AGE (Postgres) | Готово |
| **Telemetry + analytics** | Встроенный OpenTelemetry для трекинга LLM calls, latency, token usage. | Фаза 11.4: memory analytics dashboard | Низкий |

### Что у нас лучше (подтверждено анализом)

- **1 unified LLM call** vs mem0 (2 calls: extract → dedup) и Memobase (3 calls per flush)
- **Confidence filtering** (< 0.65 dropped) — ни mem0, ни Memobase не фильтруют по confidence
- **Temporal anchoring** (`valid_at`) — mem0 вообще без temporal, Memobase не парсит даты
- **Full Go scheduler** (8 task types, priority queues, retry+DLQ) — mem0 OSS без background worker, Memobase только sync flush
- **Local ONNX embedder** — оба конкурента зависят от embedding API

### Рекомендуемый порядок внедрения

1. **Buffer zone** → встроить в Фазу 7 (самый быстрый win по LLM cost)
2. **Custom extraction prompts** → добавить в configure API (низкий effort, высокий UX impact)
3. **Schema-guided extraction** → эксперимент в Фазе 9 (альтернатива fine-tune для domain-specific)
4. **Profile delta tracking** → в Фазу 11 (enterprise feature, audit compliance)

---

## Приоритизация

```
NOW:   Фаза 7 (quick wins + buffer zone) — 1-2 недели, −57% LLM cost
NEXT:  Фаза 8 (ColBERT rerank)           — 2-3 недели, zero-cost reranking
THEN:  Фаза 9 (local extraction + schema) — 1-2 месяца, zero API dependency
LATER: Фаза 10 (procedural mem)           — 2-3 месяца, blue ocean feature
LAST:  Фаза 11 (enterprise + audit trail) — 3-6 месяцев, $150M features
```
