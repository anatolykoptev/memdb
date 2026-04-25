# Search Pipeline — Competitive Analysis (April 2026)

> Snapshot dated April 2026. Current state in master [ROADMAP.md](../../ROADMAP.md).
> Pending search backlog: [docs/backlog/search.md](../backlog/search.md)
>
> Sources: langchain-ai/langchain (`vectorstores/utils.py`),
> MemTensor/MemOS (`modules/retrieval/rerank/mmr_reranker.py`),
> arXiv:2504.19413 (mem0 paper), memobase benchmark README, MemDB internal measurements.

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

## LoCoMo Benchmarks (LLM Judge Score, April 2026)

> Метрика LLM Judge Score (gpt-4o, 1=correct). Источники: arXiv:2504.19413 (mem0 paper), memobase benchmark README (github.com/memodb-io/memobase).

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
| **MemDB (цель)** | **> 76** | — | — | — | — | **Go** | **+VEC_COT+profile** |

> *Zep* = self-reported by Zep team in memobase repo issue #101, not independently re-benchmarked. Original Zep score was 65.99 (independently benchmarked by Memobase). Use with caution.

**MemDB M7 — separate measurement (NOT LLM Judge, NOT directly comparable to the table above):**

| System | Metric | Scope | Note |
|--------|--------|-------|------|
| MemDB M7 (single-conv F1 reference) | F1 0.238 / hit@k 0.769 on 199 QA conv-26 only | NOT directly comparable to LLM Judge — different metric, different scope |

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
- ❌ **VEC_COT** → [docs/backlog/search.md](../backlog/search.md) Фаза 1 (отдельная задача, не покрыта Phase D)

### Из mem0:
- ✅ **LLM entity extraction** — реализован (entity linking в add_fine)
- ✅ **Dedup-before-insert** — реализован (3-layer dedup)
- ✅ **Unified API** — `add/search/get_all/delete`

---

## Функциональное сравнение: Go vs Python vs MemOS vs Memobase vs Graphiti (April 2026)

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
