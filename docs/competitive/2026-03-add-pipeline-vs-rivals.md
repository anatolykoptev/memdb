# Add Pipeline — Competitive Snapshot (March 2026)

> Snapshot dated March 2026. For current state see
> [docs/backlog/add-pipeline.md](add-pipeline.md) (pending backlog) or the
> master [ROADMAP.md](../../ROADMAP.md).
>
> Competitors sampled: mem0 22k★, Zep/Graphiti 4k★, Letta 12k★, LangMem 2k★,
> Motorhead (deprecated). Goal at time of writing: bring the add pipeline to
> best-in-class, outpacing all competitors.

---

## Current advantages (March 2026)

As of March 2026 MemDB's add pipeline already led competitors on multiple
dimensions. The table below is a snapshot; items marked ✅ in the backlog file
have since shipped further improvements.

| Feature | MemDB | Best competitor |
|---------|-------|----------------|
| Multi-mode pipeline (fast/fine/buffer/async/feedback) | 5 modes | Graphiti: 2 (normal/bulk) |
| Content classifier (skip trivial/code-only) | Present | None of the above |
| Buffer batching (reduces LLM calls ~80%) | Present | Motorhead (deprecated) |
| Hallucination filter | Present | None of the above |
| VSET hot cache (Redis HNSW, ~1-5ms) | Present | All competitors hit main store |
| Bi-temporal model (valid_at) | Present | Graphiti only |
| Content-hash dedup | Present | redis/agent-memory-server |
| importance_score + retrieval_count decay | Present | A-MEM (retrieval_count only) |
| Multi-layer dedup (hash → cosine → near-dup) | 3 layers | mem0: 2 (cosine + LLM) |
| Python proxy for /product/add | Removed ✅ | — |
| **window_chars per-request override** | Present (v2.1.0) | None of the above |
| **Embed batching in fast-add pipeline** | Present (`memdb.add.embed_batch_size` histogram) | None of the above |
| **mode=raw per-message ingest granularity** | Present | None of the above |

---

## Full pipeline comparison (March 2026)

The table below compares core pipeline dimensions across five frameworks.
Backend latency numbers updated to v2.1.0 (post-M7 embed batching).

| Dimension | MemDB | mem0 | Graphiti | Letta | LangMem |
|-----------|-------|------|----------|-------|---------|
| LLM calls per add | 1 (unified) | 2 | 4-6 | 0 | 1 |
| Pipeline stages | 10+ | 2 | 8 | 1 | 1 |
| Dedup layers | 3 (hash+cosine+near-dup) | 2 (cosine+LLM) | 2 (cosine+LLM+union-find) | 1 (hash) | 1 (PATCH) |
| Content classifier | Present | No | No | No | No |
| Buffer batching | Present | No | bulk API (no dedup) | No | Background executor |
| Hallucination filter | Present | No | No | No | No |
| Temporal model | valid_at | No | expired_at (full) | No | No |
| Memory types | 7 (LTM/WM/UM/Skill/Tool/Episodic/Pref) | 3 | 3 (Episode/Entity/Community) | 3 | 3 |
| Async ingestion | Redis Streams + semaphore | asyncio | semaphore_gather | No | ThreadPool |
| Hot cache | Redis VSET HNSW (~1ms) | No | No | Redis embed cache | No |
| Observability | OTel (counters + histograms) | PostHog | OTel Tracer | OTel @trace_method | LangSmith |
| Soft-delete | merged only → **full (planned)** | SQLite audit | expired_at (full) | No | No |
| Rate limiting | addSem (sync) → **llmSem (planned)** | No | semaphore_gather | No | No |
| Backend latency | Go ~15-30ms (fast-add); ~1.0s p95 at window=512 after embed batching (v2.1.0) | Python ~200ms | Python ~300ms | Python ~100ms | Python ~200ms |

### Key patterns from competitors (reference)

| Pattern | Competitor | File | Applicability |
|---------|-----------|------|--------------|
| Two-phase LLM extract+update | mem0 | `mem0/memory/main.py` | Our unified prompt is better (1 call vs 2) |
| Temporal invalidation (expired_at) | Graphiti | `edge_operations.py` | **Take** → backlog item 1 |
| Union-find canonical merge | Graphiti | `bulk_utils.py` | Already in reorganizer |
| trustcall JSON Patch | LangMem | `extraction.py` | Deferred → backlog item 8 |
| Background async reflection | LangMem | `reflection.py` | Already present (goroutines) |
| Batch embedding + hash dedup | Letta | `connectors.py` | Already present (content_hash) |
| semaphore_gather | Graphiti | `helpers.py` | **Take** → backlog item 4 |
| Redis embedding cache | Letta | `passage_manager.py` | **Take** → backlog item 2 |
| @trace_method (OTel) | Letta | service methods | **Take** → backlog item 3 |
