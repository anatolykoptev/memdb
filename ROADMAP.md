# MemDB Roadmap

> Current version: **v0.22.0** (first public release, 2026-04-26).
> Pure-Go stack, post-Python migration. Memobase-comparable LLM Judge measurement layer.
>
> This is the **master roadmap** — a single-page view across migration, search quality,
> add pipeline, and features. Detailed plans live in:
> - [docs/ROADMAP-GO-MIGRATION.md](docs/ROADMAP-GO-MIGRATION.md) — closed
> - [ROADMAP-SEARCH.md](ROADMAP-SEARCH.md) — active
> - [ROADMAP-ADD-PIPELINE.md](ROADMAP-ADD-PIPELINE.md) — active
> - [ROADMAP-FEATURES.md](ROADMAP-FEATURES.md) — active
>
> Competitive analysis: [docs/competitive/2026-04-26-memobase-deep-dive.md](docs/competitive/2026-04-26-memobase-deep-dive.md)

---

## Where we are (2026-04-26)

MemDB v0.22.0 is the first public release. The full pre-public iteration history
(v1.x, v2.x) consolidated into a single Go runtime: `memdb-go` + `memdb-mcp` plus
infra sidecars (postgres+AGE+pgvector, redis, qdrant, embed-server). Six
containers total, zero Python in the hot path. The `memdb-api` Python container
was shut down 2026-04-26 after M9 Stream 8 with zero regressions over a 2-day
soak. Retrieval intelligence (Phase D, ten features D1-D11) is shipped and
production-gated. Measurement was upgraded in M9 to Memobase-comparable LLM
Judge methodology, which lets us publish numbers directly against the public bar
(Memobase 75.78%, MemOS 73.31%, Mem0 66.88%).

## What we shipped

| Sprint / Phase | Theme | Highlights | Status |
|---|---|---|---|
| Phase 1-3 | Initial Go port | Add pipeline + LLM extractor v2 + Redis Streams scheduler in Go | Feb-Mar 2026 |
| Phase 4 | Proxy elimination | Native feedback, search/fine, get/delete with complex filter, MCP add+chat, llm/complete, schema migration runner | Apr 2026 |
| Phase A-C | Safety net + integrity + code quality | Heartbeat counters, Prometheus alerts, agtype audit, file splits, release-drafter | Apr 2026 |
| Phase D | Retrieval intelligence (v2.0.0) | D1-D11: temporal decay, multi-hop AGE, hierarchical reorganizer, query rewriting, staged retrieval, post-retrieval enhancement, CoT decomposition, structural edges | Apr 2026 |
| M7 Compound Lift (v2.1.0) | Quality + speed | F1 0.053 -> 0.238 (+349%), -52% p95 chat from `answer_style=factual`, embed batching 13s -> 1.0s | 2026-04-25 |
| M8 Multi-hop + infra | D2 fix, CoT D11, structural edges, GOMEMLIMIT, pprof behind auth | 2026-04-26 |
| M9 Memobase port + Phase 5 | Dual-speaker retrieval, LLM Judge metric, `[mention DATE]` time anchoring, cat-5 exclusion, Python container shutdown | 2026-04-26 |
| v0.22.0 | First public release | Pure-Go runtime, public README, LICENSE/CONTRIBUTING/SECURITY/CODE_OF_CONDUCT, auto-release infra | 2026-04-26 |

Phase D measured delta on `chat/complete` end-to-end (1 conv, 10 cat-1 QAs):
F1 0.143 (+14x vs retrieval-only baseline), semsim 0.150 (+3.3x), hit@20 0.700.
Stage 3 full-corpus run (1986 QA, 10 conversations) is queued — see
"Open measurement gate" below.

## Active workstreams

### Search quality — [ROADMAP-SEARCH.md](ROADMAP-SEARCH.md)

- **Deep search agent** (Python `mem_agent/deepsearch_agent.py` port) —
  QueryRewriter + Reflection loop. Currently no Go equivalent.
- **BGE rerank strategies** beyond CE single-pass — concat / cosine_local /
  noop fallbacks from Python `reranker/strategies/`. Builds on already-shipped
  cross-encoder rerank step.
- **VEC_COT** (sub-question embeddings as probes) — the one MemOS retrieval
  feature we still don't have. D7 already does CoT decomposition for atomic
  multi-part queries; VEC_COT is a separate "embed each sub-Q as a vector
  probe" pattern.

### Add pipeline quality — [ROADMAP-ADD-PIPELINE.md](ROADMAP-ADD-PIPELINE.md)

- **Soft-delete / `expired_at`** (Graphiti-derived) — replace hard-delete in
  `applyDeleteAction` / `applyUpdateAction` with `expired_at` timestamp +
  query-time filter. Unblocks point-in-time queries, undo, audit, and Memory
  Recovery Endpoint (FEATURES #6).
- **OTel tracing across the pipeline** — span hierarchy from `NativeAdd` down
  to per-stage children (classify / extract / embed / apply / background).
  Today only slog timings; bottleneck identification requires breakdown.
- **LLM call semaphore** — bounded concurrency for fire-and-forget background
  goroutines (episodic / skill / tool / entity / preference). Prevents burst
  overflow against CLIProxyAPI rate limits.
- **Source attribution + structured preference taxonomy refinements** —
  builds on D8 22-category extraction shipped in v2.0.0.

### Features — [ROADMAP-FEATURES.md](ROADMAP-FEATURES.md)

- **Image Memory + multimodal** (CLIP embeddings, image+text co-retrieval).
- **MemCube cross-sharing** (read/write permissions between cubes).
- **RawFileMemory + `evolve_to` provenance** — lineage from raw chunk to LTM
  facts; enables "forget this document" + selective re-extraction.
- **Memory lifecycle (5 states)** + versioning — depends on soft-delete.

### M10 candidates (post-M9 backlog)

From [docs/backlog/2026-04-26-followups.md](docs/backlog/2026-04-26-followups.md)
and [docs/competitive/2026-04-26-memobase-deep-dive.md](docs/competitive/2026-04-26-memobase-deep-dive.md):

| Item | Size | Rationale |
|------|------|-----------|
| **Structured `user_profiles` layer** (Memobase moat) | XL (~3-4 weeks) | First-class `topic / sub_topic / memo` table; structured lookups replace cosine search for entity facts. Closes Memobase advantage on cat-1 + cat-4. |
| **Pre-compute CE rerank scores at ingest** | M | Persist pair-wise CE scores in `Memory.properties->>'ce_score_topk'` during D3 reorganizer; query-time CE -> graph lookup. -50-300ms p95 chat. |
| **PageRank on `memory_edges`** | S | Background goroutine computes PageRank, boosts D1 rerank. cat-1 + cat-3 recall lift. |
| **`BulkCopyInsert` / `CypherWriter` for AGE writes** | M | Direct text-format COPY into AGE bypassing Cypher parser; 2-5x speedup on Stage 3 ingest, D3 batch, structural edges. |

## Where we're going (6-12 months)

**Public adoption.** v0.22.0 is the public foundation. Next: HN / Reddit /
Discord launch with worked examples (Telegram bot, IDE copilot memory, customer
support sessions), a hosted demo cube, SDK clients in Go / Python / TypeScript,
and per-use-case cookbooks.

**Match Memobase 75.78% LLM Judge headline.** The M9 measurement upgrade lets us
publish honest comparable numbers. Closing the remaining gap rests on the M10
`user_profiles` layer (structured retrieval beats cosine for entity facts) plus
the Phase D follow-ups (D2 multi-hop diagnosis, hub-and-spoke topology in D3).

**v1.0.0 stability commitment.** After 60+ days of no breaking changes and a
public API soak with external users, version 0.x graduates to 1.0.0 with a
formal compatibility contract. See [docs/versioning.md](docs/versioning.md) for
the current 0.x policy.

**Multimodal + agent integration.** Image Memory (FEATURES #1) plus deeper
out-of-the-box integrations with agent frameworks (LangChain memory adapter,
LlamaIndex memory store, Vercel AI SDK), so MemDB becomes the default
self-hosted memory layer rather than another competitor to evaluate.

## What we're NOT doing

- **Parametric memory (LoRA)** — requires GPU and fine-tuning infrastructure;
  ROI is low for a self-hosted memory store.
- **Activation memory (KV-cache)** — too tightly coupled to a specific LLM
  deployment, breaks the "bring-your-own-LLM" model.
- **Migration to Neo4j** — Apache AGE on Postgres keeps the stack one engine,
  not two. Operational simplicity beats marginal Cypher feature parity.
- **Migration to Milvus** — Qdrant has better self-hosted ergonomics for our
  scale; we already use sparse + dense in one engine.
- **Graph traversal as primary search mode** — AGE traversal is too slow for
  real-time on the 200ms p95 budget; we use graph recall as a re-rank boost,
  not the main retrieval path.

## Open measurement gate

Stage 3 full LoCoMo run (1986 QA across 10 conversations, ~3-5h) is queued but
needs a dedicated session. Infrastructure is ready: `GOMEMLIMIT=4915MiB`,
recovery script at `/tmp/m8-stage3-runner.sh`. The headline LLM Judge number
for v0.22.0 will land once that run completes. Until then, comparable public
numbers are scoped to the smaller cat-1 sample and the M7 single-conv reference
(F1 0.238, hit@k 0.769 on conv-26).

## How to contribute

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, coding conventions, and PR
flow. M10 candidate items above are independent and good first sprints — pick
one, open a design issue first, then a PR. For larger architectural proposals
(structured profiles, multimodal, agent integrations) start with a GitHub
Discussion so we can align on shape before code.

## Releases

- Latest: **v0.22.0** — 2026-04-26 — first public release.
  ([release notes](docs/release-notes/v0.22.0.md))
- Versioning policy: [docs/versioning.md](docs/versioning.md) — 0.x phase,
  expect minor breaking changes pre-1.0.
- Full changelog: [CHANGELOG.md](CHANGELOG.md)
