# Search Pipeline Backlog

> Pending search quality work. For shipped features see CHANGELOG.md.
> Competitive analysis (MMR comparison, functional table vs rivals):
> [docs/competitive/2026-04-search-pipeline-vs-rivals.md](../competitive/2026-04-search-pipeline-vs-rivals.md)
>
> Master roadmap: [ROADMAP.md](../../ROADMAP.md)

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

## Путь к лидерству (roadmap context)

```
Текущий оценочный score MemDB: ~68-70
  + Cross-encoder rerank (apr 2026)  → +3-5 points  ✅ Реализовано (Фаза 3)
  + VEC_COT search                    → +5-7 points  ❌ Фаза 1 (pending)
  + Prompt quality gaps (1.5)         → +3-5 points  ✅ Закрыта (v2.0.0)
  + Sparse vector prefs               → +1-2 points  ❌ Фаза 2 (pending)
─────────────────────────────────────────
Цель: > 75 (превзойти MemOS 73.31)
```

---

## Что НЕ делаем

- ❌ Graph traversal search mode (AGE traversal слишком медленный для real-time, используем graph recall как boost)
- ❌ Параметрическая память / LoRA (требует GPU)
