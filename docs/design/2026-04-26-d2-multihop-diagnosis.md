# D2 Multi-Hop Diagnosis & Fix — 2026-04-26

**Author:** M8 Stream 3 GRAPH controller
**Status:** Shipped (PR `feat/d2-multihop-fix`)
**Scope:** Diagnose why M7 Stage 2 cat-2 (multi-hop) F1 = 0.091 stayed flat
despite `MEMDB_SEARCH_MULTIHOP=true`, `D2_MAX_HOP=3`, `D3_MIN_CLUSTER_RAW=2`,
and 1345 valid `memory_edges` rows in the DB.

## TL;DR

D2 expansion was firing on every query but its candidates **never survived
`TrimSlice(text, p.TopK)`**. Reason: expansion items inherited a *RRF-derived*
score (~0.013) and had no embedding, while seeds were re-scored to *cosine*
(~0.5–1.0) by `ReRankByCosine`. After sort + trim, all expansions fell below
the cut. D2 was, in effect, dead code on prod traffic.

Fix: return the neighbour's pgvector embedding from `MultiHopEdgeExpansion`,
score expansions as `cosine(query, neighbour) × decay^hop` so they compete on
the same scale as seeds. Embeddings are not exposed to `ReRankByCosine` (we
don't want it to overwrite our hop-decayed score with plain cosine).

## Diagnosis evidence (against live prod, 2026-04-24)

### 1. Edges exist and are correctly scoped

```
SELECT relation, COUNT(*) FROM memos_graph.memory_edges WHERE invalid_at IS NULL GROUP BY relation;
   relation       | count
 -----------------+-------
 CONSOLIDATED_INTO|   850
 MERGED_INTO      |   273
 MENTIONS_ENTITY  |   205
 CONTRADICTS      |    15
```

`conv-26__speaker_a` alone has 346 valid edges (291 CONSOLIDATED_INTO +
55 MERGED_INTO). Hypothesis-1 ("D3 not running") rejected.

### 2. Topology is hub-and-spoke

Per-memory degree histogram for `conv-26__speaker_a` (343 activated nodes):

```
 deg | count
-----+-------
 291 |     1   ← single central consolidator
   6 |     4
   3 |     8
   2 |    13
   1 |   264   ← 77% of nodes have exactly one edge
   0 |    51   ← 15% are isolated
```

Vector top-20 returns the *raw text leaves* (memory text matching question
verbatim). 14/20 of those have degree 1 (pointing at the consolidator); 6/20
have degree 0. No dense clusters at hop-1.

### 3. Expansion query *does* return rows

Replicating the exact CTE from `queries_search_graph.go::MultiHopEdgeExpansion`
against the 20 vector-top IDs of a real cat-2 query:

```
hop | distinct_nodes
----+----------------
  0 |             20   ← seeds
  1 |              1   ← the central consolidator
  2 |            291   ← consolidator's other 290 children
```

Expansion returns 1 hop-1 + 290 hop-2 nodes, capped at 40 (2× origSize). So
*technically* it injects ~20 new candidates — hypothesis-2 ("filter excludes")
also rejected.

### 4. Score scale mismatch — the actual root cause

`expandViaGraph` (pre-fix) computed:

```go
score := parent * math.Pow(multihopDecay(), float64(hop))
```

`parent` is the seed's RRF score (`1/(rank+60) ≈ 0.016`). So a hop-2
expansion item gets `0.016 × 0.64 = 0.010`.

Downstream pipeline:

1. `FormatMergedItems` writes `meta["relativity"] = m.Score` for every item
   (seeds and expansions alike).
2. `ReRankByCosine` overwrites `meta["relativity"]` with
   `(cosineSim(q, emb) + 1) / 2 ∈ [0, 1]` — **only for items in
   `embeddingsByID`**. Expansion items had `Embedding: nil` (line 121-123),
   so `ReRankByCosine` skipped them.
3. After rerank: seeds carry cosine scores [0.5, 1.0]; expansions carry
   ~0.01.
4. Sort by relativity desc → expansions all sink to the bottom.
5. `TrimSlice(text, p.TopK)` (TopK=20) keeps only the top 20 → all seeds, no
   expansions. **D2 has zero observable effect.**

`ce_rerank` would have re-scored everything together, but is gated on
`s.RerankClient.Available()` and only fires sometimes (the prod logs show
`ce_rerank:0` on most queries with the M8 sample). Therefore the bug is
load-bearing on most traffic.

## Fix

Three coupled changes, kept tiny:

### `memdb-go/internal/db/queries/queries_search_graph.go`

Add `m.embedding::text AS embedding` as a 5th SELECT column in
`MultiHopEdgeExpansion`.

### `memdb-go/internal/db/postgres_graph_recall.go`

Add `Embedding []float32` field to `GraphExpansion`; parse the 5th column
through `ParseVectorString`.

### `memdb-go/internal/search/service_multihop.go`

Take a new `queryVec []float32` parameter. For each expansion:

```go
if len(queryVec) > 0 && len(e.Embedding) > 0 {
    cosNorm := (float64(CosineSimilarity(queryVec, e.Embedding)) + 1.0) / 2.0
    score = cosNorm * decay
} else {
    score = parent * decay   // legacy fallback
}
```

Critically, `Embedding` is left `nil` on the resulting `MergedResult` so
`ReRankByCosine` does **not** overwrite our hop-decayed score with plain
cosine — that would defeat the decay.

Also added structured `slog.Debug` lines per expansion call (`seed_count`,
`expansion_count`, `max_hop`, `scored_by_cosine`, `scored_by_decay_only`,
`pool_after_cap`) for ops triage.

### `memdb-go/internal/search/metrics.go`

New OTel histogram `memdb.search.d2_hops_per_query` with bucket boundaries
[0, 1, 2, 3, 4, 5]. Recorded on every call (0 when no expansion happened).
Existing `memdb.search.multihop` counter is unchanged.

(The originally-planned `d2_edge_types_seen` counter was descoped — the SQL
aggregates relations away inside the recursive CTE, and surfacing it would
require a separate query per call. Edge-type breakdown is already cheap to
get from `SELECT relation, COUNT(*) FROM memory_edges` ad-hoc.)

## Verification

### Unit tests (added)

- `multihop_cosine_test.go::TestExpandViaGraph_CosineScoring_QueryAlignedWins`
  — query-aligned hop-1 neighbour (cos=1, decay 0.8 → score 0.8) beats
  orthogonal hop-1 neighbour (cos=0, normed 0.5, decay 0.8 → score 0.4).
- `…HopDecayApplied` — same neighbour at hop-1 vs hop-2 → 0.8 vs 0.64.
- `…FallsBackWhenEmbeddingMissing` — `parent × decay` survives when
  `e.Embedding` is empty (regression guard).

### livepg integration test (added)

- `multihop_livepg_test.go::TestLivePG_ExpandViaGraph_TwoHop` — inserts 3
  Memory rows + 2 edges A→B→C, queries with embedding aligned to C, asserts
  C (hop-2, query-aligned) outranks B (hop-1, orthogonal) and both beat the
  cold seed.

Run:

```bash
MEMDB_LIVE_PG_DSN="postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable" \
GOWORK=off go test -tags=livepg ./memdb-go/internal/search/... \
    -run TestLivePG_ExpandViaGraph_TwoHop -count=1 -v
```

Result (verified): `D2 fix verified: seed=0.0100 B(hop1,orth)=0.4000 C(hop2,aligned)=0.6400`.

## Follow-up: v2 fix (independent cap, 2026-04-26)

Live re-measurement on conv-26 cat-2 (37 QAs) after the v1 fix uncovered a
**second bug introduced by the v1 score change**: cosine-rescored expansions
got Score in `[0.4, 0.8]`, but seeds at the `expandViaGraph` exit point still
carry RRF Score (~0.016) because `ReRankByCosine` runs DOWNSTREAM. The
joint sort + 2× cap then pushed every seed to the bottom and evicted
those that didn't fit, including the gold leaf for q33
("How long has Caroline had her current group of friends for? — 4 years").

Symptom: M7 stage 2 cat-2 hit@k = 0.43 → M8 v1 dropped to 0.27, F1
collapsed to 0.0 (every chat answered "no answer" because the
gold-bearing leaves were evicted from the top-20).

v2 fix (single function, ~20 lines in `service_multihop.go`):

- Cap expansions independently: keep ALL seeds, trim only expansions to
  `(2 × origSize − origSize)` slots.
- Seeds always survive to `ReRankByCosine`, which rescues their score.
- Expansions still compete for the remaining budget by cosine × decay.
- `multihop_cosine_test.go` updated to exercise 5 seeds + 2 expansions
  (cap=10, expBudget=5) so both expansions fit; new assertion verifies
  every seed is preserved.

## Topology caveat — what this fix does NOT solve

The hub-and-spoke topology means hop-1 produces just one neighbour (the
consolidator) for most queries. The 290 hop-2 leaves are *siblings under the
same consolidator* — semantically they may or may not relate to the query.
Cosine scoring filters out the irrelevant siblings, but recall is still
gated by what D3 chose to consolidate.

For a denser graph (CAUSES / SUPPORTS / CONTRADICTS edges from D3 relation
detector), cat-2 lift will compound. As of 2026-04-24 conv-26 has 0 of
those — D3 relation detector wasn't producing inter-cluster links on the
M7 corpus. That's a separate workstream (S6 or S7).

## Out of scope (deliberately)

- D3 reorganizer changes (separate concern).
- AGE schema changes.
- BFS / different traversal algorithm.
- Anything for cat-1/3/4/5 (those are Stream 6 + Stream 7).
- Refactoring multihop code beyond the targeted fix.
