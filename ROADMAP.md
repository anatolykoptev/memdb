# MemDB Roadmap

> Current version: **v0.23.0** (M10 user_profiles + perf + audit, 2026-04-26).
> Pure-Go stack, post-Python migration. Memobase-comparable LLM Judge measurement
> layer. Headline: **72.5% LLM Judge** on LoCoMo chat-50 (excl cat-5).
>
> This is the **master roadmap** — a single-page view across migration, search quality,
> add pipeline, and features. Detailed plans live in:
> - [docs/ROADMAP-GO-MIGRATION.md](docs/ROADMAP-GO-MIGRATION.md) — closed
> - [docs/backlog/search.md](docs/backlog/search.md) — active
> - [docs/backlog/add-pipeline.md](docs/backlog/add-pipeline.md) — active
> - [docs/backlog/features.md](docs/backlog/features.md) — active
>
> Competitive analysis: [docs/competitive/2026-04-26-memobase-deep-dive.md](docs/competitive/2026-04-26-memobase-deep-dive.md)

---

## Where we are (2026-04-26)

MemDB v0.23.0 ships the M10 sprint on top of the v0.22.0 public foundation.
The runtime is unchanged: `memdb-go` + `memdb-mcp` plus infra sidecars
(postgres+AGE+pgvector, redis, qdrant, embed-server). Six containers total,
zero Python in the hot path.

M10 added the structured `user_profiles` layer (Memobase moat), L1/L2/L3
memory-layer API skin, Helm chart, cross-encoder pre-compute at ingest,
PageRank background scheduler, reward-loop scaffolding, plus a five-finding
internal security audit (cube isolation, prompt-injection mitigation, DoS
admission control, multi-replica advisory lock, search cache key
correctness). The audit shipped in the same release.

Headline: **72.5% LLM Judge** on chat-50 stratified (excl cat-5, Memobase
convention) — up from 70.0% in v0.22.0 (+2.5pp). Position: between MemOS
(73.31%) and Zep (75.14%), +5.62pp ahead of Mem0 (66.88%), -2.64pp short of Zep, -3.28pp short of Memobase (75.78%) leader.

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
| **M10 user_profiles + perf + audit (v0.23.0)** | Memobase profile layer (S1/S2/S3), L1/L2/L3 API (S4), Helm chart (S5), CE precompute (S6), PageRank (S7), reward scaffold (S8). 5 audit fixes (C1/C2/C3/I4/P3). 72.5% LLM Judge (+2.5pp), 7.5× faster ingest. | 2026-04-26 |
| **M11 14-stream sprint** | F7 temporal index, F8 atomic per-fact extraction (mem0 ADDITIVE), F11 bi-temporal edges (Graphiti/Zep), F12 linked_memory_ids resolver, F14 Personalized PageRank (HippoRAG 2), F2 reflection-loop, F9 recall budget tuning, F3 event extraction + 4 followup buckets. Measured 41.5% headline → regression vs M10 chat-50 stratified (72.5%). | 2026-04-28 |
| **M12 recovery** | M12.1 harness ts-prefix fix, M12.2 chat prompt softening, M12.5 observability metrics, M12.6 staged rerank bge-reranker backend, M12.7 LLM Judge revival (60% top-1 swap rate), M12.11 go-kit/embed extraction, agtype migration cycle (PR #167), bfs_expand 1830× index speedup (PR #169). | 2026-04-29 |
| **M13 evaluation harness v2 (industrial-grade)** | J1 core judge robustness, J2 programmatic judges per cat (numeric/temporal/multi-fact), J5 statistical rigor (bootstrap CI, McNemar, Cohen's κ), J3-Q multi-query expansion (HyDE-style; replaces J3 ensemble), W1 go-kit/rerank v2 wire-up. Open-source harness + arXiv methodology paper planned. | 2026-04-29 |
| **M14 MathReranker integration** | A1 Stage-0 pre-CE diversity prefilter (PR #183 — λ=0.5, env-gated), B1 CE precompute background prefilter (PR #182 — env-gated), go-kit v0.29.0 bump (subsumed PR #181). Both env-gated default OFF; opt-in via `~/deploy/server-config/.env`. | 2026-04-29 |

Phase D measured delta on `chat/complete` end-to-end (1 conv, 10 cat-1 QAs):
F1 0.143 (+14x vs retrieval-only baseline), semsim 0.150 (+3.3x), hit@20 0.700.
M9 Stage 3 v3 completed the full-corpus run (1986 QA, 10 conversations) — see
"Latest measurement" below for results.

## Post-merge ops checklist (M14)

Track: enable M14 features in production env after PR merge + dozor auto-deploy. **All flags default OFF in code.** Flip in `~/deploy/server-config/.env`, then `docker compose restart memdb-go memdb-mcp` (env reload only — no rebuild). Owner: controller.

### B1 — CE precompute background math prefilter (PR #182, safe to enable on merge)

Add to `~/deploy/server-config/.env`:

```bash
# M14 B1 — drop low-cosine pairs before bge-reranker call
# threshold=0.5 (stricter than legacy hardcoded floor 0.30 → real CPU saving)
MEMDB_CE_PRECOMPUTE_MATH_PREFILTER=1
MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD=0.5
```

Verify:
- `memdb_scheduler_ce_precompute_math_skipped_total > 0` after first scheduler tick.
- `embed_server` Prometheus shows reduced CE batch volume (target −20–40%).

Risk: low. Background scheduler only, not on search hot path. Threshold 0.5 ≪ cosine median (0.6–0.8 in our corpus), safe upper bound. Fallback: unset env, restart.

### A1 — Stage-0 pre-CE MathReranker prefilter (PR #183, A/B before flipping)

**Do NOT enable on merge.** Quality feature — needs A/B measurement against M12 baseline first.

Plan:
1. Merge #183 with default OFF (no behavior change).
2. Run A/B: 50 LoCoMo QA each with `MEMDB_RERANK_MATH_PREFILTER=1 MEMDB_RERANK_MATH_LAMBDA=0.5` vs unset.
3. Compare cat-1 (multi-fact aggregation) headline. Hypothesis: +2-3pp.
4. If positive, add to `~/deploy/server-config/.env`:

```bash
# M14 A1 — Stage-0 cosine+MMR diversity-aware pre-CE prefilter
MEMDB_RERANK_MATH_PREFILTER=1
MEMDB_RERANK_MATH_LAMBDA=0.5
```

Verify:
- `memdb_search_math_prefilter_engaged_total > 0` after first /search.
- `memdb_search_math_prefilter_skipped_total{reason="no_query_vec"}` ≈ 0 (sanity).
- Cat-1 LLM Judge metric ≥ M12 baseline.

Risk: medium. Reorders CE input → can shift recall on edge queries. Mitigated by env-gate + small A/B sample.

### B1.1 follow-up — replace hardcoded `cePrecomputeMinNeighbourCosine = 0.30` with env (task #67)

After B1 is in prod and stable, fold the legacy hardcoded floor into the same `MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD` env. Default 0.3 = parity. Eliminates dual filter, brings legacy filter under B1's metrics.

### M14.X Stage 1 — pipeline magic-number audit (tasks #68, #69)

Wrap 10+ found hardcoded thresholds (`similarity.go`, `dedup.go`, `merge.go`, `linked_expand.go`, `config.go` decay weights) in env-gated reads + Prometheus gauges. Defaults unchanged → zero behavior change. Goal: observability, not retuning. Rationale: `feedback_pipeline_magic_numbers.md` — silent magic numbers are silent regression vectors (M11 incident pattern).

## Active workstreams

### Search quality — [docs/backlog/search.md](docs/backlog/search.md)

- **Deep search agent** (Python `mem_agent/deepsearch_agent.py` port) —
  QueryRewriter + Reflection loop. Currently no Go equivalent.
- **BGE rerank strategies** beyond CE single-pass — concat / cosine_local /
  noop fallbacks from Python `reranker/strategies/`. Builds on already-shipped
  cross-encoder rerank step.
- **VEC_COT** (sub-question embeddings as probes) — the one MemOS retrieval
  feature we still don't have. D7 already does CoT decomposition for atomic
  multi-part queries; VEC_COT is a separate "embed each sub-Q as a vector
  probe" pattern.

### Add pipeline quality — [docs/backlog/add-pipeline.md](docs/backlog/add-pipeline.md)

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

### Features — [docs/backlog/features.md](docs/backlog/features.md)

- **Image Memory + multimodal** (CLIP embeddings, image+text co-retrieval).
- **MemCube cross-sharing** (read/write permissions between cubes).
- **RawFileMemory + `evolve_to` provenance** — lineage from raw chunk to LTM
  facts; enables "forget this document" + selective re-extraction.
- **Memory lifecycle (5 states)** + versioning — depends on soft-delete.

### M11 candidates (post-v0.23.0 backlog)

All M10 / upstream-MemOS items have shipped in v0.23.0. The M11 backlog is
seeded from this sprint's deferred work + audit follow-ups.

| Item | Size | Rationale |
|------|------|-----------|
| **Close the reward loop (S8 reads)** | M | `feedback_events` + `extract_examples` tables + write paths shipped in v0.23.0 (S8). M11 wires reads into D1 importance scoring + extract-prompt example bank. Targets cat-3 preference gap. |
| **D2 BFS recall lift for cat-2** | M | Full-corpus cat-2 LLM Judge is 29% (chat-50 is 80% — the gap is recall, not generation). Hub-and-spoke topology in D3 + tuned hop-decay. |
| **Parallelize CE precompute at D3 reorganizer** | S | Currently per-memory sequential. Worker pool of 4 should halve D3 phase wall-time on cold ingest. |
| **`COPY FROM` bulk inserts for `memory_edges` + `entity_nodes`** | M | The 7.5× ingest speedup leaves AGE Cypher inserts as the next bottleneck. Direct text-format COPY can win another 2-3× on Stage 3 scale. |
| **GIN index on `Memory.properties->'ce_score_topk'`** | S | The S6 lookup is currently btree on graphid; a GIN expression index would make the merge O(log N) at scale. |
| **Semantic prompt-injection classifier** | M | C2 catches structural attacks via tag-wrapping + sanitization. A small classifier (or regex bank) would catch semantic payloads ("ignore previous instructions") embedded in benign-looking memos. |
| **Migration `0018`: NULL `cube_id` reaper for `user_profiles`** | XS | After M10 production has only NULL legacy rows from pre-`0017` development; reap them and flip column to `NOT NULL`. |
| **PageRank advisory-lock observability** | XS | Add `pagerank_skipped_total{replica}` counter so we can see which replica wins per interval. |

What we explicitly do **not** take from MemOS upstream: Electron desktop app
(`apps/memos-local-plugin/`), OpenClaw browser plugin
(`apps/memos-local-openclaw/`), 1394-file kitchen-sink monorepo. Their direction is
end-user productivity suite; ours is backend infrastructure component. Different
audiences, different SKUs.

## Where we're going (6-12 months)

**Public adoption.** v0.22.0 was the public foundation; v0.23.0 raises the
quality bar to within 3.28pp of the Memobase leader. Next: HN / Reddit /
Discord launch with worked examples (Telegram bot, IDE copilot memory,
customer support sessions), a hosted demo cube, SDK clients in Go / Python /
TypeScript, and per-use-case cookbooks.

**Match Memobase 75.78% LLM Judge headline.** v0.23.0 closes the gap to
-3.28pp via the `user_profiles` layer plus CE precompute and PageRank.
Remaining gap rests on M11 cat-2 work (full-corpus 29% is the lowest non-cat-5
category) plus deeper profile coverage per conversation.

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

## Latest measurement (2026-04-26 — v0.23.0 / M10 Phase 4)

Stack: Phase D (D1-D11) + M7/M8/M9 + M10 (user_profiles + CE precompute +
PageRank). Methodology: Memobase-comparable LLM Judge (Gemini Flash binary
judge), dual-speaker harness, two-track aggregate (incl / excl cat-5).

| Track | All cats | Excl cat-5 (Memobase convention) |
|-------|----------|----------------------------------|
| Chat-50 stratified, end-to-end (n=50/40) | F1 0.138, **LLM Judge 62.0%** | F1 0.151, **LLM Judge 72.5%** |
| Full corpus 1986 QAs, end-to-end (n=1986/1540) | F1 0.153, LLM Judge 41.8% | F1 0.178, **LLM Judge 50.9%** |

Per-category LLM Judge (chat-50 stratified): cat-1 60% · cat-2 80% · cat-3 70%
· cat-4 80% · cat-5 20%.
Per-category LLM Judge (full 1986): cat-1 53.5% · cat-2 29.0% · cat-3 37.5%
· cat-4 59.9% · cat-5 10.3%.

vs public leaderboard (excl cat-5):
- Memobase 75.78% · **Zep 75.14%** · MemOS 73.31% · **MemDB v0.23.0 72.5%** · Mem0 66.88%

Position: between MemOS and Zep, **+5.62pp ahead of Mem0**, **-0.81pp
short of MemOS**, **-2.64pp short of Zep**, **-3.28pp short of Memobase leader**. Up from M9 70.0%
(+2.5pp). M11 cat-2 BFS work + deeper profile coverage are the path to closing
the remaining gap. Wall-time: ingest 40min (7.5× faster than M9), query
phase ~10h with 4 outer workers + `D2_MAX_HOP=2`. Full breakdown in
[evaluation/locomo/MILESTONES.md](evaluation/locomo/MILESTONES.md).

## How to contribute

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, coding conventions, and PR
flow. M10 candidate items above are independent and good first sprints — pick
one, open a design issue first, then a PR. For larger architectural proposals
(structured profiles, multimodal, agent integrations) start with a GitHub
Discussion so we can align on shape before code.

## Releases

- Latest: **v0.23.0** — 2026-04-26 — M10 user_profiles + perf + audit.
  ([release notes](docs/release-notes/v0.23.0.md))
- Previous: **v0.22.0** — 2026-04-26 — first public release.
  ([release notes](docs/release-notes/v0.22.0.md))
- Versioning policy: [docs/versioning.md](docs/versioning.md) — 0.x phase,
  expect minor breaking changes pre-1.0.
- Full changelog: [CHANGELOG.md](CHANGELOG.md)
