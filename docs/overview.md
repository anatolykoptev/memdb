# MemDB: Memory Database for AI Agents

> A comprehensive overview of what MemDB is, what makes it unique, and how it compares to alternatives.

---

## What Is MemDB?

**MemDB** is a Memory Operating System for Large Language Models and AI agents. It treats memory as a **first-class computational resource** — the same way a traditional OS manages CPU, RAM, and disk. MemDB stores, retrieves, organizes, and evolves knowledge that LLMs accumulate over time, enabling persistent, personalized, and context-aware AI interactions.

MemDB is a hard fork of [MemOS](https://github.com/MemTensor/MemOS) (Apache 2.0), an open-source project by MemTensor and several Chinese universities (Shanghai Jiao Tong, Renmin, Zhejiang, Tongji, USTC). The academic foundation is described in the paper ["MemOS: A Memory OS for AI System"](https://arxiv.org/abs/2507.03724) (36 pages, 39 authors).

**The core problem**: LLMs are stateless. They forget everything between sessions, cannot learn from ongoing interactions, provide inconsistent responses as knowledge accumulates without organization, and cannot share memory across agents or platforms. RAG is a workaround, not a solution — it has no lifecycle management, no deduplication, no versioning, no governance.

**MemDB's answer**: A unified memory system with 5 memory types, graph-based storage, multi-strategy search, self-organizing intelligence, and a high-performance Go gateway.

---

## Architecture Overview

```
Clients (Claude Code, OpenClaw, API consumers)
          |
          | HTTP/REST (:8080)
          v
+--------------------------------------------------+
|  memdb-go (Go Gateway)                           |
|  - 15 native handlers (45% of routes)           |
|  - Reverse proxy for LLM-dependent routes        |
|  - Redis cache (30s TTL)                         |
|  - VoyageAI embedder, parallel DB queries        |
|  - Auth, rate limiting, OpenTelemetry            |
+--------------------------------------------------+
          |                        |
          | native                 | proxy (:8000)
          v                        v
+------------------+    +-------------------------+
| PolarDB          |    | Python Backend (FastAPI)|
| (PostgreSQL +    |    | - Memory lifecycle      |
|  Apache AGE +    |    | - LLM summarization     |
|  pgvector)       |    | - Skill extraction      |
|                  |    | - Preference extraction  |
| Qdrant (prefs)   |    | - Chat with memory      |
| Redis (cache)    |    | - Scheduler (Redis      |
+------------------+    |   Streams)              |
                        +-------------------------+
```

**Two languages, one system**:
- **Python** (82K+ LOC): Core intelligence — memory lifecycle, LLM-driven extraction, skill learning, reorganization, preference detection
- **Go** (18K+ LOC): Performance layer — native search (400ms vs 30s Python), connection pooling, caching, rate limiting, 70x faster for read operations

---

## The Five Memory Types

MemDB implements all five memory types from the MemOS paper. No other system covers all five.

### 1. Long-Term Memory (text_mem)

General facts, conversation summaries, extracted knowledge. Stored as graph nodes in PolarDB (PostgreSQL + Apache AGE) with vector embeddings (VoyageAI, 1024-dim). Organized hierarchically: **task -> concept -> fact**.

- Limit: 1,500 entries per user (FIFO eviction at 80% threshold)
- Auto-dedup: cosine similarity (0.95 threshold) + content hash (SHA256)
- Self-reorganizing: LLM detects conflicts/redundancies, merges or archives

### 2. Skill Memory (skill_mem)

Procedural knowledge — **how to do things**. Automatically extracted from conversations by an LLM that identifies task sequences and generates structured skill records.

Fields:
- `name`: Skill title
- `description`: What it does
- `procedure`: Step-by-step instructions
- `experience[]`: Past successful uses
- `preference[]`: User preferences when using this skill
- `examples[]`: Concrete examples
- `scripts`: Code snippets
- `url`: Reference links

When extracting new skills, the system recalls existing related skills (top_k=5) to update/extend rather than duplicate.

### 3. User & Preference Memory (user_mem + pref_mem)

**User Memory**: Profile facts about the user (limit: 480 entries). Interests, background, behavioral patterns.

**Preference Memory**: Dual-layer system in Qdrant vector DB:
- **Explicit preferences**: Direct statements ("I prefer Python over Java")
- **Implicit preferences**: Inferred from behavioral patterns ("user always chooses concise explanations")

Both collections are queried during search and merged into results.

### 4. Parametric Memory (para_mem)

Knowledge encoded directly in model weights through **LoRA adapter modules**. Domain-specific patches that can be composed, activated, or deactivated without full retraining. Example: a legal domain LoRA that gives a general-purpose model legal reasoning capabilities.

Supports cross-type promotion: stable plaintext knowledge can be distilled into parametric form.

*Note: Requires local model infrastructure. Not used with API-based LLMs (OpenAI, Gemini).*

### 5. Activation Memory (act_mem)

KV-cache structures from LLM inference — the transient cognitive state that normally vanishes after each request. MemOS persists frequently-accessed KV states across requests, achieving up to **91.4% reduction in Time To First Token** for long contexts.

*Note: Requires local HuggingFace/vLLM model access to KV tensors. Not available with API-based LLMs.*

---

## Multi-Strategy Search

MemDB runs **five parallel retrieval strategies** — more than any competitor:

| Strategy | How It Works | Latency |
|----------|-------------|---------|
| **Vector Search** | VoyageAI embedding -> pgvector cosine similarity across all memory scopes | ~100ms |
| **BM25** | Probabilistic keyword relevance ranking via `rank-bm25` | ~50ms |
| **Fulltext (Fast Graph)** | PostgreSQL `tsvector`/`tsquery` with GIN index. Multi-language tokenizer (EN/RU/CJK) | ~50ms |
| **VEC_COT** | LLM decomposes complex queries into sub-queries -> multiple embedding lookups for broader recall | ~3-9s (LLM calls) |
| **Internet Search** | SearXNG self-hosted metasearch (Google, DuckDuckGo, Brave, Wikipedia, GitHub) | ~2s |

All strategies run concurrently. Results are merged by node ID (keep highest score), with dual-found items receiving a +10% score boost. Three deduplication modes:

- **`no`**: Trim to top_k, no dedup
- **`sim`**: Greedy cosine threshold (0.92) — removes near-duplicates
- **`mmr`**: Maximal Marginal Relevance (lambda=0.8) — maximizes result diversity via 3-phase algorithm: prefill top-2, MMR selection with exponential penalty, re-sort by relevance

---

## Self-Organizing Intelligence

MemDB doesn't just store memories — it actively maintains their quality through four mechanisms:

### 1. Reorganizer LLM

Background thread that periodically optimizes the memory graph:
- Detects pairs with cosine similarity > 0.8
- LLM classifies: **conflict** (merge/archive), **duplicate** (consolidate), or **unrelated** (keep both)
- Builds hierarchical summaries: child nodes clustered by embedding similarity, LLM generates parent summary nodes
- 600-second timeout per optimization cycle

### 2. FIFO Eviction

When memory exceeds limits (1500 LTM, 480 User, 20 Working), oldest entries are evicted. Archived entries preserved for audit trail.

### 3. Cosine Deduplication (Pre-Insertion)

Before inserting any new memory, checks existing activated entries via `search_by_embedding(threshold=0.95)`. Prevents near-duplicates from accumulating. Different thresholds for fast-mode (0.92, already LLM-processed) vs fine-mode (0.95).

### 4. Content Hash Deduplication (Cross-Client)

Clients generate `SHA256[:16]` hashes stored in `info.content_hash`. Exact match detection across different clients (Claude Code, OpenClaw, API).

---

## The MemCube Abstraction

The **MemCube** is the fundamental unit of memory — a standardized container wrapping any memory type with rich metadata:

**Content**: The actual data (text for plaintext, tensors for activation, LoRA weights for parametric)

**Metadata**:
- **Descriptive**: Timestamps, origin signatures (user input / inference output / knowledge base), semantic tags
- **Governance**: Access permissions (read/write/share scope), TTL/lifespan policies, priority levels
- **Behavioral**: Access frequency, relevance scores, version lineage

**Lifecycle states**: Generated -> Activated -> Merged -> Archived -> Deleted (with version rollback at each step)

**Cross-type evolution**: MemCubes support promotion/demotion between memory types — frequently accessed plaintext can become activation memory, stable knowledge can become parametric memory. This "memory as OS resource" treatment is unique to MemOS/MemDB.

---

## Go Gateway: Performance Layer

The Go gateway (`memdb-go`) is a reverse proxy + native handler layer that provides dramatic performance improvements:

| Metric | Python Backend | Go Native | Speedup |
|--------|---------------|-----------|---------|
| Health check | 1.80ms | 0.56ms | **3.2x** |
| Get all memories | 25,600ms | 363ms | **70x** |
| Search (cached) | N/A | 1.7ms | - |
| Search (uncached) | ~30,000ms | 350-500ms | **60-85x** |

**How native search works** (Go Phase 3):
1. VoyageAI Embed: query -> 1024-dim vector (~200ms)
2. Parallel errgroup: PolarDB vector search + PolarDB fulltext search + Qdrant preference search (2 collections)
3. Merge & format results
4. Apply dedup (sim/mmr/no)
5. Cache in Redis (30s TTL)

**Proxy fallback**: Any native handler gracefully falls back to Python if dependencies are missing (no embedder, no DB connection, fine mode requiring LLM, internet search requiring SearXNG).

**Coverage**: 15 of 33 routes native (45%), 18 proxied to Python. Proxied routes are those requiring LLM inference: add (summarization), chat (streaming), feedback, suggestions, scheduler.

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
