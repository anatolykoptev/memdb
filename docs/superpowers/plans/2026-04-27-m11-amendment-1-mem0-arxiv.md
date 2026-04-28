# M11 Amendment 1 — mem0 + arxiv findings

> **Why this amendment:** the original M11 plan (`2026-04-27-m11-locomo-leadership.md`) was synthesized from MemOS + Memobase + pain-points + metrics. After dispatching mem0 deep-dive + arxiv 2025-2026 research, **two new structural patterns** dominate everything else in expected impact:
>
> 1. **mem0's atomic per-fact extraction** explains their cat-2 51.15% advantage over us (29%) — the gap is **+22pp on cat-2** despite mem0 being 5.6pp behind on headline. Largest single-stream lever in M11.
> 2. **Bi-temporal edges (Zep / Graphiti, arxiv 2501.13956)** — directly addresses cat-2 76% temporal "When..." domination. ICLR-tier evidence: +18.5% accuracy on LongMemEval.
>
> **Headline target update:** from +3.5pp (76.0%) to **+5-7pp (77.5-79.5%)**, putting MemDB at or above Memobase **and** Zep simultaneously. The original plan's 76.0% is the floor, not the ceiling.

---

## A. mem0 cat-2 paradox — root cause

| Factor | mem0 | MemDB v0.23.0 |
|---|---|---|
| Facts per 10-msg chunk | **5-15 atomic, 15-80 words each, self-contained** | **1 LongTermMemory** (paragraph-aggregated) |
| Graph linkage | `linked_memory_ids` emitted **at extract time** by LLM | D3 reorganizer postfacto |
| Eval `top_k` | **30 per speaker × 2 = 60 candidates** | 25 + similarity floor 0.2 |
| ADD/UPDATE/DELETE policy | **ADD-only**; contradictions live as linked facts | D8 deletes losing fact |
| Reverse-role ingest | conv ingested **twice** with role swap | dual-speaker, no swap |
| mem0-Graph variant | **47.19% on cat-2 (worse than vanilla mem0!)** | our D2 may be similarly mis-fed |

**Mechanism**: a multi-hop Q like "What did Marcus's wife do after his promotion?" requires **two atomic facts** with independent embeddings. Our paragraph aggregation dilutes each "hop" — its semantic mass is spread across the paragraph and no single embedding focuses on the bridging fact.

**Critical anti-pattern detected (mem0-Graph regression)**: their own graph variant performs *worse* on cat-2 than vanilla mem0 because graph triples flatten narrative ("User -[switched_to]-> oat_milk" loses "after sensitivity in Mar 2024"). **Suspicion**: our D2 + AGE traversal may be doing the same — pushing edges into the answer prompt without paragraph context. **M1 ablation must test cat-2 with D2 ON vs OFF.**

**Source citations:**
- Atomic-fact prompt: `mem0/configs/prompts.py::ADDITIVE_EXTRACTION_PROMPT` (944 LOC, lines 468-944).
- Search: `mem0/memory/main.py::search` lines 1126-1237; `_search_vector_store` 1343-1410.
- Graph hurts cat-2: `evaluation/src/memzero/search.py:42-87` + `prompts.py::ANSWER_PROMPT_GRAPH:1-62`.
- Eval harness: `evaluation/src/memzero/{search,add}.py` (per-speaker user_id, reverse-role ingest, top_k=30).
- ADD-only: `mem0/memory/main.py::_add_to_vector_store` 662-855.

## B. Zep — bi-temporal edges + 3-scope search

- 3 parallel `graph.search` calls per query: `scope=nodes/edges/episodes` with limits `(20/10/0)`.
- CE reranker over the union of all 3 scope results.
- Bi-temporal edges: `(fact, valid_at, invalidated_at)` per edge. Invalidated facts auto-filtered from current-state queries.
- Source: `zep/zep-eval-harness/zep_evaluate.py:180-281,284-313`.

## C. arxiv 2025-2026 — top 5 papers (filtered for portability)

> Filters applied: **rejected** any paper requiring GPU fine-tuning (no LoRA), proprietary LLMs, or only-toy benchmarks. **Required** concrete numbers on LoCoMo / LongMemEval / multi-hop QA benchmarks.

1. **Graphiti / Zep bi-temporal KG** — `arxiv.org/abs/2501.13956` (2025) — every node/edge has `valid_at` + `invalidated_at`; LLM extractor decides invalidation. **+18.5% on LongMemEval, 94.8% Deep Memory Retrieval.** Effort **M**: schema migration + extractor prompt update.

2. **Mem0g — graph-augmented production memory** — `arxiv.org/abs/2504.19413` (Apr 2025) — same paper as the public mem0 leaderboard claim; Mem0g adds +2% over base mem0. **+26% J over OpenAI memory across all 4 LoCoMo cats.** Effort **M**: write-path conflict resolver in `internal/handlers/add_*` calling LLM judge on overlapping subject+predicate.

3. **PRISM — agentic 3-agent multi-hop retrieval** — `arxiv.org/abs/2510.14278` (Oct 2025) — Question Analyzer → Selector → Adder loop. Beats baselines on HotpotQA / 2WikiMultiHopQA / MuSiQue / MultiHopRAG. Effort **S-M**: slots into our F2 Reflection-loop in M11; mostly prompt + budget control.

4. **HippoRAG 2 — Personalized PageRank** — `arxiv.org/abs/2502.14802` (ICML 2025) — PPR seeded by **query-entity embeddings** (not global PageRank like our M10). **+7% on associative-memory tasks.** Effort **S**: modify `internal/scheduler/pagerank.go` to accept query-seeded restart vector. **Drop-in upgrade.**

5. **RAPTOR — recursive tree-of-summaries** — `arxiv.org/abs/2401.18059` (ICLR 2024, seminal) — recursive cluster→summarize→embed at multiple abstraction levels. **+20% on QuALITY w/ GPT-4.** Effort **M**: D3 reorganizer extension; add k-means clustering loop until convergence.

**Skip list (cited so we don't reconsider):** MemoryBank (Ebbinghaus only — we have D1), Self-RAG (LoRA-style fine-tune), MemoRAG (KV-compression, requires open-weights LLM), Tree of Reviews (PRISM dominates), A-MEM (Zettelkasten — our SIMILAR_COSINE_HIGH already covers; no LoCoMo numbers), CraniMem (no numbers), TARGCN (pre-2024, GDELT-only, GNN training), LightRAG (subsumed by HippoRAG 2; UltraDomain not LoCoMo).

---

## D. New M11 streams (F8–F14)

> **Numbering:** F1–F7 from the original plan stay. F8–F14 are added by this amendment. F1 (VEC_COT) becomes **lower priority** because F8 (atomic facts) eliminates most of its value.

### F8 — Atomic per-fact extraction (port of `ADDITIVE_EXTRACTION_PROMPT`)

- **Branch:** `feat/f8-atomic-fact-extraction`
- **Owner:** Opus subagent
- **Depends on:** R0 + R1 merged.
- **Scope:**
  - `internal/llm/extractor.go` — replace single-paragraph extraction with the 944-line `ADDITIVE_EXTRACTION_PROMPT`. Output schema: `[]Fact{Text (15-80 words), AttributedTo (speaker), TopicTag, EventDates, LinkedMemoryIDs []uuid}`.
  - `migrations/0022_atomic_facts.sql` — `Memory.kind='atomic_fact'` value + `linked_memory_ids` JSONB array column with GIN index.
  - `internal/llm/profile_extractor.go` — atomic facts feed BOTH the dedup pipeline AND the profile extractor (instead of the current single condensed memory).
  - `cmd/reembed/` — extend to do a one-time **re-extraction** pass: for each cube, fetch raw blobs (or stored conversation text), re-extract under the new prompt, write atomic facts. Old `LongTermMemory` rows kept as `kind='paragraph_legacy'` for fallback.
  - Behind env flag `MEMDB_ATOMIC_FACTS=true` for staged rollout.
- **Acceptance:**
  1. Average **5-15 atomic facts per 10-message chunk** in test corpus (parity with mem0).
  2. Each fact passes self-contained test: 15-80 words, contains a proper noun reference, time anchor preserved.
  3. cat-2 hit@k full-corpus rises from 35% → ≥ **55%** (target +20pp on hit@k for cat-2).
  4. cat-2 LLM Judge full-corpus rises from 29% → ≥ **45%** (target +16pp).
  5. Headline excl-cat-5 rises ≥ +5pp.
  6. New metrics: `memdb.add.atomic_facts_extracted{outcome}`, histogram `memdb.add.facts_per_chunk` (buckets 1,3,5,10,15,20).
  7. Schema migration: 0% data loss (legacy rows kept under `kind='paragraph_legacy'`); reembed cost ≤ 60min per cube on 1986-QA corpus.
- **Risk:** **HIGH.** Largest behavioral change in M11. Worktree mandatory. Two-stage review must include data-shape validation (does the new extractor preserve EVERY fact previously captured?).
- **Lift:** **+5–7pp headline, +12–18pp cat-2.** Largest single stream.
- **Note:** if F8 lands cleanly, F1 (VEC_COT fanout) drops in priority — atomic facts already match sub-question fanout's intent at the extraction layer.

### F9 — Recall budget tuning (`top_k=30 per speaker`, drop similarity floor on cat-2)

- **Branch:** `feat/f9-recall-budget`
- **Owner:** Sonnet subagent
- **Depends on:** R2 merged.
- **Scope:**
  - `evaluation/locomo/query.py` — query each speaker independently with `top_k=30`, concat results with timestamp prefix `f"{ts}: {memory}"` per item, dedupe by id.
  - `internal/search/pipeline_query.go` — when query category heuristic detects cat-2 (multi-hop / temporal "When..." regex), drop `event_similarity_threshold` from 0.2 to 0.05 for that query only.
  - **Reverse-role ingest in eval harness** — `evaluation/locomo/ingest.py` ingests every conv twice with roles swapped (mem0 pattern, lifts mutual extraction).
- **Acceptance:**
  1. cat-2 hit@k full-corpus rises ≥ +5pp purely from recall budget change.
  2. Headline excl-cat-5 rises ≥ +1.5pp from the combined three changes.
  3. New metric `memdb.search.recall_budget{category,top_k}`.
- **Risk:** low. Eval-side and pipeline-side are both reversible by env flag.
- **Lift:** **+1.5–2pp headline, +4–7pp cat-2.**

### F10 — Reverse-role ingest

- **Branch:** `feat/f10-reverse-role-ingest`
- **Owner:** Sonnet subagent
- **Scope:** Folded into F9 (single subagent dispatch). Documented separately because it can also be enabled in production for some agent personas (companions, coaches) where mutual perspective is valuable.

### F11 — Bi-temporal edges (Graphiti / Zep, arxiv 2501.13956)

- **Branch:** `feat/f11-bi-temporal-edges`
- **Owner:** Opus subagent
- **Depends on:** R0 merged.
- **Scope:**
  - `migrations/0023_bitemporal_edges.sql` — add `valid_at TIMESTAMPTZ`, `invalidated_at TIMESTAMPTZ NULL` to `memos_graph.memory_edges` and `entity_edges`. Default `valid_at = created_at`, `invalidated_at = NULL`.
  - `internal/llm/edge_extractor.go` (NEW) — on every new fact, LLM judge call: "does this fact contradict or supersede any of these existing facts on subject X?" — if yes, set `invalidated_at = NOW` on losing edge AND add new edge.
  - `internal/search/pipeline_query.go` — query-time filter: by default exclude edges with `invalidated_at IS NOT NULL`; add `?include_invalidated=true` query param for historical analysis.
  - Background loop `internal/scheduler/bitemporal_validator.go` (uses R4 `periodicLoop`): every 30min re-evaluate stale edges (≥ 6 months old, never invalidated, low cosine to recent extracts).
- **Acceptance:**
  1. ≥ 30% of conflicting facts on the LoCoMo test corpus produce an `invalidated_at` write (validated by hand-spot-check).
  2. cat-4 LLM Judge full-corpus rises from 59.9% → ≥ **65%**.
  3. cat-2 LLM Judge full-corpus rises ≥ +3pp (temporal-flavored cat-2 Q benefit).
  4. New metrics: `memdb.add.edges_invalidated_total`, `memdb.search.edges_filtered_invalidated_total`.
- **Risk:** medium — schema change on hot table; backfill must be done in chunks.
- **Lift:** **+1.5pp headline, +3pp cat-4, +3pp cat-2.**

### F12 — `linked_memory_ids` at extraction time

- **Branch:** `feat/f12-linked-ids-extract`
- **Owner:** Opus subagent
- **Depends on:** F8 merged (F8's prompt schema already includes `LinkedMemoryIDs`).
- **Scope:**
  - `internal/llm/extractor.go` — populate `linked_memory_ids` field by running a follow-up LLM call after F8 produces atomic facts: "for each fact, pick up to 3 IDs from this candidate set that this fact relates to". Candidate set = top-20 cosine to the new fact from same cube.
  - `internal/db/postgres_memory.go` — write `linked_memory_ids` to `Memory.properties->'linked_memory_ids'`.
  - `internal/search/pipeline_multihop.go` — extend D2 expansion to include `linked_memory_ids` as a 1-hop edge (cheaper than AGE traversal; pre-computed at ingest).
- **Acceptance:**
  1. ≥ 60% of new facts ship with at least 1 `linked_memory_id`.
  2. cat-2 hit@k rises ≥ +3pp (compounding with F8 + F9 + F11).
  3. New metric `memdb.add.linked_ids_per_fact` histogram.
- **Risk:** medium — extra LLM call per fact = cost. Mitigate via `MEMDB_LINKED_IDS_BATCH=10` (bulk) and admission-control semaphore.
- **Lift:** **+0.5–1pp headline, +2–3pp cat-2.**

### F13 — `paragraph + triples` answer prompt (anti-mem0-Graph)

- **Branch:** `feat/f13-paragraph-triples-prompt`
- **Owner:** Sonnet subagent
- **Scope:** `internal/handlers/chat_prompt.go` — when D2 returns triples, render them as `"<triple><br>(source: [paragraph_excerpt])"` rather than bare triples. Each triple ALWAYS arrives with its originating paragraph excerpt (≤80 words) so generator never sees stripped narrative.
- **Acceptance:** ablation in M1 — cat-2 with vs without triples-only mode shows non-negative delta.
- **Lift:** +0.5–1pp; main value is **avoiding regression** observed in mem0-Graph.

### F14 — Personalized PageRank (HippoRAG 2, arxiv 2502.14802)

- **Branch:** `feat/f14-personalized-pagerank`
- **Owner:** Sonnet subagent
- **Depends on:** R4 merged.
- **Scope:**
  - `internal/scheduler/pagerank.go` — extend to compute **per-query** PPR seeded by entity-extraction of query terms. Cache result for 5min per (cube, query_entities_hash).
  - Replace global PageRank score in D1 boost with PPR score when query-entity match count > 0; fall back to global PR otherwise.
  - `internal/search/query_entities.go` (NEW) — small NER pass on query (LLM call or regex on capitalized tokens) → entity vector seed.
- **Acceptance:**
  1. PPR cache hit ratio ≥ 50% in steady state (most queries reuse seeds).
  2. cat-2 hit@k rises ≥ +3pp ablated isolation.
  3. New metric `memdb.scheduler.ppr_cache_hit_total{outcome}`.
- **Risk:** low (drop-in extension of existing PR code from M10).
- **Lift:** +1–2pp; arxiv claim is +7% on associative-memory tasks.

---

## E. Re-prioritized Phase 3 ranking

| Priority | Stream | Rationale |
|---|---|---|
| **P0 (must)** | **F8 atomic per-fact extraction** | Largest single lever (+5-7pp headline, +12-18pp cat-2). Unlocks F12. |
| **P0 (must)** | F9 recall budget + reverse-role | Cheap S, +1.5-2pp headline. |
| **P0 (must)** | F11 bi-temporal edges | arxiv ICLR-tier evidence (+18.5% LongMemEval). cat-4 + cat-2. |
| **P1 (should)** | F12 linked_memory_ids | Compounds with F8; reduces D2 cost. |
| **P1 (should)** | F14 Personalized PageRank | Drop-in upgrade to M10 PR; HippoRAG 2 evidence. |
| **P1 (should)** | F3 event extraction (Memobase) | Independent of F8; second-largest structural gap. |
| **P1 (should)** | F2 Reflection-loop | Compounds with F8 atomic facts at retrieval. |
| **P1 (should)** | F7 temporal-index | Direct cat-2 lever; partially subsumed by F11 but extract-time signal differs. |
| **P2 (may)** | F1 VEC_COT fanout | **Demoted from P0** — F8 atomic facts subsume most of its value. Keep for cases where atomic extraction misses sub-aspects of a query. |
| **P2 (may)** | F4 buffer flush | Cost optimization, not headline lever. |
| **P2 (may)** | F6 RECREATE-ENHANCE | Polish. |
| **P2 (may)** | F13 paragraph+triples prompt | Regression prevention; ship if D2-vs-no-D2 ablation reveals the issue. |
| **P2 (may)** | F5 dialogue-pair rerank | Polish, +0.5pp. |
| **DEFER M12** | RAPTOR recursive tree | Big change, M-effort, conflicts with F8 staging. |
| **DEFER M12** | PRISM Selector/Adder | Conflicts with F2 reflection-loop scope; ship reflection first, evaluate. |
| **DEFER M12** | Mem0g write-path conflict resolver | Subsumed by F11 invalidation logic. Re-evaluate after F11 ships. |

---

## F. Updated headline expectation

| Layer | Lift (excl-cat-5) | Source |
|---|---|---|
| Q1 timeout retry | +3.0 pp | infra fix |
| Q2 LoCoMo taxonomy | +0.7 pp | M10 wrong-taxonomy correction |
| Q3+Q4 rerank polish | +0.6 pp | MemOS pattern |
| F8 atomic facts | **+5.5 pp** | mem0 cat-2 paradox resolution |
| F9 recall budget | +1.5 pp | mem0 eval harness parity |
| F11 bi-temporal | +1.5 pp | Zep / Graphiti, ICLR evidence |
| F12 linked ids | +0.7 pp | compounding with F8 |
| F14 Personalized PR | +1.2 pp | HippoRAG 2 |
| F3 event extraction | +1.5 pp | Memobase pattern |
| F7 temporal-index | +0.7 pp | cat-2-specific (partial overlap with F11) |
| **Subtotal P0 + P1** | **+15.9 pp** | (raw sum, will compound-discount) |
| **Compound discount** | -7 to -8 pp | features overlap (especially F8 ↔ F12 ↔ F1 ↔ VEC_COT path) |
| **Net realistic delta** | **+7-9 pp** | from 72.5% → **79.5-81.5%** |

**Updated headline target:** **≥ 78.0% LLM Judge excl-cat-5 chat-50 stratified**, putting MemDB clearly above Memobase 75.78% and Zep 75.14% — establishing leaderboard leadership.

**Stretch:** 80.0% if F8 lands without compound discount.

**Floor:** 76.0% (original plan target) if F8 doesn't ship cleanly.

---

## G. Updated risk register (deltas only)

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **F8 schema migration breaks prod cubes** | medium | high | `MEMDB_ATOMIC_FACTS=false` default in prod; canary on staging cube; legacy rows preserved as `kind='paragraph_legacy'`; rollback plan = flip env flag |
| **F8 re-extraction cost** | high | medium | Re-ingest one cube takes ≤ 60min on 1986-QA corpus (M10 measurement); 10 cubes = 10h serial → run on staging only; prod cubes opt-in via API |
| **D2 multi-hop hurts cat-2 (mem0-Graph regression replicated)** | medium | medium | M1 ablation: cat-2 with D2 OFF — if delta > +1pp, default `MEMDB_DISABLE_D2_FOR_CAT2_HEURISTIC=true`; F13 paragraph+triples is the structural fix |
| **F11 bi-temporal extractor over-invalidates** | medium | medium | LLM judge prompt requires confidence ≥ 0.7 to invalidate; extensive prompt testing pre-merge; `invalidated_at` is reversible |
| **F12 follow-up LLM call doubles ingest cost** | high | medium | Bounded batch + admission-control semaphore; if cost > 1.8× of baseline, drop F12 to P2 |
| **Compound discount worse than -8pp** | medium | low | F8 alone clears Memobase; F8 + F11 alone clears Zep; floor 76% holds even with severe overlap |

---

## H. Updated ablation matrix (M1 stream)

Original plan ran 11 ablation runs. Amendment expands to **15** runs covering the new streams + the **D2-OFF on cat-2** test that mem0-Graph evidence demands.

| Run | Config | Purpose |
|---|---|---|
| 1 | full M11 stack | headline measure |
| 2-12 | full minus {Q1,Q2,Q3+Q4,F1,F2,F3,F7,F8,F9,F11,F14} | per-stream attribution |
| **13** | **full minus D2 (cat-2 only)** | **mem0-Graph anti-pattern test** |
| **14** | **full minus F8** | **largest single-stream isolation** |
| **15** | **full minus (F8 ∪ F12)** | **atomic-facts pipeline isolation** |

Compute budget update: 15 × 10h = 150h on `--workers 4` parallel = ~38h wall. Acceptable; one extra day of M1 phase.

---

## I. Updated cadence

The original plan budgets 14 days. F8 alone adds ~3 days due to schema migration + re-extraction infrastructure + extensive validation. **New total: 17 days.**

| Day | Phase | Notes |
|---|---|---|
| 1-4 | Phase 1 refactors | unchanged |
| 5-7 | Phase 2 quick wins | unchanged |
| 8-11 | **Phase 3 part A: F8 + F9 + F11 + F14** | atomic facts, recall budget, bi-temporal, PPR — the **P0/P1 musts** |
| 12-14 | Phase 3 part B: F12 + F3 + F2 + F7 | layered on top of F8 |
| 15 | Phase 3 part C (cherry-pick from P2) | F1, F4, F5, F6, F13 — only what schedule allows |
| 16 | M1 ablation matrix runs (38h wall, overnight) | |
| 17 | M2 release v0.24.0 | |

---

## J. References

- mem0 deep-dive (subagent report 2026-04-27, in conversation context).
- arxiv research (subagent report 2026-04-27, in conversation context).
- Original M11 plan: `2026-04-27-m11-locomo-leadership.md` (this amendment supersedes its priority ordering and headline target; refactor + Phase 1/2 specs unchanged).
- Cited papers: `arxiv.org/abs/2501.13956` (Graphiti), `arxiv.org/abs/2504.19413` (Mem0g), `arxiv.org/abs/2510.14278` (PRISM), `arxiv.org/abs/2502.14802` (HippoRAG 2), `arxiv.org/abs/2401.18059` (RAPTOR).
- mem0 source: `/home/krolik/src/compete-research/mem0`.
- Zep source: `/home/krolik/src/compete-research/zep`.
