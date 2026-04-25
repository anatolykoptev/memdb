# Memory Framework Competitive Survey — 2026-04-26

## Methodology

- Phase 2a discovery: go-search MCP was offline; fell back to direct repo clone + Bash grep per banked fallback rule
- Phase 2b structural analysis: go-code MCP (`explore`) on cloned repos + Bash file reads for key techniques
- Repos cloned to `/home/krolik/src/compete-research/<name>/` (depth 1)
- Bias: ranked by F1 lift potential per engineering effort, not by buzz/stars
- Benchmark numbers sourced from: Memobase LoCoMo README (`docs/experiments/locomo-benchmark/README.md`), mem0 paper arXiv:2504.19413, docs/competitive/2026-04-search-pipeline-vs-rivals.md

---

## Per-Framework Findings

### 1. MemOS

**Architecture:** Chinese-origin Python framework (MemTensor). Three-tier memory model: working memory (KV cache), textual tree memory (raw → episodic → semantic), parametric memory (fine-tuned weights). Core search is via `Searcher` class (`src/memos/memories/textual/tree_text_memory/retrieve/searcher.py`) which orchestrates: TaskGoalParser → GraphMemoryRetriever → (optional COT expansion) → reranker. Deployment uses Neo4j + Qdrant + Ollama/Azure LLM stack.

**Published F1 numbers (LoCoMo):** 73.31 aggregate (our docs/competitive/2026-04-search-pipeline-vs-rivals.md cites this; primary source MemOS paper/repo, no newer number found in cloned depth-1 repo). Memobase v0.0.37 claims 75.78 (via LLM Judge, arXiv:2504.19413 comparison table).

**Key techniques relevant to MemDB:**
- **VEC_COT search** (`searcher.py:68`, `mem_search_prompts.py:SIMPLE_COT_PROMPT`): when `search_strategy={"cot": True}`, LLM decomposes query into sub-questions, each embedded separately, union of embeddings passed to retriever. Gate: `if self.vec_cot: queries = self._cot_query(query)` → `cot_embeddings = self.embedder.embed(queries)`.
- **`search_priority` dict** (`searcher.py:84`): passed through to reranker; boosts items by metadata field match (e.g., `{"user_id": ..., "tags": ...}`).
- **HTTPBGEReranker** (`reranker/http_bge.py`): Cohere-compatible `/rerank` endpoint, same architecture as MemDB's `cross_encoder_rerank.go` — already implemented by us.
- **Multi-path parallel retrieval** (`_retrieve_paths`): A/B/C/D/E/F paths run in ThreadPoolExecutor, results merged.

**Code refs:**
- `src/memos/memories/textual/tree_text_memory/retrieve/searcher.py:68` — `vec_cot` flag
- `src/memos/memories/textual/tree_text_memory/retrieve/searcher.py:625-697` — VEC_COT embed expansion + parallel retrieval
- `src/memos/memories/textual/tree_text_memory/retrieve/searcher.py:1240-1265` — `_cot_query` decompose function
- `src/memos/templates/mem_search_prompts.py:1-40` — `SIMPLE_COT_PROMPT` and `COT_PROMPT` (with context)

**Gap vs MemDB:** MemDB has D7 CoT decomposition (PR #47) which splits queries and union-by-id merges results, but does NOT reuse the union of sub-query embeddings for vector search. MemOS approach embeds each sub-question independently → each becomes a separate HNSW probe → recall improvement on multi-hop questions. MemDB fires one embedding vector regardless.

**Port effort:** M (2-3 days). VEC_COT requires extending `SearchService` to embed multiple vectors and merge results before MMR.

**Expected MemDB F1 impact:** high (+5-7 points on complex multi-hop questions per docs/competitive/2026-04-search-pipeline-vs-rivals.md Phase 1 estimate). Already validated as the #1 remaining lift.

---

### 2. mem0

**Architecture:** Python framework (mem0ai, 47k stars). Single-store vector memory with optional Neo4j graph layer. Key flow: conversation → `ADDITIVE_EXTRACTION_PROMPT` (LLM) → extract facts as ADD list → cosine dedup against existing memories → optional graph entity upsert. No tree hierarchy, no tiered storage.

**Published F1 numbers (LoCoMo LLM Judge):**
- mem0: 66.88% overall (single-hop 67.13%, multi-hop 51.15%, open 72.93%, temporal 55.51%)
- mem0-Graph: 68.44% overall (from arXiv:2504.19413, verified in Memobase benchmark README)

**Key techniques relevant to MemDB:**
- **ADDITIVE_EXTRACTION_PROMPT** (`mem0/configs/prompts.py:468`): comprehensive extraction prompt covering user + assistant messages, temporal resolution against `Observation Date`, linked_memory_ids for relation tracking, dedup via "recently extracted" context window. Already partially ported to MemDB (D6 pronoun resolution, D8 preference taxonomy).
- **Entity store** (`main.py:390`, `_upsert_entity`): separate vector store for entity names, cosine threshold 0.95 for merge, `linked_memory_ids` payload field. MemDB has MENTIONS_ENTITY edges but no dedicated entity embedding store.
- **Session scope** (`main.py:317`): deterministic string from entity IDs (user_id + agent_id + run_id) — already covered by MemDB cube/user model.

**Code refs:**
- `mem0/configs/prompts.py:468-600` — `ADDITIVE_EXTRACTION_PROMPT` full text
- `mem0/memory/main.py:390-450` — entity store lazy init + `_upsert_entity` logic
- `mem0/memory/main.py:573-730` — `add()` flow: extract → dedup → graph upsert

**Gap vs MemDB:** mem0's extraction prompt is more complete on temporal anchoring ("Observation Date" as explicit param) and on linking new memories to existing via `linked_memory_ids` in the extraction output. MemDB extraction prompt doesn't pass the observation date as a named variable — it's included in context but not explicitly named.

**Port effort:** S (1 day). Extraction prompt enhancement: add explicit `observation_date` variable, add `linked_memory_ids` to extraction output schema.

**Expected MemDB F1 impact:** med (+2-4 points on temporal questions, category 2 in LoCoMo which is currently 85.05 for Memobase vs our unknown). Targeted at temporal accuracy.

---

### 3. Graphiti

**Architecture:** Python async framework (getzep, 4k stars). Pure temporal knowledge graph: Neo4j nodes + edges with `valid_at`/`invalid_at`/`expired_at` fields. No vector tiers — everything is a graph edge with fact embedding. Key concepts: episodic nodes (raw events) → entity extraction → edge resolution (contradiction detection → `expired_at`).

**Published F1 numbers:** No standalone LoCoMo score found in cloned repo. Zep (which uses Graphiti internally) reports 75.14% on LoCoMo LLM Judge (from Memobase README issue reference).

**Key techniques relevant to MemDB:**
- **`expired_at` edge invalidation** (`graphiti_core/utils/maintenance/edge_operations.py:543-572`): when a new edge contradicts an existing one, the old edge gets `expired_at = utc_now()` (soft-delete). Search WHERE filters `expired_at IS NULL`. Point-in-time queries possible.
- **Canonical node merge** (`graphiti_core/utils/maintenance/node_operations.py:341-383`): exact normalized-name dedup using dict; when two nodes have same name, newer replaces older and episode indices from discarded node are merged into canonical.
- **`semaphore_gather`** (`graphiti_core/helpers.py:122-133`): async wrapper around `asyncio.gather` with bounded semaphore (`SEMAPHORE_LIMIT`). Used throughout for parallel edge/node operations.
- **`valid_at` / `invalid_at` LLM extraction** (`edge_operations.py:_extract_edge_timestamps`): lightweight LLM call to extract when a fact became true/false from episode text.

**Code refs:**
- `graphiti_core/edges.py:271` — `expired_at: datetime | None = Field(...)`
- `graphiti_core/utils/maintenance/edge_operations.py:543-572` — `resolve_edge_contradictions` → set `expired_at`
- `graphiti_core/utils/maintenance/node_operations.py:341-383` — `deduplicate_node_list` canonical merge
- `graphiti_core/helpers.py:122-133` — `semaphore_gather` implementation

**Gap vs MemDB:** MemDB uses hard-delete in `applyDeleteAction`/`applyUpdateAction` (confirmed in ROADMAP-ADD-PIPELINE.md §1). No `expired_at` column. No point-in-time queries. MemDB's LLM semaphore (ROADMAP-ADD-PIPELINE.md §4) is designed but not yet implemented. Graphiti's canonical merge is simpler than MemDB's 3-layer dedup but provides provenance tracking via episode index merging.

**Port effort:** M (5-7 files, SQL migration, adds `expired_at` + `invalid_at` columns).

**Expected MemDB F1 impact:** med-high. Direct F1 impact low (temporal questions already score well with existing approach), but high product quality impact: point-in-time queries, undo capability, contradiction audit trail. Indirect F1 impact: prevents stale contradicted memories from polluting retrieval.

---

### 4. Letta (MemGPT)

**Architecture:** Python agent framework (12k stars). Memory is a first-class agent resource: in-context "working memory" block + external archival memory + recall (conversation history search). Key innovation: agent autonomously decides to `memory_edit` its own core memory. Context compaction via sliding-window summary.

**Published F1 numbers:** MemGPT listed as a baseline in LoCoMo paper with ~51 score. No current Letta-specific number found.

**Key techniques relevant to MemDB:**
- **`async_redis_cache` decorator** (`letta/helpers/decorators.py:90`): generic async Redis cache with `key_func` lambda, TTL, Pydantic model serialization, OTel spans. Used for embedding calls (`passage_manager.py:34`): `@async_redis_cache(key_func=lambda text, model, endpoint: f"{model}:{endpoint}:{text}")`.
- **Sliding-window summarizer** (`letta/services/summarizer/summarizer_sliding_window.py`): token-aware, triggers compaction when context exceeds threshold, creates running summary of dropped messages.
- **OTel tracing** (`letta/otel/tracing.py`, `@trace_method`): per-method tracing across all service calls. Already flagged in ROADMAP-ADD-PIPELINE.md §3.

**Code refs:**
- `letta/helpers/decorators.py:90-165` — `async_redis_cache` full implementation
- `letta/services/passage_manager.py:34` — embedding cache usage
- `letta/services/summarizer/summarizer_sliding_window.py` — sliding window logic

**Gap vs MemDB:** MemDB has embedding TTL cache in ROADMAP-ADD-PIPELINE.md §2 (not yet implemented). Letta's decorator pattern is the clean reference implementation. MemDB has no OTel tracing. Letta's sliding window is not relevant (MemDB uses add pipeline, not in-context agent memory).

**Port effort:** S (1 day). Embedding Redis cache is a wrapper around existing embedder; ROADMAP-ADD-PIPELINE.md §2 already has the design.

**Expected MemDB F1 impact:** low on F1, high on latency/cost. Cache hit rate ~15-20% during buffer flush (ROADMAP-ADD-PIPELINE.md estimate).

---

### 5. LangMem

**Architecture:** LangChain ecosystem library (2k stars). Provides functional primitives for memory: `create_memory_store_manager`, `ReflectionExecutor`. Core tech: `trustcall` library for JSON-PATCH memory updates (instead of full rewrite), background `ReflectionExecutor` that dispatches memory consolidation to a separate LangGraph thread.

**Published F1 numbers (LoCoMo LLM Judge):** 58.10% overall (confirmed in both docs/competitive/2026-04-search-pipeline-vs-rivals.md and Memobase README: single-hop 62.23%, multi-hop 47.92%, open 71.12%, temporal 23.43%). Weakest on temporal (23.43% — catastrophic).

**Key techniques relevant to MemDB:**
- **trustcall PATCH** (`knowledge/extraction.py:161`, `from trustcall import create_extractor`): instead of extracting full new memory text, generates JSON-PATCH operations against existing memory document. Enables surgical updates to structured memory objects. Used with `extractor = create_extractor(model, tools=[schema], tool_choice="any")`.
- **`ReflectionExecutor`** (`reflection.py`): background thread that accepts `submit(payload)` and dispatches to LangGraph. Decouples memory consolidation from request path. Queue-based, fire-and-forget.

**Code refs:**
- `src/langmem/knowledge/extraction.py:25,161,253,268,357,371` — `create_extractor` / trustcall usage
- `src/langmem/reflection.py:83-130` — `ReflectionExecutor` / `RemoteReflectionExecutor` protocol

**Gap vs MemDB:** MemDB uses full replacement on UPDATE (LLM generates new text). trustcall PATCH would enable: "User's favorite food changed from pizza to sushi" → `[{"op": "replace", "path": "/food_preference", "value": "sushi"}]` against the existing structured memory. Requires structured memory format. LangMem's F1 is worst in class — this approach alone isn't sufficient.

**Port effort:** XL (requires structured memory schema redesign). Not recommended as standalone — trustcall only helps with structured preference memories, not episodic.

**Expected MemDB F1 impact:** low. LangMem itself scores 58.10 — the technique doesn't deliver F1 wins.

---

### 6. Zep

**Architecture:** The cloned `getzep/zep` repo is an examples/integrations repo (not the core engine — that's closed-source `zep-cloud`). Contains: LoCoMo eval harness (`benchmarks/locomo/`), integration examples, and client code using `zep_cloud` SDK. The actual Graphiti library (also by getzep) is the open-source core.

**Published F1 numbers (LoCoMo LLM Judge):** 75.14% overall (Zep* updated results per Memobase README, issue #101). Breakdown not confirmed from this repo. Original Zep numbers: 65.99% overall.

**Key techniques relevant to MemDB:**
- Zep's working memory is provided by Graphiti (see #3 above).
- **Eval harness architecture** (`benchmarks/locomo/benchmark.py`): YAML-based config, semaphore concurrency control, timestamped runs with config snapshots. Well-engineered reference for MemDB's eval pipeline.
- No novel memory technique found beyond Graphiti — the differentiation is closed-source model fine-tuning.

**Code refs:**
- `benchmarks/locomo/README.md` — production-grade eval harness design
- `benchmarks/locomo/benchmark.py` — semaphore pattern for rate-limited eval

**Gap vs MemDB:** Zep's open-source contribution is Graphiti. Core advantage is commercial fine-tuned models (not portable). Eval harness design is useful but not F1-lifting.

**Port effort:** N/A (no portable technique beyond Graphiti).

**Expected MemDB F1 impact:** none directly. Graphiti techniques covered in #3.

---

### 7. Cognee

**Architecture:** Python knowledge graph framework (topoteretes, ~2k stars). Positions as "AI memory with Knowledge Engine." Core flow: text → chunking → `extract_graph_from_data` → nodes + edges in graph DB → retrieval via multiple strategies (vector, graph completion, decomposition, temporal). Multi-round cascade extraction.

**Published F1 numbers:** None found in cloned repo. No LoCoMo eval harness present.

**Key techniques relevant to MemDB:**
- **Cascade triplet extraction** (`tasks/graph/cascade_extract/utils/extract_edge_triplets.py`): multi-round extraction where each round sees previous nodes/edges and adds new ones. `n_rounds=2` default. Prevents LLM from re-extracting already-known facts. Dedup by `(source, target, relation)` triplet key.
- **`GraphCompletionDecompositionRetriever`** (`modules/retrieval/graph_completion_decomposition_retriever.py`): query decomposition + per-subquery graph completion + merge. Similar to MemOS VEC_COT but at graph level.
- **Temporal retriever** (`modules/retrieval/temporal_retriever.py`): separate retriever for time-based queries — recognized as a distinct query type.

**Code refs:**
- `cognee/tasks/graph/cascade_extract/utils/extract_edge_triplets.py:8-60` — multi-round extraction with dedup
- `cognee/modules/retrieval/graph_completion_decomposition_retriever.py:1-60` — decomposition retriever
- `cognee/tasks/temporal_awareness/` — temporal awareness pipeline

**Gap vs MemDB:** Multi-round extraction is not in MemDB. MemDB extracts once per add call. Cascade extraction would help for long documents where a single LLM pass misses implicit relations. For LoCoMo use case (short conversation turns), benefit is limited.

**Port effort:** M (new extraction mode).

**Expected MemDB F1 impact:** low for LoCoMo (short turns don't benefit), med for document-heavy use cases.

---

### 8. Memobase

**Architecture:** Python server + Go client (memodb-io). User-profile-centric memory: structured `UserProfileTopic` taxonomy (8 top-level topics: basic_info, contact_info, education, demographics, work, interest, psychological, life_event) with configurable sub-topics. Extraction prompt maps to taxonomy slots. No vector search — pure structured profile CRUD with LLM-extracted fields.

**Published F1 numbers (LoCoMo LLM Judge):**
- v0.0.32: 70.91% overall
- v0.0.37: **75.78% overall** (single-hop 70.92%, multi-hop 46.88%, open 77.17%, temporal **85.05%**)
- Source: `docs/experiments/locomo-benchmark/README.md` + fixture files

**Key techniques relevant to MemDB:**
- **Profile taxonomy** (`src/server/api/memobase_server/prompts/user_profile_topics.py`): 8 top-level topics with structured sub-topics (e.g., `psychological: [personality, values, beliefs, motivations, goals]`). LLM extraction guided by taxonomy. Configurable via YAML override.
- **Profile organize** (`controllers/modal/chat/organize_profile.py`): when sub-topics exceed `max_subtopics`, LLM re-organizes memories into fewer buckets. Structural compaction.
- **Profile context injection** (`controllers/modal/chat/extract.py`): during extraction, current user profile state is packed into prompt context — LLM sees existing facts and enriches/extends them.

**Code refs:**
- `src/server/api/memobase_server/prompts/user_profile_topics.py:9-80` — `CANDIDATE_PROFILE_TOPICS` definition
- `src/server/api/memobase_server/controllers/modal/chat/extract.py:26-80` — `extract_topics` with profile context injection
- `src/server/api/memobase_server/controllers/modal/chat/organize_profile.py` — profile compaction

**Gap vs MemDB:** MemDB has 22-category preference taxonomy (D8) but it's not injected into extraction as context. Memobase achieves 85.05% on temporal questions (LoCoMo category 2) — strongest temporal score in the comparison table. MemDB's temporal handling is via `valid_at` + decay, not via structured profile temporal anchoring.

**Port effort:** S (1-2 days). Profile context injection: pack current preference/profile state into extraction prompt context, so LLM generates continuations rather than isolated facts.

**Expected MemDB F1 impact:** med (+3-5 points on temporal category, potentially high on single-hop personalization).

---

## Ranked Port-Targets

### Why this ranking

Port priorities map to M7 weak spots, not raw F1 numbers from competitors:
- **#1 VEC_COT** addresses MemDB's lowest-scoring LoCoMo category — cat-2 multi-hop F1=0.091 (MILESTONES.md M7 Stage 2). Competitors confirm this matters: Zep* claims 66.04% multi-hop using graph traversal, Memobase only 46.88%. Whether VEC_COT specifically (vs alternative graph approaches) is the right port is an open question — see the caveat in port-target-vec-cot.md.
- **#2 Profile context injection** addresses temporal/single-hop where Memobase 85.05% sets the public bar, vs MemDB temporal F1=0.201 in M7 Stage 2 (different metric, comparable shape).
- **#3 expired_at soft-delete** is an indirect quality lift (cleaner retrieval) plus a major product win (point-in-time queries, undo, audit). Lift estimate is a planning assumption.

| Rank | Source | Technique | Effort | Expected F1 Lift | Status |
|------|--------|-----------|--------|------------------|--------|
| 1 | MemOS | VEC_COT: embed each sub-question independently, union vectors for HNSW probe | M | +5-7 pts (complex multi-hop) | RECOMMEND M8 — S4 poised |
| 2 | Memobase | Profile context injection: pack current profile state into extraction prompt context | S | +3-5 pts (temporal/single-hop) | RECOMMEND M8 |
| 3 | Graphiti | `expired_at` soft-delete: replace hard-delete with temporal invalidation | M | +2-3 pts indirect (cleaner retrieval) + product quality | RECOMMEND M9 |
| 4 | mem0 | Observation date extraction anchor + `linked_memory_ids` in extraction output | S | +2-4 pts temporal | M9 |
| 5 | Letta | Redis embedding cache via decorator | S | latency/cost only | M9 backlog |
| 6 | Cognee | Multi-round cascade extraction | M | low for LoCoMo | defer |
| 7 | LangMem | trustcall PATCH structured updates | XL | low (LangMem scores 58.10) | skip |
| 8 | Zep | No novel portable technique | — | none | skip |

---

## Where MemDB Already Wins (don't break)

From docs/competitive/2026-04-search-pipeline-vs-rivals.md comparison table:

- **Go latency** — 15-30ms vs Python ~200-300ms. Unique in class.
- **Real MMR** with exponential penalty, phase-1 prefill, text similarity guard, bucket-aware quota — no competitor has this.
- **BM25 + Vector hybrid** — mem0/MemOS/LangMem all vector-only.
- **Cross-encoder rerank** (BGE-reranker-v2-m3 via embed-server) — MemOS has optional NLI, others don't.
- **Multi-hop AGE graph retrieval** (D2) — only Graphiti does graph retrieval but requires Neo4j.
- **CoT query decomposition** (D7, PR #47) — union-by-id merge on decomposed sub-questions.
- **Bi-temporal model** (valid_at) — only Graphiti also does this.
- **3-stage iterative retrieval** (D5) — unique in class.
- **mode=raw per-message granularity** — unique.
- **window_chars per-request override** — unique.

---

## Where MemDB Is Open (Gaps)

1. **VEC_COT multi-vector search** (port-target #1): sub-question embeddings not used for HNSW probing — one vector per search regardless of D7 decomposition.
2. **Profile context injection at extraction time** (port-target #2): existing profile state not packed into add prompt.
3. **`expired_at` soft-delete** (port-target #3): hard-deletes in `applyDeleteAction` lose historical context.
4. **Observation date as explicit anchor** (port-target #4): temporal resolution uses context but not named variable.
5. **Embedding Redis cache** (port-target #5): re-embeds identical strings on every add.
6. **LLM semaphore** (ROADMAP-ADD-PIPELINE.md §4): background goroutine burst uncontrolled.
