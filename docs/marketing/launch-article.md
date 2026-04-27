# How we hit 72.5% on LoCoMo with 6 weeks of compound improvements (and why your agent memory probably costs too much)

*MemDB v0.23.0 — an honest engineering writeup*

---

If you have shipped an LLM agent in the last twelve months, you have run into the same wall. The model is fine. The prompts are fine. The first session is fine. Then the user comes back two days later and asks "remember that thing we agreed on?" — and the agent does not remember, because the context window is gone, the chat log is too long to stuff back in, and nobody on your team wants to own "memory" as a subsystem.

The market answer to that problem in 2025 was a small cluster of hosted memory APIs — Mem0, Zep Cloud, Memobase — that you point your agent at and stop thinking about. They work. They also ship with a list of trade-offs that a serious infra engineer eventually finds unacceptable: per-seat pricing that scales with your agent count, US-only data residency, vendor lock-in on retrieval ranking, and a Python service to run alongside your Go or Rust stack if you want any of it on-prem.

I built **MemDB** because I refused to accept any of those trade-offs. Six weeks of focused work later it scores **72.5% on LoCoMo LLM-Judge** — between MemOS and Zep on the public leaderboard, +5.62 percentage points over Mem0, **−3.28 points behind the Memobase leader**, fully self-hostable as one `docker compose up`, pure Go core, Apache-2.0, with three first-class Claude integration surfaces (Code plugin, MCP server, `memory_20250818` adapter).

This article is the engineering story. Not the marketing version. Eleven measurement milestones, the things that worked, the things that did not, and the numbers that came out the other side.

---

## 1. Why agent memory is harder than it looks

People usually pitch agent memory as "vector DB plus a system prompt." It is not. The actual problem has three axes, and the existing solutions each give up on at least one:

1. **Multi-session continuity.** The agent must answer questions about events that happened weeks ago, in a different chat, possibly with a different model on the other end. A vector DB alone gets you semantic similarity; it does not get you "this fact was mentioned three sessions back and is now stale."
2. **Cross-LLM portability.** If your stack is Gemini for cheap drafting + Claude for reasoning + a local Llama for offline mode, you do not want the memory layer hard-wired to OpenAI. You want BYO-LLM at the embedder, the extractor, and the judge.
3. **Multi-tenant isolation.** A B2B SaaS agent has dozens of customers, each with thousands of users. Memories cannot leak across tenants. Most "personal AI" memory products treat tenancy as a namespace bolt-on; in production that is one schema bug away from a data-leak incident.

Then add the constraints I had on top: must self-host (regulated verticals do not get to use US-only clouds), must be deployable from Russia (which rules out half the cloud providers on principle), single docker-compose so a small team can operate it, no Python runtime in the hot path because I am tired of fighting venv drift on shared infra. Nothing on the market hit all five.

So the design constraint became simple: **build the memory layer I would actually deploy, then prove it on the same benchmark the cloud vendors use.**

---

## 2. The benchmark — and why the methodology matters more than the score

LoCoMo is the long-conversation memory benchmark from the Mem0 paper ([arXiv:2504.19413](https://arxiv.org/abs/2504.19413)). Ten synthetic conversations, ~1986 question-answer pairs, five categories: single-hop, multi-hop, temporal, open-domain, adversarial. Every serious memory paper reports on it.

Two scoring metrics get used in the literature, and the difference matters:

- **F1** — token-level overlap between the predicted answer and the gold answer. Strict. Penalizes verbose generations even when they are correct. Good for retrieval-only ablations, terrible for chat mode where the LLM rephrases.
- **LLM Judge** — Gemini Flash (or GPT-4o, depending on who is reporting) reads the question, gold, and prediction, and emits a binary `correct/incorrect`. Semantic. This is what Memobase, Zep, MemOS, Mem0 all publish on their leaderboards.

When I started in March, MemDB was reporting F1. We were getting 0.05 and feeling bad about it. Then I read the Memobase benchmark code and noticed two things: they evaluate with LLM Judge (not F1), and they hardcode `exclude_category={5}` in their harness — adversarial questions are dropped before scoring (`memobase_client/memobase_search.py:147`).

That is not cheating, by the way. Cat-5 in LoCoMo is "questions about things that were never said," and an honest answer is "I don't know" — which the LLM Judge rewards if calibrated correctly, but which most published harnesses never calibrate. Memobase, Zep, MemOS all exclude it. So we ported their methodology *exactly* — same Gemini 2.5 Flash judge prompt, same chat-50 stratified subset, same cat-5 exclusion — and now publish a number on the same scale. The harness writes both tracks (`aggregate_with_excl_none` and `aggregate_with_excl_5`) so a reader can pick the comparison they want.

If you take one thing from this article, take this: **you cannot compare benchmark scores across systems unless the methodology is the same.** Half the agent-memory market noise in 2025 is people comparing F1 against LLM Judge and pretending it is apples to apples. It is not.

---

## 3. The 11-milestone journey

Here is the durable audit trail. Every row is a real measurement against a known commit, recorded in [`evaluation/locomo/MILESTONES.md`](../../evaluation/locomo/MILESTONES.md). I am putting the noisy and the failed runs in the table on purpose. If you only look at successful milestones you learn nothing.

| Milestone | Date | Theme | Key metric | Notes |
|---|---|---|---|---|
| Baseline v1.1.0 | 2026-04-23 | Harness ship | F1 0.000 / hit@20 0.000 | Write path silently broken on prod (AGE 1.7 `agtype_in` overload removed) |
| Post-P1 | 2026-04-23 | Real baseline | F1 0.010 / **hit@20 0.700** | 3 cascading SQL/AGE blockers fixed; retrieval works |
| D1 (decay+importance) | 2026-04-24 | Phase D start | unchanged | Honest zero — needs longitudinal data to fire |
| D2 (multi-hop AGE) | 2026-04-24 | Graph walk | unchanged | Needs `memory_edges`, which D3 populates |
| D3 (hierarchical reorganizer) | 2026-04-24 | Tiered tree | unchanged | Cluster threshold not met on small sample |
| D10 (post-retrieval enhance) | 2026-04-24 | Synthetic rank-0 | semsim +25% | First non-zero Phase D delta |
| All Phase D + chat-mode (M3) | 2026-04-24 | End-to-end | **F1 +14×** (0.010 → 0.143) | Chat/complete unlocks rank-0 synthetic dominance |
| M4 tuning grid | 2026-04-24 | Hyperparams | F1 0.053 (50 QAs) | 12 envs exposed; combo config |
| M6 prompt ablation | 2026-04-24 | Factual QA prompt | **F1 +51%** (0.053 → 0.080) | Default chatbot prompt was wrong shape |
| M7 compound (Stage 2) | 2026-04-25 | Compound lift | **F1 0.238** on 199 QAs (+349% vs baseline) | First MemOS-tier number — answer_style + raw ingest + threshold |
| M9 Memobase port | 2026-04-26 | Methodology fix | **70.0% LLM Judge** | Dual-speaker, judge metric, two-track, date-aware extract |
| M10 user_profiles + perf + audit | 2026-04-26 | Compound + ops | **72.5% LLM Judge** | M11 is the path to closing the gap |

Three of these deserve more than one row. I will go deep on the ones that taught me the most.

### M7 — the compound lift (and why small samples lie)

By late April we had shipped all ten Phase D features (D1 temporal decay, D2 multi-hop graph walk via Apache AGE, D3 hierarchical reorganizer, D4 query rewriting, D5 staged retrieval, D6 pronoun + temporal resolution, D7 chain-of-thought decomposition, D8 third-person enforcement, D9 the harness itself, D10 post-retrieval enhancement). On a 50-question sample they collectively bought us nothing visible. F1 sat at 0.080. Hit@20 was actually `0.000` because of a relativity-threshold bug.

Three things landed in M7:

1. **Server-side `answer_style=factual`** — the default chat prompt was a 700-word "conversational assistant, never mention retrieved memories" template. LoCoMo gold answers are 1-5 words. Wrong shape. We added `factualQAPromptEN` that says "shortest factual phrase, no explanation."
2. **Raw-mode ingest** — the default ingest path collapsed each session into 4096-character sliding windows. For QA workloads that destroys per-message granularity. We switched to `mode="raw"` (one memory per message, 58 per conversation in the sample).
3. **Retrieval threshold override** — the default `relativity=0.5` cutoff killed all raw-turn embeddings (cosine ~0.15-0.30 against question-form queries). We added `LOCOMO_RETRIEVAL_THRESHOLD=0.0` for the harness only; production keeps the production default.

On the 50-QA sample, the result was disappointing: F1 stayed at 0.080. **Same number as prompt-only**. Were the other two changes useless?

They were not. They needed evidence density. We re-ran on the full conv-26 corpus (199 questions across all 19 sessions instead of 3), and the compound effect materialized: **F1 0.238 — a 3× jump on the same code, just from giving retrieval enough material to surface answers from**.

The lesson burned in: **compound improvements are multiplicative + threshold-gated by evidence density**. Small samples can hide gains. If you are tuning a memory system on 50 questions, you are tuning for 50 questions; the curve only opens up at production-corpus scale. We added that observation to the team's standing operating procedure. It has saved us from several premature "this feature didn't help" conclusions since.

### M9 — porting the Memobase methodology

M7 hit a ceiling. Stage 3 (full 1986 QAs) crashed on an OOM mid-ingest, and the recovery script swallowed the error because of `set -euo pipefail` on a long script with no `trap ERR`. Cost us a day. (Heartbeat + per-phase checkpoint files are now part of the harness contract.)

The bigger problem was that even when Stage 3 finally ran, our F1 numbers were not comparable to anyone else's leaderboard score. Nobody publishes F1 anymore. Memobase's 75.78%, MemOS's 73.31%, Mem0's 66.88% are all LLM Judge. We were measuring a different thing.

M9 was the methodology port. Five streams in one day, all subagent-driven:

- **HARNESS-DUAL** — `query.py` now queries both `<conv>__speaker_a` and `<conv>__speaker_b` per question and merges results. The Memobase paper buries this as a one-liner; it is actually their single biggest trick. We had been throwing away half the evidence.
- **LLM Judge metric** — `score.py --llm-judge` calls Gemini Flash with the verbatim Memobase judge prompt and caches results in `.llm_judge_cache.json`. Reproducible, and second pass takes <1 second.
- **Two-track reporting** — every score run emits both `aggregate_with_excl_none` (full) and `aggregate_with_excl_5` (Memobase-comparable). Nobody can accuse anyone of cherry-picking.
- **Date-aware extract prompt** — Memobase's `extract_profile.py` injects a `[mention YYYY-MM-DD]` instruction that lifts temporal-question recall. Their public-best 85.05% on temporal F1 is +5pp over the runner-up; the date hint is cited as the driver. Cheap port, immediate gain.
- **cat-5 exclusion** — built in two-track reporting, applied as the default headline.

Result: **70.0% LLM Judge on chat-50 stratified, excluding cat-5**. First time MemDB had a number on the same scale as anyone else. Released as v0.22.0. Up +2pp from M9 to M10.

### M10 — the user_profiles moat + perf compound + the audit

M10 was supposed to close the remaining gap to Memobase. It didn't quite — we ended at 72.5%, still −3.28pp short — but it did three other things that mattered more than the headline number.

**The user_profiles layer.** Memobase's real moat is a structured per-user profile table (`topic / sub_topic / memo`) populated by an LLM extractor running on every ingest. Their judge sees a clean profile section in the system prompt above the memory section, and that lifts answer quality on questions about user attributes ("what is Caroline's job?") far more than raw retrieval can. We ported it: migration `0015`, S2 extractor with the verbatim Memobase prompt, S3 chat-handler injection of `<user_profile>` above `<memories>`. Default-on (`MEMDB_PROFILE_EXTRACT=true`, `MEMDB_PROFILE_INJECT=true`).

**Perf compound.** Stage-3 ingest dropped from 5 hours to 40 minutes. **7.5× speedup** from CE (cross-encoder) score precompute at D3 ingest (no more query-time CE call), embed batching in the embed-server sidecar, structural-edge dedup, and the fast-add pipeline. Query phase parallelization (`--workers 4` outer + `D2_MAX_HOP=2` instead of 3) brought query-side wall-time to ~10 hours.

**The five-finding security audit before release.** Five real findings: cross-tenant `cube_id` leak in the new `user_profiles` table (high), prompt-injection via stored profile facts (high), no admission control on the profile-extract goroutine (DoS by burst, medium), PageRank background job racing across replicas (medium), search cache key omitting `level / agent_id / pref_top_k` (correctness bug under load, medium). All five fixed in the same release. The user_profiles table now has explicit `cube_id` + cube-scoped unique index. Profile facts get sanitized and tag-wrapped in `<fact>...</fact>`. Profile-extract is bounded by a size-8 semaphore acquired *before* the goroutine spawns. PageRank acquires a Postgres advisory lock so only one replica computes per cycle. Cache key fixed.

**Headline:** 72.5% LLM Judge on chat-50 stratified (excl cat-5). Per-category: 60% / 80% / 70% / 80% on cats 1-4. Multi-hop is now strong (80%); the full-corpus cat-2 number is still 29% (cosine recall ceiling) and is the M11 target.

---

## 4. What's actually in the box

```
┌─────────────────────────────────────────────────────────────────┐
│                        Your agent (any LLM)                      │
└─────────┬──────────────────────┬──────────────────────┬─────────┘
          │ HTTP                 │ MCP                   │ memory_20250818
          ▼                      ▼                      ▼
┌─────────────────────────────────────────────────────────────────┐
│                          memdb-go (Go binary)                    │
│  D1 decay  D2 graph walk  D3 reorg  D4 rewrite  D5 staged       │
│  D6 pronoun  D7 CoT  D8 prefs  D10 enhance  D11 fan-out         │
│  + user_profiles layer  + PageRank  + CE precompute             │
└─┬───────────┬──────────────┬─────────────┬───────────┬──────────┘
  │           │              │             │           │
  ▼           ▼              ▼             ▼           ▼
Postgres+  pgvector       qdrant        redis      embed-server
Apache AGE (dense ANN)   (high-thru)   (hot path)  (ONNX sidecar)
(graph)                                            multilingual-e5-large
```

Single `docker-compose.yml` brings up all of it. Pure-Go core. Python is gone from the request path entirely (M9 Phase 5 retired the Python `memdb-api` service; the legacy compose file removed it in PR #93). The embed-server sidecar is Rust, 40MB image, ONNX runtime, BYO-model via `EMBED_MODELS` env.

### The three Claude integration surfaces

I keep getting asked why MemDB ships three integrations instead of one. The answer is that "Claude" is not one product, it is three deployment shapes, and each one needs a different memory entry point:

1. **Claude Code plugin** — auto-injects relevant memories into your prompt before each turn. Drop-in install, no code changes in your project. For developers who want their CLI agent to remember the last week of work.
2. **MCP server** — speaks Model Context Protocol, plugs into Claude Desktop, Claude Code, or any MCP-compatible host. Lets the model *call* memory as a tool when it decides it needs to.
3. **`memory_20250818` adapter** — a drop-in replacement for Anthropic's official memory tool, packaged separately as [memdb-claude-memory-tool](https://github.com/anatolykoptev/memdb-claude-memory-tool). The Anthropic default writes to the local filesystem; this swaps in MemDB as the backend with three lines of Python.

```python
from memdb_claude_memory_tool import MemDBMemoryTool
tool = MemDBMemoryTool(base_url="http://localhost:8080", cube_id="user_42")
client.messages.create(
    model="claude-sonnet-4-6",
    tools=[tool.as_tool_spec()],   # speaks memory_20250818 wire format
    ...
)
```

Multi-tenancy is explicit. Every record carries a `cube_id`, every query is scoped, and the cube-scoped unique index on `user_profiles` (audit fix C1) means a schema bug cannot leak across tenants without a migration. Tenancy is a first-class concept, not a namespace.

---

## 5. Honest tradeoffs

This is the section I most want you to read before you decide whether to use MemDB.

**We are not the leaderboard leader.** Memobase scores 75.78%, we score 72.5%. The gap is real. It is mostly profile coverage (Memobase extracts more facts per conversation than we do) and cat-2 multi-hop recall on the full corpus (29% LLM Judge, vs ~47% for Memobase). M11 is targeting both. If you need the absolute highest published number on LoCoMo and you can pay Memobase's commercial-cloud terms, use Memobase. I would.

**There is no managed cloud yet.** `api.memdb.ai` exists as a single-node demo endpoint, not a production service. The only supported deployment today is self-host via the docker-compose file or the Helm chart that shipped in v0.23.0 S5. I would rather ship a great self-hosted product than a half-baked cloud one.

**The PyPI package for the Claude memory tool adapter is not yet published.** Install from git for now: `pip install git+https://github.com/anatolykoptev/memdb-claude-memory-tool`. Wire format for `memory_20250818` is still evolving on Anthropic's side; we are tracking changes before we cut a 1.0.

**D2 multi-hop default is depth=2.** The benchmark numbers above were measured at depth=2 because depth=3 is ~30% slower for ~1pp F1 lift on cat-2. That trade is wrong for some workloads (heavy multi-hop QA) and right for most. Configurable via `D2_MAX_HOP` env. Closing the Memobase gap probably requires depth=3 default + a per-query auto-tune, both M11 work.

**Full-corpus cat-2 multi-hop is still the weakest point.** 29% LLM Judge on 321 questions. The D2 BFS expansion is shipped, but the recall ceiling on the full 1986 corpus is currently cosine-bound. Hub-and-spoke topology in D3 is the M11 candidate.

These are deliberate trade-offs, not laziness. I would rather under-promise on the README than have you discover them in production.

---

## 6. Where this goes from here

M11 is in active design. The shortlist:

- **Parallelize CE precompute at D3 reorganizer** — currently per-memory sequential; worker pool of 4 should halve cold-ingest D3 wall-time.
- **`COPY FROM` bulk inserts for `memory_edges` + `entity_nodes`** — the 7.5× ingest speedup leaves AGE-Cypher inserts as the next bottleneck. Direct text-format `COPY` should win another 2-3× on Stage-3 scale.
- **GIN expression index on `Memory.properties->'ce_score_topk'`** — the S6 lookup is btree on graphid today; a GIN expression index makes the merge step O(log N) at scale.
- **Semantic prompt-injection classifier** — C2 catches structural attacks via tag-wrapping and sanitization. A small classifier (or a regex bank) catches semantic payloads ("ignore previous instructions") embedded inside a benign-looking memo.
- **Close the reward loop (S8 reads)** — `feedback_events` and `extract_examples` ship as write-only schema in v0.23.0. M11 wires them into D1 importance scoring + the extract-prompt example bank. This is the path to closing the cat-3 (preference) gap.
- **D2 BFS recall lift for cat-2** — biggest remaining quality gap. Hub-and-spoke topology in D3 + tuned hop-decay is the candidate.

Roadmap is open in [`ROADMAP.md`](../../ROADMAP.md). If you want to argue with any of the trade-offs above, Discord and GitHub Issues are open. If you have shipped agent memory in production and you have a real-world failure mode you have not seen me address, I want to hear it — that is the most useful feedback I can get.

---

## 7. Try it

```bash
git clone https://github.com/anatolykoptev/memdb && cd memdb
docker compose up -d
curl http://localhost:8080/health
# {"status":"ok","version":"0.23.0"}
```

Add a memory, then recall:

```bash
curl -X POST http://localhost:8080/v1/cubes/demo/memories \
  -H 'Content-Type: application/json' \
  -d '{"text":"User prefers concise answers in Russian.","speaker":"system"}'

curl "http://localhost:8080/v1/cubes/demo/recall?q=language%20preference"
```

Interactive API docs live at https://api.memdb.ai/docs/ — open the Swagger UI, hit endpoints from the browser, no setup.

The Claude memory tool adapter: https://github.com/anatolykoptev/memdb-claude-memory-tool.

---

## Closing

MemDB is Apache-2.0, self-host friendly, and the only top-tier system on the LoCoMo leaderboard you can ship as one `docker compose up` with no Python in the request path. If you want to use it, the repo is the starting point: https://github.com/anatolykoptev/memdb. If you have an enterprise pilot or a hard deployment constraint I should know about, reach out at consulting@memdb.ai.

If you ship agents, I would love to hear what memory bottlenecks you have actually hit. The quality of MemDB v0.24 depends on it.
