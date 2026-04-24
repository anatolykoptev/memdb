# Port Target #1: VEC_COT Multi-Vector Search

**Source:** MemOS `src/memos/memories/textual/tree_text_memory/retrieve/searcher.py`
**Technique:** Embed each CoT sub-question independently; union of embedding vectors used for HNSW probe
**Effort:** M (3-5 days)
**Expected F1 lift:** +5-7 points on complex multi-hop LoCoMo questions (per ROADMAP-SEARCH.md Phase 1 estimate)

---

## What We Port and Why

MemDB already decomposes queries into sub-questions (D7 CoT, PR #47). The problem: decomposed sub-questions are only used to expand the *result union by ID* after retrieval — the underlying HNSW vector search is still done with one embedding (the original query).

MemOS uses sub-question embeddings as additional vector probes:

```python
# searcher.py:625-645
if self.vec_cot:
    queries = self._cot_query(query, mode=mode, context=parsed_goal.context)
    if len(queries) > 1:
        cot_embeddings = self.embedder.embed(queries)  # embed all sub-questions
    cot_embeddings.extend(query_embedding)  # original query embedding appended
else:
    cot_embeddings = query_embedding

# All cot_embeddings passed to retrieve() → each becomes an HNSW probe
```

Result: for "What does Alice like about travel?", MemOS probes:
1. "What destinations does Alice prefer?" → hits geolocation memories
2. "What travel activities does Alice enjoy?" → hits activity memories
3. "What are Alice's travel companions?" → hits person memories
4. "What does Alice like about travel?" (original) → general travel memories

Union → rerank. vs MemDB today which only probes with #4.

---

## Source Code Reference

File: `/home/krolik/src/compete-research/memos/src/memos/memories/textual/tree_text_memory/retrieve/searcher.py`

```python
# Line 68: VEC_COT flag from search_strategy config
self.vec_cot = search_strategy.get("cot", False) if search_strategy else False

# Lines 625-645: VEC_COT embedding expansion
cot_embeddings = []
if self.vec_cot:
    queries = self._cot_query(query, mode=mode, context=parsed_goal.context)
    if len(queries) > 1:
        cot_embeddings = self.embedder.embed(queries)
    cot_embeddings.extend(query_embedding)
else:
    cot_embeddings = query_embedding

# Lines 1240-1265: _cot_query decompose function
response_text = self.llm.generate(messages, temperature=0, top_p=1)
response_json = parse_json_result(response_text)
if not response_json["is_complex"]:
    return [query]
else:
    return response_json["sub_questions"][:split_num]
```

Prompt (`mem_search_prompts.py:1`):
```
SIMPLE_COT_PROMPT: "is_complex": true/false, "sub_questions": [...]
```

---

## Where It Lives in MemDB

MemDB already has:
- `memdb-go/internal/search/cot_decompose.go` — D7 CoT decomposition (PR #47)
- `memdb-go/internal/search/service.go` — main search pipeline
- `memdb-go/internal/embedder/` — ONNX embedder

Integration point: `service.go` step **before** HNSW probe, after CoT decomposition.

Current flow in `service.go`:
```go
// step 1: query embedding (single vector)
queryVec, _ := s.Embedder.Embed(ctx, query)

// step 2: CoT decompose (D7)
subQuestions, _ := CotDecompose(ctx, s.LLM, query)  // returns []string

// step 3: vector search (single probe today)
results, _ := s.VectorSearch(ctx, queryVec, params)
```

VEC_COT modification:
```go
// step 2b: embed each sub-question
subVecs := make([][]float32, len(subQuestions))
for i, sq := range subQuestions {
    subVecs[i], _ = s.Embedder.Embed(ctx, sq)
}

// step 3: multi-probe vector search
allVecs := append([][]float32{queryVec}, subVecs...)
results := s.VectorSearchMulti(ctx, allVecs, params)  // NEW
// VectorSearchMulti: parallel goroutines per vec, union by ID, dedup
```

New function needed: `internal/search/vec_multi.go: VectorSearchMulti(ctx, vecs [][]float32, params) → []SearchResult`

---

## Test Plan

1. Unit: `VectorSearchMulti` with 2 vectors returns union (not intersection) of results, deduped by ID
2. Unit: single-vector input → same results as current `VectorSearch`
3. Integration: LoCoMo multi-hop question (category 3, currently our weakest) — verify recall improvement
4. Regression: run `evaluation/locomo/` harness on 50 QA sample with `mode=smart` → aggregate F1 ≥ current 0.238 + 0.05
5. Latency: p95 search latency < 150ms for 3 sub-questions (3 goroutines × 15ms HNSW each)
6. Gate: env var `MEMDB_SEARCH_VEC_COT=true` (default false until validated)

---

## Risk

- **LLM latency**: CoT decomposition adds ~200-400ms LLM call. Mitigate: only in `mode=smart` (not `fast`). Fast path unchanged.
- **Embedding cost**: 2-3× ONNX calls per smart search (~30-60ms extra). Acceptable.
- **Dedup correctness**: Union of results from multi-probe may return duplicates. Must dedupe by memory ID before MMR. Already done in D7 result merge — reuse same logic.
- **Recall explosion**: 3× candidates may overflow `top_k * InflateFactor` budget. Solution: cap per-probe top_k at `InflateFactor * top_k / num_probes`, then union.

## Estimated Effort

- `internal/search/vec_multi.go`: ~80 LOC (new file)
- `internal/search/service.go`: ~20 LOC change (call VectorSearchMulti when mode=smart + sub-questions > 1)
- `internal/config/config.go`: 1 env var
- Tests: ~60 LOC
- **Total: ~160 LOC, 3-5 days**
