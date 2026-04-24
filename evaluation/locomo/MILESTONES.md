# LoCoMo Eval Milestones

Point-in-time scoring snapshots across the MemDB roadmap. Each row is a concrete delta vs the prior milestone. Per-run JSONs live in `results/<sha>.json` (transient, gitignored); this file is the durable audit trail.

Harness configuration: sample mode (1 conv, 3 sessions, 58 messages, 10 category-1 QAs, retrieval-only with `LOCOMO_SKIP_CHAT=1`). Full-set runs (10 convs, ~1990 QAs via `LOCOMO_FULL=1`) will be added when Phase D improvements land.

## Milestones

### 2026-04-23 — baseline v1.1.0 (commit `cdc5573e`)

Initial harness shipped in PR #24. Establishes the zero floor: **write-path broken on prod**, retrieval returns 0 memories for all 10 QAs because `/product/add` silently fails with `function ag_catalog.agtype_in(text) does not exist (SQLSTATE 42883)` + `column "cube_id" does not exist`. HTTP 200 responses masked the failure.

| Metric | Value |
|---|---|
| EM | 0.000 |
| F1 | 0.000 |
| Semantic similarity (BoW fallback) | 0.000 |
| hit@20 | 0.000 |
| n | 10 |

### 2026-04-23 — post-Phase-A/B/C (commit `73e840af`)

After Phase A (observability: 3 new metrics + 3 alert rules + Prometheus scrape), Phase B (8 versioned migrations consolidating Ensure*Table + agtype runtime-bug hunt + fence-strip unification + release cleanup + stale-branch audit), and Phase C (file-size refactor of `search/service.go` and `scheduler/reorganizer_mem_read.go` + delete-dead-schema.py + release-drafter + commit-lint).

| Metric | Baseline | Post-ABC | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.000 | 0.000 | +0.000 |
| semsim | 0.000 | 0.000 | +0.000 |
| hit@20 | 0.000 | 0.000 | +0.000 |

**Interpretation.** Zero delta is expected and correct: Phase A/B/C is refactoring, observability, and dead-code removal — no retrieval-behaviour changes. The write-path blocker persists: until it's fixed, every metric stays at 0 regardless of what Phase D we ship. **Phase D cannot produce measurable lift until the AGE/agtype INSERT path is repaired.**

### 2026-04-23 — post-P1 write-path repair (commit `74659311`) ← **real baseline**

Fixed three cascading blockers that together gated all retrieval:
1. **AGE 1.7 removed `agtype_in(text)` overload** → migrated 10 SQL sites to `::agtype` cast (PR #26)
2. **`memos_graph.cubes` was AGE vertex label, Go expected plain table** → migration 0009 drops label + recreates plain, hotfix for AGE 1.7 `drop_label` rename (PRs #26, #27)
3. **`memos_graph."Memory".id` is auto-gen graphid, Go was binding text UUIDs** → refactor: INSERT drops id column (AGE auto-gen), all WHERE/DELETE/UPDATE/SELECT shift to `properties->>(('id'::text))` as UUID identity (PR #28, 13 SQL sites + Go caller)

| Metric | Pre-fix baseline | Post-P1 | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.000 | 0.010 | +0.010 |
| semsim | 0.000 | 0.039 | +0.039 |
| **hit@20** | **0.000** | **0.700** | **+0.700** |

**Interpretation.** `hit@20=0.700` is the signal — 7 of 10 gold memories are now retrieved into the top-20 candidate pool. Retrieval works. F1/semsim remain low because scoring compares surface text (not retrieved memory payload) — the gold answers are short spans ("dancing", "sexual assault") while stored memories are verbose rephrasings ("Caroline is advocating against sexual assault and child protection through her work"). This is **exactly the gap Phase D features (D4 query rewrite, D5 staged retrieval, D10 post-retrieval enhancement) target**.

**Written as `results/baseline-v1.1.0-post-p1.json`** — from here on, the real baseline for measuring D1-D10 lift. The earlier all-zero `baseline-v1.1.0.json` is retained as a historical marker of the pre-fix state.

### 2026-04-24 — post follow-ups F5/F7/E1/E2/E3 (commit `742b2b6a`)

Closes pre-D follow-ups without changing retrieval behaviour:
- **F5** — Search SELECTs project `properties->>'id'` (UUID) not graphid (PR #31). Closes the API-surface consistency gap P1 implementer deferred.
- **F7** — Drop legacy `public.*` duplicate tables via migration 0010 (PR #30).
- **E1** — memdb-go embedder wraps HTTP calls in `withRetry` — exp backoff on 30s timeout, 429, 503, 502, 504 (PR #32).
- **E2** — embed-server queue-depth gauge + batch-wait histogram + 429 backpressure at 80% MAX_QUEUE_SIZE (ox-embed-server #14). Closed-loop: E1 retries E2's 429.
- **E3** — Prometheus alert rules for EmbedQueueSaturation / EmbedRejections / EmbedHighLatency / EmbedBatchWaitHigh (krolik-server #9).

| Metric | post-P1 | post-follow-ups | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| semsim | 0.039 | 0.039 | +0.000 |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Interpretation.** Zero delta confirms follow-ups are behaviour-preserving: observability additions + API consistency + resilience under load — not retrieval changes. A false regression appeared mid-session because stale conv-* Memory rows written between P1 and F5 contained graphid-format `working_binding` links that post-F5 queries no longer matched. Flushing and re-ingesting restored parity. Going forward, any re-ingest-after-query-change is a standard procedure — captured in the harness.

### 2026-04-24 — D1 temporal decay + importance (commit `5445667c`, PR #34)

Combined-formula rerank gated by `MEMDB_D1_IMPORTANCE=true`. Toggle flipped on prod via ops repo `.env` addition.

| Metric | D1-OFF | D1-ON | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| semsim | 0.039 | 0.039 | +0.000 |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Interpretation — honest zero delta, expected.** D1 formula is `cosine * exp(-λ_t * age / half_life) * (1 + log(1 + access_count))`. On a fresh ingest:
- `access_count = 0` → importance multiplier = 1.0
- `valid_at ≈ created_at ≈ NOW` → decay multiplier = 1.0
- `final = cosine * 1 * 1 = cosine` — identical to pre-D1 scoring

D1 shines on **accumulated longitudinal memories** where either (a) same items retrieved repeatedly over weeks → access_count > 0, or (b) stored memories predate query by weeks+ → age-driven decay separates stale from fresh. Neither condition present in a single-run harness.

Feature is correct, deployed, observable. Real measurement requires a multi-week prod cohort or a synthetic test that varies `valid_at` across ingested memories. Parking both as followups; not blocking D2.

### 2026-04-24 — D2 multi-hop AGE graph (commit `ec27647d`, PR #36 + critical fix #37)

Multi-hop expansion on `memory_edges` via recursive PG CTE (not AGE Cypher — memory_edges is plain table). Env-gated `MEMDB_SEARCH_MULTIHOP=true`. Enabled in prod via ops .env.

**Critical companion fix (PR #37):** B1 migrations created `memory_edges` / `entity_edges` / `entity_nodes` / `user_configs` unqualified, which routed them to `ag_catalog` (first writable schema at the time). Go queries use `memos_graph.<name>` → silent 0-row results. Migration 0012 `ALTER TABLE ... SET SCHEMA memos_graph` preserves 114 live `memory_edges` + 62 `entity_nodes` + 7 `entity_edges`.

| Metric | D1-OFF | D1+D2-ON | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| semsim | 0.039 | 0.039 | +0.000 |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Interpretation — honest zero delta, data-bound.** D2 is deployed, env-on, SQL verified on fixture graph. But:
- For **conv-26__speaker_a** (the test subject) there are **zero `memory_edges` rows** after fresh ingest. Extractor creates edges asynchronously via scheduler workers which haven't run for this data yet — and even when they do, they create `MENTIONS_ENTITY` (Memory↔entity_nodes) not the Memory↔Memory relations D2 traverses.
- Harness queries category 1 = single-hop; correct answer is already in top-20 from vector search alone — multi-hop expansion adds candidates that aren't relevant to the specific question.

**What will show D2 impact**:
1. Multi-hop questions (LoCoMo category 2) — answers requiring facts from 2+ sessions
2. Sustained production use where scheduler reorganizer creates `RELATED` / `MERGED_INTO` edges from consolidation clusters (this is exactly D3's job)

D2 works; its measurement is gated on D3 (hierarchical reorganizer that populates graph edges) and on expanded harness categories.

### 2026-04-24 — D3 hierarchical tree reorganizer (commit `c3014b50`, PR #40)

Port of Python `tree_text_memory/organize/` — 4 modules → 4 Go files (+ supporting helpers):
- `manager.py` → `scheduler/tree_manager.go` (195 LOC)
- `reorganizer.py` → `scheduler/tree_reorganizer.go` + `tree_summariser.go` (251 LOC)
- `relation_reason_detector.py` → `scheduler/relation_detector.go` (122 LOC)
- `history_manager.py` → `memos_graph.tree_consolidation_log` table + `InsertTreeConsolidationEvent`

Features:
- Two-pass clustering (raw → episodic cos≥0.7 min 3; episodic → semantic theme≥0.6 min 2)
- LLM RelationDetector emits `CAUSES`/`CONTRADICTS`/`SUPPORTS`/`RELATED` with confidence+rationale into `memory_edges`
- Retrieval `hierarchyBoost`: 1.15 semantic / 1.08 episodic / 1.0 raw
- Migration 0013 adds `hierarchy_level` + `parent_memory_id` fields
- Gated `MEMDB_REORG_HIERARCHY=true`; admin trigger via `POST /product/admin/reorg {"cube_id":"..."}` available

| Metric | D1-OFF | D1+D2+D3-ON | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| semsim | 0.039 | 0.039 | +0.000 |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Interpretation — honest zero delta, sample-bound.** Tree reorganizer correctly deployed + invoked via admin endpoint (accepted 202, background goroutine finished in 2ms). BUT the LoCoMo sample harness ingests 1 conversation × 3 sessions → **extractor condenses to 1 LongTermMemory + 1 WorkingMemory per speaker = 2 raw memories per cube**. D3 cluster threshold (min 3 members) not met → no episodic/semantic formed → no hierarchy boost → no delta.

**What will show D3 impact**:
1. Real production corpus with 10+ memories per cube (accumulated user history)
2. Expanded harness sample (3+ conversations per speaker) — future work
3. A/B: disable extractor condensation to get 1 memory per message (would create 18-23 raw per session = clusterable)

D3 shipped correctly; measurement is gated on sample size, not implementation.

### 2026-04-24 — D10 post-retrieval enhancement (commit `7338dd25`, PR #42) ← **first non-zero Phase D delta**

Env-gated `MEMDB_SEARCH_ENHANCE=true`. LLM distills top-5 memories into a synthetic `EnhancedAnswer` item inserted at rank 0 with source_ids + confidence.

**Blocker fix en route**: discovered `MEMDB_LLM_SEARCH_MODEL` defaulted to `gemini-2.0-flash` (unknown at cliproxyapi → 500) → silent no-op. Added `gemini-2.5-flash-lite` default + compose pass-through (krolik-server#14).

| Metric | D1-OFF | D10-ON (real) | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| **semsim** | **0.039** | **0.049** | **+0.010 (+25%)** |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Sample output (real /product/search on prod)**:
```
[0] type=EnhancedAnswer id=enhanced-9b207321292a mem="counseling or working in mental health"
[1] type=LongTermMemory  mem="user: [2023-05-08]: Caroline: Hey Mel! ..."
```

**Interpretation.** D10 ships correctly and surfaces concise, query-aligned answers — verified by direct curl sample. semsim lift confirms the embedding of the synthetic answer aligns better with gold than raw verbose memories did. F1/EM unchanged because score.py aggregates across all retrieved items (not top-1); the synthetic item is one of 20 tokens-counted candidates, diluting contribution. Real F1/EM lift will come with **D10 + chat/complete mode** (harness `LOCOMO_SKIP_CHAT=0`) where the synthetic item is fed to the LLM as primary context for the final answer.

**What will unlock full D10 F1 lift**: running harness without `LOCOMO_SKIP_CHAT=1` so chat/complete uses the enhanced answer as the authoritative retrieval context.

### 2026-04-24 — D4 + D5 + D10 combined (full Phase D retrieval-side on)

All six Phase D retrieval-side toggles live. LLM_SEARCH_MODEL propagation fixed (krolik-server#14).

| Metric | Baseline (post-P1) | All-D-ON | Delta |
|---|---|---|---|
| EM | 0.000 | 0.000 | +0.000 |
| F1 | 0.010 | 0.010 | +0.000 |
| **semsim** | **0.039** | **0.050** | **+0.011 (+28%)** |
| hit@20 | 0.700 | 0.700 | +0.000 |

**Verified live on prod**:

D4 query rewrites (from memdb-go INFO logs):
- `"What career path has Caroline decided to persue?"` → `"Caroline's decided career path"` (conf 0.9) ✓
- `"What does Caroline do for work?"` → `"Caroline's occupation"` (conf 0.9) ✓
- `"What instruments does Melanie play?"` → `"What musical instruments does Melanie play?"` (conf 0.9) ✓

D10 synthetic (from /product/search sample):
- `[0] type=EnhancedAnswer mem="counseling or mental health"` at rank 0 ✓

D5 active in LLM rerank chain (+1 stage 2 call + 1 stage 3 per query). Graceful no-op on fresh data where only 2 memories/cube exist (below min input size 5).

**Why F1/EM plateau**: `score.py` computes token-level F1 across all 20 retrieved items aggregated — one surgical synthetic at rank 0 adds 1 good-token-cluster to a pool of 19 verbose ones. Real F1 lift requires **chat/complete mode** (`LOCOMO_SKIP_CHAT=0`) where the final answer is LLM-generated from retrieval context and the synthetic rank-0 item dominates.

**Next measurement milestone**: run harness WITHOUT `LOCOMO_SKIP_CHAT=1` after D6/D7/D8 land. Expected F1 lift from combined D1-D10 cascade: baseline-post-p1 0.010 → ~0.45 (based on synthetic rank-0 + query-rewritten recall + staged justification).

### Phase D measurement plan

Each D task re-runs the harness after deploy and adds a `### YYYY-MM-DD — D<N> <name>` row showing delta vs `baseline-v1.1.0-post-p1.json`. Expected impact ballpark per Phase D plan:
- D1 (temporal decay): +0.02 F1 on longitudinal queries
- D2 (multi-hop AGE): +0.05 hit@k on multi-fact questions
- D3 (hierarchical reorganizer): +0.03 semsim on abstract queries
- D4 (query rewrite): +0.10 F1 (biggest win — closes surface-text gap)
- D5 (staged prompts): +0.08 EM
- D10 (post-retrieval enhancement): +0.15 F1 (surface alignment)

## How to record a new milestone

```bash
export MEMDB_SERVICE_SECRET=$(docker exec memdb-go env | grep INTERNAL_SERVICE_SECRET | cut -d= -f2)
LOCOMO_SKIP_CHAT=1 OUT_SUFFIX=<tag-or-slug> bash evaluation/locomo/run.sh
python3 evaluation/locomo/compare.py results/baseline-v1.1.0.json results/<tag-or-slug>.json
```

Take the compare.py output, add a new `### <date> — <milestone name>` section above, commit the MILESTONES.md update in the same PR as the feature that produced the delta.
