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

---

## M8/M9 follow-ups (added 2026-04-26)

### Tier 2 — Active diagnostics

#### cat-2 multi-hop F1 = 0.091 — D3 hub-and-spoke topology

**Source:** M8 S3 GRAPH v2 diagnosis (PRs #81 + #84). M8 restored M7 parity (F1 0.097)
but did not reach the 0.18 stretch gate. Root diagnosis: **D3 reorganizer hub-and-spoke
topology** — the reorganizer may produce only consolidation edges (sibling→hub) rather
than CAUSES/SUPPORTS edges that D2 can traverse.

**Action:** Dedicated D3 relation detector diagnosis — does it produce `CAUSES`/`SUPPORTS`
edges that D2 can traverse, or only consolidation edges that turn D2 into "all siblings
of consolidator"?

**Effort:** M (2-3 days).

---

#### D7 + D11 share fanout pattern — extract helper when D12/D13 ships

**Source:** M8 code review. Two copies of fanout-to-scope exist in D7 and D11.
Project rule: 3rd copy → extract; only 2 copies now so the rule is NOT yet triggered.

**Action:** When D12 or D13 ships, extract `fanoutSubqueryToScope` helper at that point.
Do not extract preemptively — wait for the 3rd copy.

**Effort:** M (refactor only, no behavior change).

---

### Tier 3 — Perf / M10 candidates (go-code lifted patterns)

#### Pre-compute CE rerank scores at ingest

**Source:** go-code commits `520b3e9` + `417ed1b`. M8 follow-up.

**What:** `internal/search/cross_encoder_rerank.go` currently fires per query
(~100-400ms). Pre-compute pair-wise CE scores during D3 reorganizer (background),
persist in `memos_graph."Memory".properties->>'ce_score_topk'`. Query-time CE →
graph lookup.

**Why:** -50-300ms p95 chat. Compound with M7 factual -52% → up to 3-5× total speedup.

**Effort:** M. M10 candidate (also listed in ROADMAP.md M10 table).

---

#### PageRank on `memory_edges`

**Source:** go-code commit `30373c9`. M8 follow-up.

**What:** Background goroutine computes PageRank on `memory_edges`, stores result in
`Memory.properties->>'pagerank'`, boosts D1 rerank.

**Why:** cat-1 + cat-3 retrieval recall lift via better top-K ranking of well-connected
memories.

**Effort:** S. M10 candidate (also listed in ROADMAP.md M10 table).

---

#### `BulkCopyInsert` / `CypherWriter` for AGE writes

**Source:** go-code commits `79b8791` + `a1adb38`. M8 follow-up.

**What:** Direct text-format COPY INTO AGE, bypass Cypher parser,
`synchronous_commit=off`. Target: heavy AGE write paths (Stage 3 ingest, D3 batch,
S10 structural edges).

**Why:** 2-5× speedup on ingest-heavy paths. Stage 3 full-set run currently takes 3-5h;
this reduces ingest bottleneck.

**Effort:** M. M10 candidate (also listed in ROADMAP.md M10 table).
