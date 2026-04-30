# MemDB: Memory Database for AI Agents

> A comprehensive overview of what MemDB is, what makes it unique, and how it compares to alternatives.

---

## What Is MemDB?

**MemDB** is a Memory Operating System for Large Language Models and AI agents. It treats memory as a **first-class computational resource** — the same way a traditional OS manages CPU, RAM, and disk. MemDB stores, retrieves, organizes, and evolves knowledge that LLMs accumulate over time, enabling persistent, personalized, and context-aware AI interactions.

MemDB is a hard fork of [MemOS](https://github.com/MemTensor/MemOS) (Apache 2.0), an open-source project by MemTensor and several Chinese universities (Shanghai Jiao Tong, Renmin, Zhejiang, Tongji, USTC). The academic foundation is described in the paper ["MemOS: A Memory OS for AI System"](https://arxiv.org/abs/2507.03724) (36 pages, 39 authors).

**The core problem**: LLMs are stateless. They forget everything between sessions, cannot learn from ongoing interactions, provide inconsistent responses as knowledge accumulates without organization, and cannot share memory across agents or platforms. RAG is a workaround, not a solution — it has no lifecycle management, no deduplication, no versioning, no governance.

**MemDB's answer**: A unified pure-Go memory system with typed memory tiers, Apache AGE graph storage, multi-strategy search with adaptive rerank, background reorganization workers, atomic-fact extraction, bi-temporal edges, and per-cube tenant isolation.

---

## Architecture Overview

```
Clients (Claude Code plugin, vaelor, oxpulse, API consumers)
          |
          | HTTP/REST (:8080) + MCP (:8001)
          v
+----------------------------------------------------------+
|  memdb-go (Pure Go, ~44k LoC, all routes native)         |
|  - HTTP+MCP handlers, OpenTelemetry, rate limiting       |
|  - 4 add modes: raw / fast / fine / fine-atomic          |
|  - ~30 search stages (rerank, multihop, fast-path,       |
|    LLM judge, CoT, query rewrite, event inject, …)       |
|  - ~30 background reorganizer phases                     |
|  - Server-side dual-speaker fan-out (M9)                 |
+----------------------------------------------------------+
   |          |                |                  |
   | pgxpool  | redis           | http (sidecar)   | http
   v          v                 v                  v
+----------+ +-------+ +-----------------+ +----------------+
| Postgres | | Redis | | embed-server    | | LLM gateway    |
| 17 +     | | 7     | | (Rust, BoringSSL)| | (CLIProxyAPI   |
| pgvector | | (cache,| | multilingual-e5  | |  :8317, OpenAI |
| + AGE +  | |  XADD,| | + jina-code-v2,  | |  Anthropic     |
| tsvector | |  WM    | | tokio batcher)   | |  Gemini, …)    |
+----------+ +-------+ +-----------------+ +----------------+
```

**Pure-Go stack** (since M9, 2026-04-26): the legacy Python backend was removed — every route is native Go. The 70× retrieval speedup that motivated the original Python→Go migration is now table-stakes; the rest of the engineering effort moved into ingestion quality (atomic facts, bi-temporal edges, structural edges) and retrieval intelligence (adaptive rerank gating, multi-hop graph expansion, M9 dual-speaker).

| Layer | What | Where |
|---|---|---|
| HTTP gateway | All ingest/search/chat/admin routes; OpenAPI spec; auth (Bearer + X-Service-Secret); MCP | `memdb-go/internal/handlers` |
| Embed sidecar | Rust HTTP service, multi-model (`multilingual-e5-large` 1024-dim + `jina-code-v2`); tokio batcher; Prometheus | `embed-server/` (separate repo) |
| LLM | All callers go through CLIProxyAPI on `:8317` (Gemini Flash/Pro, Claude Sonnet, OpenAI) — single env (`LLM_API_BASE`) | external |
| DB | PostgreSQL 17 + Apache AGE (Cypher) + pgvector (cosine) + tsvector full-text + GIN indexes | `memos_graph` schema |
| Cache | Redis: ingest stream (XADD), Working Memory hot cache (VSET), query rewrite cache, scheduler queue | external |

---

## Memory tiers and entity types

MemDB inherits the five-type taxonomy from the MemOS paper but extends it with first-class entity tables for events, profiles, and feedback. The shape on disk:

### Memory rows (`memos_graph.Memory`)

Apache AGE vertex with rich `properties` JSONB. The `memory_type` discriminator splits four classes:

| memory_type | Purpose | Lifecycle |
|---|---|---|
| `WorkingMemory` | hot, session-scoped buffer (paired 1:1 with `LongTermMemory` in fast/fine modes) | trimmed by `cleanupWorkingMemory` per-cube budget |
| `LongTermMemory` | persistent facts, summaries, extracted knowledge | tree-promoted (raw → episodic → semantic), bi-temporal edges, PageRank-scored |
| `UserMemory` | user-only personal facts (auto-classified when content is exclusively about the user) | same lifecycle as LTM, but search filtered by speaker_label / attributed_to |
| `SkillMemory` | procedural knowledge (extractor: `internal/llm/skill_extractor.go`) | structured fields: name, description, procedure, experience, examples |

Common `properties` fields (excerpt — see `add_props.go::buildNodeProps`):
`id, memory, memory_type, hierarchy_level (raw|episodic|semantic), user_name, user_id, agent_id, session_id, created_at, updated_at, observation_date, sources[], tags[], info{...}, kind (atomic_fact?), attributed_to, event_dates[], linked_memory_ids[], parent_memory_id, merged_into_id, pagerank, ce_score_topk[]`.

### Atomic facts (M11 F8, mem0-arxiv pattern)

When `MEMDB_ATOMIC_FACTS=true` (default ON), the fine-mode extractor emits 5–15 self-contained 15–80-word facts per chunk instead of one paragraph. Each fact carries `kind=atomic_fact`, `attributed_to=<speaker>`, `linked_memory_ids[]` (LLM-picked at extract time), `event_dates[]`. Lives in the same `Memory` table.

### User profiles (`memos_graph.user_profiles`, M10)

Typed (topic, sub_topic) taxonomy with confidence + valid_at + soft-delete (`expired_at IS NULL`). Cube-scoped (security audit C1, migration 0017). Per-slot LLM merge: `add_fine_profile.go` extracts slot updates, `reorganizer_prefs.go` periodically reorganises and promotes. Closed-set 22-key MemOS-style preference taxonomy enforced at parse time.

### User events (`memos_graph.user_events`, F3)

LLM-extracted dated events from messages. Used by the F7 search-time temporal augment stage (`SearchMemoriesByDateRange`) and the F3 `event_inject` stage (cosine + date + tag matched).

### Feedback events (`memos_graph.feedback_events`)

Persistent record of user feedback signals (`feedback_ops.go`). Consumed by `reorganizer_feedback.go` to bias future retrieval.

### Memory edges (Apache AGE)

`memory_edges` and `entity_edges` carry F11 bi-temporal columns: `valid_at` (when the fact became true), `invalid_at` (when it stopped), `expired_at` (when we learned it was wrong). Structural edges are emitted at ingest by `add_structural_edges.go` with three relations: `SAME_SESSION`, `TIMELINE_NEXT`, `SIMILAR_COSINE_HIGH`.

### Activation Memory + Parametric Memory

The two MemOS types that need access to model internals are **out of scope for the API-based LLM stack** MemDB targets (Gemini, Claude, GPT). Tracked as a future deliverable for self-hosted vLLM deployments.

---

## Add modes (4 ingest pipelines)

| Mode | Pipeline | Sources stamping | LLM extract? | When to use |
|---|---|---|---|---|
| `raw` | `add_raw.go` (1 row per message, no windowing, no LLM) | per-msg metadata in `info{chat_time,uuid,agent_id,role}` | no | structured payloads (JSON experience records, API logs, tool trajectories) where windowing would corrupt the blob |
| `fast` | `add_windowing.go::extractFastMemories` (sliding-window with overlap, batch embed, WM+LTM pair) | per-window `sources[]` array carries chat_time, uuid, agent_id, role, content per source | no | conversational ingest where session-aware structural edges + WM hot cache help |
| `fine` | `add_fine.go::nativeFineAddForCube` (LLM extract + dedup + entity link + atomic facts) | per-msg `sources[]` after extraction | yes | LoCoMo / unstructured human dialogue / multi-turn agent transcripts |
| `fine-atomic` (F8) | branch of fine when `MEMDB_ATOMIC_FACTS=true` (default) | adds `kind`, `attributed_to`, `linked_memory_ids`, `event_dates` | yes (atomic prompt from mem0 arXiv) | every fine call, opt-out via env |

All modes share `chat_time` propagation (M12.1 temporal anchor), `observation_date` resolver (`add_obs_date.go`), per-msg uuid passthrough (PR #218 raw, PR #216 fast, PR #215 fine), and content_hash + cosine dedup (with the M9 session-aware dedup fix from PR #224 — cross-session high-cosine pairs are no longer treated as duplicates).

---

## Search pipeline (~30 stages)

The native search path runs a long pipeline rather than a fixed RRF on three retrievers. `internal/search/pipeline.go` orchestrates; `internal/search/service.go::defaultStages` lists the canonical order. Headline stages, in order:

1. **Tokenize + temporal cutoff** — extract per-language tokens, detect "yesterday / last week / 2023" intent, compute time window
2. **D4 query rewrite** (LLM, cached) — rephrase ambiguous queries for embedding match
3. **D7 / D11 CoT decompose** — break complex queries into sub-queries
4. **Vector search** — pgvector cosine on `Memory.embedding` (pgvector HNSW or halfvec_cosine_ops)
5. **Full-text search** — Postgres `tsvector` GIN index, EN/RU/CJK tokenizer
6. **Multi-hop graph expansion (D2)** — Apache AGE Cypher traversal from top-K seeds
7. **Linked-expand (F12)** — 1-hop GIN lookup on `linked_memory_ids` (atomic-facts only)
8. **F3 event inject** — cosine + date + tag match against `user_events`
9. **F7 temporal augment** — boost rows whose `event_dates[]` intersect the query window
10. **PPR scorer** — Personalized PageRank over the result graph (F14)
11. **Counting classifier** — detects "how many" queries and inflates top_k
12. **Internet augmentation** — optional, via go-search MCP (off by default)
13. **Fusion (RRF / DBSF / LinearMinMax)** — env-selectable strategy
14. **Cross-Encoder rerank** — M14 CE precompute + adapter
15. **MMR / staged / cosine rerank** — three local rerankers run unconditionally
16. **Adaptive rerank gate** — decides "skip / clustered / wide-spread / too-few" before LLM rerank fires
17. **LLM Judge** — fires when adaptive gate selects clustered/wide-spread; can swap, reject, or agree with the cosine top-1
18. **D10 answer enhancement** — LLM-driven candidate boost
19. **Threshold filter + chatMinPersonalMem floor** — relativity cutoff with min-rows guard
20. **Token-budget truncation** (M12 RAM-style) — caps the memories block at `max_context_tokens` before prompt assembly
21. **Format response** — `FormatMemoryItem` strips `sources[]` for response size, exposes metadata

The list above is canonical for the read path; ingest also runs F11 bi-temporal edge extraction and F3 event extraction in parallel fan-out workers.

---

## Background reorganizer (`internal/scheduler`)

~30 phases run on Redis-stream workers and timer loops:

- **Tree promotion** (`tree_manager.go`, `tree_cluster_promote.go`, `tree_summariser.go`) — clusters of `raw` memories promote to `episodic`, `episodic` clusters promote to `semantic` with LLM-generated summaries. Children carry `parent_memory_id`.
- **LLM consolidation** (`reorganizer_consolidate.go`, `reorganizer_llm_consolidate.go`) — pairs above cosine 0.8 → LLM classifies conflict / duplicate / unrelated → merge or archive
- **Auto-merge / fuzzy** (`reorganizer_automerge.go`, `reorganizer_fuzzy.go`) — non-LLM bulk merge for high-confidence near-dupes
- **Entity reorganization** (`reorganizer_entities.go`) — entity_edges maintenance, F11 invalid_at bookkeeping
- **Episodic / WM consolidation** (`reorganizer_episodic.go`, `reorganizer_wm.go`, `reorganizer_wm_compact.go`) — WM trimming, episodic clustering
- **Profile reorganization** (`reorganizer_prefs.go`) — slot-by-slot user_profiles consolidation
- **Bi-temporal validator** (`bitemporal_validator.go`) — cross-record valid_at synchronisation
- **PageRank** (`pagerank.go`, `pagerank_personalized.go`) — F14 importance scoring, personalised per-cube
- **CE precompute** (`tree_ce_precompute.go`) — M14 cross-encoder score caching for top neighbours
- **Profiler** (`profiler.go`) — LLM-driven user_profile updater on conversation completion
- **Worker pool** (`worker.go`, `worker_*.go`) — Redis stream consumers with retry, priority queues, periodic flush

---

## Performance

Pure-Go gateway, single docker-compose deployment. Numbers from M9 / M10 prod traces:

| Metric | Value | Note |
|---|---|---|
| Search p50 | 80–150 ms | full ~30-stage pipeline, single cube |
| Search p95 | 350–500 ms | with multi-hop expansion + LLM judge |
| Search (cached, Redis 30s) | 1–2 ms | query-rewrite cache + result cache |
| Add fast (single window, batch embed) | 50–80 ms | dominated by ONNX embed RTT |
| Add fine (LLM extract + dedup) | 1.2–3 s | gemini-2.5-flash, 30-msg batch |
| Add fine-atomic | 2–4 s | adds atomic-extractor LLM call |
| Health check | < 1 ms | |

Numbers depend heavily on cube size, query complexity, and whether the adaptive rerank gate fires LLM Judge.

---

## Stack

| Layer | Tech | Why |
|---|---|---|
| Server | Go 1.26, single binary, multi-stage Docker (debian:trixie-slim runtime) | static linking, low startup, no Python runtime dependencies |
| API | HTTP REST + MCP (Model Context Protocol) on `:8001` | first-class MCP server for AI-tool integration |
| DB | Postgres 17 + Apache AGE (Cypher) + pgvector + tsvector | one DB does graph + vector + fulltext — no Neo4j / Qdrant split |
| Embedder | ONNX `multilingual-e5-large` (1024-dim) + `jina-code-v2`, served by Rust `embed-server` sidecar with BoringSSL | multilingual + code-specific embeddings, tokio batcher |
| LLM | CLIProxyAPI on `:8317` (Gemini 2.5 Flash/Pro, Claude Sonnet 4.6, OpenAI fallback) | single env (`LLM_API_BASE`) for every caller |
| Cache | Redis 7 (XADD streams for ingest queue, VSET for WM hot cache, key-value for query rewrite cache, sorted-set for scheduler) | one server, four use-cases |
| Auth | Bearer `Authorization` header + `X-Service-Secret` (internal callers) | dual-auth — public clients use Bearer, sidecars use service secret |
| Observability | OpenTelemetry + Prometheus `/metrics` on `PROM_PORT = MCP_PORT + 1000` (e.g. 9001 for memdb-go) | grafana-ready out of the box |

---

## Competitive Landscape

### How MemDB Compares

| Feature | mem0 | Zep | Letta | LangMem | Cognee | **MemDB** |
|---------|------|-----|-------|---------|--------|-----------|
| GitHub Stars | 42k | ~3k | 19k | ~2k | ~20k | 5k (MemOS) |
| Memory Types | 2 (facts, graph) | 3 (episodic, semantic, community) | 3 (core, archival, recall) | 2 (semantic, episodic) | 2 (graph, vector) | **5 (LTM, skill, pref, parametric, activation)** |
| Skill Learning | No | No | Yes (recent) | No | Partial | **Yes (structured: procedure, experience, examples)** |
| Parametric Memory | No | No | No | No | No | **Yes (LoRA)** |
| KV-Cache Memory | No | No | No | No | No | **Yes** |
| Graph Memory | Yes (Neo4j) | Yes (Neo4j) | No | Via FalkorDB | Yes (Kuzu) | **Yes (Apache AGE on PostgreSQL)** |
| Multi-strategy Search | Vector + Graph | Vector + Graph | LLM-directed | Semantic | Vector + Graph | **Vector + BM25 + Fulltext + VEC_COT + Internet** |
| Self-cleaning | Decay/TTL | Auto-prune | LLM self-edit | Manual | Memify | **Reorganizer LLM + FIFO + Cosine dedup + Hash dedup** |
| Memory Lifecycle FSM | Basic | Temporal | No | No | Versioned | **Yes (Generated -> Activated -> Merged -> Archived)** |
| High-perf Gateway | No | No | No | No | No | **Yes (Go, 70x faster)** |
| Multi-language Tokenizer | No | No | No | No | No | **Yes (EN/RU/CJK)** |
| Single-DB Stack | No (Neo4j + vector DB) | No (Neo4j) | SQLite | Various | Kuzu + vector | **Yes (PostgreSQL: graph + vector + fulltext)** |

### MemDB's Unique Advantages

1. **Five memory types** — Only system implementing the full MemOS paper vision. Every competitor handles at most 2-3 types.

2. **Multi-strategy search (5 strategies)** — Vector + BM25 + fulltext + VEC_COT (LLM query decomposition) + internet. No competitor has BM25/fulltext as parallel strategies or integrates internet search.

3. **Go gateway (70x faster)** — No other memory system has a compiled-language performance layer. Delivers sub-500ms search vs 30s in Python.

4. **Single-PostgreSQL stack** — Apache AGE (graph) + pgvector (embeddings) + tsvector (fulltext) in one database. No separate Neo4j cluster. Operationally simpler.

5. **Sophisticated self-cleaning** — Four layered mechanisms (hash dedup -> cosine dedup -> reorganizer LLM -> FIFO eviction). Others have at most one.

6. **Real MMR dedup** — Actual Maximal Marginal Relevance with exponential penalty, not simple text matching.

7. **Academic foundation** — MemOS benchmarks: 73.31% on LoCoMo (vs mem0 66.9%, OpenAI Memory 52.8%), with +43.7% accuracy and 35.2% token savings over OpenAI Memory.

### Where Competitors Excel

| Competitor | Advantage Over MemDB |
|-----------|---------------------|
| **mem0** | 42k stars, massive ecosystem (LangChain, CrewAI, n8n), Chrome extension, managed cloud |
| **Zep** | Bi-temporal tracking (event time vs ingestion time), SOC 2/HIPAA compliance |
| **Letta** | Agent-directed memory (LLM decides what to remember via tool calls), philosophical elegance |
| **Cognee** | 30+ data source ingestion (images, audio, PDFs), automatic knowledge graph construction |
| **All** | Managed cloud offerings (MemDB is self-hosted only) |

---

## Benchmarks (from MemOS Paper)

### LoCoMo Dataset (GPT-4o-mini evaluator)

| System | Overall | Single-Hop | Multi-Hop | Temporal | Open Domain |
|--------|---------|-----------|-----------|----------|-------------|
| **MemOS** | **73.31** | **78.44** | **64.30** | **73.21** | **55.21** |
| Memobase | 72.01 | — | — | — | — |
| mem0 | 66.90 | 65.70 | — | 55.50 | — |
| LangMem | 58.10 | — | — | — | — |
| OpenAI Memory | 52.75 | 61.83 | 60.28 | 28.25 | 32.99 |

### Performance vs OpenAI Memory

- **+43.70% accuracy improvement** (overall)
- **+159% on temporal reasoning** (73.21 vs 28.25)
- **35.24% token savings** (fewer tokens needed per query)

---

## Technical Stack

| Component | Technology |
|-----------|-----------|
| **Python Backend** | FastAPI + Uvicorn, 82K+ LOC |
| **Go Gateway** | Standard library HTTP + pgx + qdrant-client, 18K+ LOC |
| **Graph Database** | PolarDB (PostgreSQL 15 + Apache AGE) |
| **Vector Database** | pgvector (main) + Qdrant (preferences) |
| **Fulltext Search** | PostgreSQL tsvector/GIN + BM25 (rank-bm25) |
| **Cache** | Redis (30s TTL for search results) |
| **Embeddings** | VoyageAI (voyage-4-lite, 1024-dim) |
| **LLM** | Gemini (via CLIProxyAPI) |
| **Internet Search** | SearXNG (self-hosted metasearch) |
| **Message Queue** | Redis Streams (scheduler tasks) |
| **Config** | Pydantic v2 (strict validation, env vars) |
| **Deployment** | Docker Compose (8+ containers) |

---

## API Surface

### Core Endpoints

| Endpoint | Method | What It Does |
|----------|--------|-------------|
| `/product/add` | POST | Add memory (async LLM processing) |
| `/product/search` | POST | Search memories (5 strategies) |
| `/product/get_all` | POST | List all memories (paginated) |
| `/product/delete_memory` | POST | Delete by ID |
| `/product/feedback` | POST | Refine memory via natural language |
| `/product/chat` | POST | Chat with memory-augmented context |
| `/product/chat/complete` | POST | Non-streaming chat |
| `/product/chat/stream` | POST | SSE streaming chat |
| `/product/users/*` | GET/POST | User management |
| `/product/configure` | POST/GET | Configuration |
| `/health` | GET | Health check |

### Search Request Shape

```json
{
  "query": "What programming languages do I prefer?",
  "user_id": "memos",
  "readable_cube_ids": ["memos"],
  "top_k": 6,
  "dedup": "mmr",
  "mode": "fast",
  "internet_search": false,
  "relativity": 0
}
```

### Search Response Shape

```json
{
  "code": 200,
  "data": {
    "text_mem": [{ "cube_id": "memos", "memories": [...], "total_nodes": 109 }],
    "skill_mem": [{ "cube_id": "memos", "memories": [...], "total_nodes": 17 }],
    "pref_mem": [{ "cube_id": "memos", "memories": [...], "total_nodes": 10 }],
    "tool_mem": [],
    "act_mem": [],
    "para_mem": [],
    "pref_note": "",
    "pref_string": ""
  }
}
```

---

## Design Patterns

### Plugin Architecture

All major subsystems follow a base class + factory pattern for pluggable backends:

```
BaseGraphDB (31 abstract methods) -> Neo4j, PolarDB, NebulGraph
BaseVecDB -> Qdrant, Milvus
BaseLLM -> OpenAI, Ollama, Gemini (via proxy)
BaseEmbedder -> VoyageAI, local ONNX
BaseReranker -> LLM-based, similarity-based
BaseConfig (Pydantic, extra="forbid") -> strict validation
```

### Provenance Tracking

Every memory item carries a `SourceMessage` recording its origin:
- `type`: "chat", "doc", "web", "file", "system"
- `role`: "user", "assistant", "system", "tool"
- `content`: minimal reproducible snippet
- `chat_time`, `message_id`, `doc_path`: locators

### Graceful Degradation

Every Go native handler falls back to Python proxy on error. Every Python search strategy has try-catch with fallback. Vector fails -> use text dedup. Fulltext fails -> graph search only. LLM fails -> return raw results.

---

## Summary

MemDB is architecturally the most complete AI memory system available. Its five memory types, multi-strategy search, self-organizing intelligence, Go performance layer, and single-PostgreSQL stack create a combination that no competitor replicates. The academic foundation (MemOS paper) provides strong benchmark validation.

The trade-off is ecosystem maturity: mem0 has 8x more GitHub stars and integrations with every major AI framework. MemDB's advantages are deep technical ones — they matter for production workloads where memory quality, search diversity, and response latency are critical.

| What MemDB Does Best | Why It Matters |
|-----------------------|---------------|
| 5 memory types (incl. skill, parametric, activation) | Most complete representation of AI knowledge |
| 5 search strategies in parallel | Best recall across different query types |
| Go gateway (70x faster reads) | Production-grade latency for real-time applications |
| 4-layer self-cleaning | Memory quality improves over time, not degrades |
| Single PostgreSQL stack | Operationally simple, no Neo4j cluster to manage |
| +43.7% over OpenAI Memory | Measurable quality improvement |
