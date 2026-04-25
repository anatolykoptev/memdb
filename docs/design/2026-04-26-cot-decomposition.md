# D11 CoT Query Decomposition for Multi-Hop and Temporal Questions

**Status:** implemented (default OFF)
**Stream:** M8 Stream 4 — COT
**Sprint:** 2026-04-26
**Files:** `memdb-go/internal/search/cot_decomposer.go`,
          `memdb-go/internal/search/cot_decomposer_cache.go`,
          `memdb-go/internal/search/service_cot_d11.go`,
          `memdb-go/internal/search/metrics.go`,
          `memdb-go/internal/config/config.go`

## Problem

LoCoMo benchmark surfaces two MemDB weaknesses:
- cat-2 (multi-hop) F1 = 0.091
- cat-3 (temporal) F1 = 0.201

Single-vector retrieval cannot bridge two-hop entity chains
("What did Caroline do in Boston after she met Emma?") — the embedding
encodes the surface form of the whole question, but the relevant memories
sit at the intersection of two facts that no single vector can recall.

## Design

D11 sits between D7 (generic conjunction-splitting CoT) and D2 (graph
multi-hop expansion). It runs **before** D2's `expandViaGraph` so the graph
walk sees an enlarged seed set built from each sub-query's vector hits.

```
query → [D7 augment] → [D11 decompose+fanout] → merge → [D2 graph walk] → ...
```

### Why a sibling of D7 instead of replacing it

D7 decomposes "X and Y" → ["X", "Y"] with a permissive heuristic
(any 8-word query). D11 targets a different failure mode (temporal +
multi-hop) with a stricter gate. Keeping them independent lets us:
- ablate cleanly (`MEMDB_SEARCH_COT=true MEMDB_COT_DECOMPOSE=false` etc.)
- ship them on different schedules
- avoid one improvement masking the other when re-measuring LoCoMo per cat

### Heuristic gate

To keep the LLM round-trip off the hot path for short / atomic queries,
D11 only invokes the LLM when ALL three signals are present:

1. `len(strings.Fields(query)) > 8`
2. Contains a temporal/causal connector (`and after before since while when until then because so`)
3. Contains ≥2 capitalized name-like tokens (proper nouns, ignoring
   the sentence-initial position)

Negative example: "what did Caroline do in the city after she finished
the work today" — only one entity, gate skips.
Positive example: "What did Caroline do in Boston after she met Emma at
the conference?" — three entities + connector + length, gate fires.

### Prompt design

```
You are decomposing a question to retrieve memories piece-by-piece.

Question: <query>

Output a JSON array of 1-3 simpler sub-questions that together cover the
original. Rules:
- Each sub-question should be answerable independently
- Preserve named entities verbatim
- For temporal questions, separate the "when" and the "what"
- For multi-hop, separate each hop into its own sub-question
- If the question is already simple, return [<original>]

Output ONLY the JSON array, no prose. Example:
["When did Caroline meet Emma?", "What did Caroline do in Boston?"]
```

Why a flat JSON array (not a structured object with `is_complex` like MemOS):
- Less for the LLM to get wrong; ~50% fewer parse failures observed in dev.
- The "atomic" case is encoded as a single-element array — the same code
  path handles both branches.

### Cache strategy

In-process `sync.Mutex`-guarded `map[string]cotEntry`, TTL 5 min. Key is
`sha256(strings.ToLower(strings.TrimSpace(query)))`. Mirrors the
`iterativeCache` pattern but kept in a separate type so the two caches
can't accidentally share keys.

5 minutes is a deliberate choice between:
- 2 min (`iterativeCache` TTL — chosen for staged retrieval where the
  memory pool changes within a session)
- 1 hr+ (LLM rerank TTL — works because rerank scores are stable per
  fixed candidate set)

For D11, the input is the user query alone (no candidate set), and
queries within a session repeat often enough (chat threads) that 5 min
catches the value without keeping stale decompositions around.

### Fanout to D2

For each sub-query at index ≥1 (index 0 is the original, already
processed by the primary path), D11 runs an extra text-scope
`VectorSearch` and unions the results into `psr.textVec` by id
(max-score). The downstream `expandViaGraph` step then walks edges from
this enlarged seed set.

Skill / tool scopes are intentionally NOT augmented here — D11 targets
the text-scope multi-hop weakness; skill/tool augmentation is already
covered by D7's `augmentWithSubqueries` when `MEMDB_SEARCH_COT=true`.

### Failure semantics

D11 is best-effort. Any of:
- LLM HTTP error
- LLM timeout (default 2s, capped at 10s)
- JSON parse failure
- empty result
- decomposer disabled by config

→ silently fall back to `[]string{originalQuery}`. The pipeline runs
identically to the no-CoT path.

## Configuration

| Env var | Default | Range | Purpose |
|---|---|---|---|
| `MEMDB_COT_DECOMPOSE` | `false` | bool | master switch |
| `MEMDB_COT_MAX_SUBQUERIES` | `3` | clamp `[1, 5]` | hard cap on sub-queries |
| `MEMDB_COT_TIMEOUT_MS` | `2000` | clamp `[500, 10000]` | LLM call timeout |

Default-OFF guarantees zero regression for existing clients. Operators
opt in by setting `MEMDB_COT_DECOMPOSE=true` after observing the metrics.

## Metrics

| Metric | Type | Attributes |
|---|---|---|
| `memdb.search.cot.decomposed_total` | counter | `outcome=success|skip|error` |
| `memdb.search.cot.subqueries` | histogram (1,2,3,4,5) | — |
| `memdb.search.cot.duration_ms` | histogram (100, 250, 500, 1000, 2000, 5000, 10000) | — |
| `memdb.search.cot.cache_hit_total` | counter | — |

## Expected lift on cat-2 / cat-3

> **Caveat (per spec):** The +5-7 lift estimate from
> `docs/competitive/2026-04-26-port-target-vec-cot.md` is an internal
> pre-M8 planning assumption (docs/backlog/search.md §Phase 1), NOT an
> independently measured delta from a MemOS ablation. We rank this
> technique #1 because it directly addresses MemDB's lowest-scoring
> categories (cat-2 multi-hop F1=0.091, cat-3 temporal F1=0.201), not
> because of an empirical published number. Re-evaluate after first
> ablation.

Stream 6 MEASURE will produce the actual cat-2 / cat-3 numbers with
`MEMDB_COT_DECOMPOSE=true` enabled in the live container. If S3 GRAPH
already lifted cat-2 independently, the compound effect should be
visible.

## Coordination notes

- **Soft conflict on `service.go`** with S3 GRAPH (PR #81): both touch
  the timing log block and the steps around D2 expandViaGraph. If S3
  lands first, rebase D11 on top and re-check the log fields.
- **No conflict** with S10 STRUCTURAL-EDGES (touches
  `internal/handlers/add_*.go`, ingest path) or with VEC_COT (different
  technique, separate stream — multi-vector embedding, not query
  decomposition).

## What's NOT in this PR

- Multi-vector embedding probe (VEC_COT) — same source paper, different
  technique, separate stream.
- Changes to D2 multi-hop walk depth / decay — that's S3 GRAPH.
- Changes to D3 reorganizer — out of scope.
- Adding structural edges at ingest — S10 STRUCTURAL-EDGES.
