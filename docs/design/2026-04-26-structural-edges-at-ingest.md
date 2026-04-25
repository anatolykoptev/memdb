# Structural Edges at Ingest — M8 Stream 10

**Date:** 2026-04-26
**Status:** implemented (`feat/structural-edges-at-ingest`)
**Owner:** Anatoly Koptev
**Related:** M8 Stream 3 GRAPH (PR #81, retrieval-side D2 multihop fix), M8 Stream 6 MEASURE (compound effect validation)

## Problem

M7 Stage 2 measured cat-2 (multi-hop) LoCoMo F1 = 0.091 — the lowest of all 5 categories. Investigation traced one root cause to the `memory_edges` table being sparsely populated. Edges were only emitted when the D3 LLM reorganizer fired (relation detector, hierarchy promotion, MERGED_INTO consolidation). On a fresh cube the table contains essentially zero rows for hours, so D2's recursive CTE has nothing to traverse — multi-hop falls back to single-hop vector search and the cat-2 F1 collapses.

## Solution

Emit three families of cheap **structural** edges synchronously at ingest, with no LLM call and at most one extra embedding pass per memory (reusing the dedup vector search).

| Relation | Trigger | Direction | Properties |
|---|---|---|---|
| `SAME_SESSION` | every pair sharing `(cube_id, session_id)` | new → existing | confidence=1.0 |
| `TIMELINE_NEXT` | adjacent rows in session sorted by `created_at` ASC | newer → older | confidence=1.0, rationale=`{"dt_seconds":N}` |
| `SIMILAR_COSINE_HIGH` | cosine ∈ (0.85, dedupThreshold) | new → partner | confidence=cosine_score |

Caps to prevent runaway fan-out:
- `SAME_SESSION` ≤ 20 partners per new memory (long sessions overflow into the `memdb.add.same_session_capped_total` counter).
- `SIMILAR_COSINE_HIGH` ≤ top-5 per new memory.
- `TIMELINE_NEXT` is naturally one edge per memory.

Skip rules:
- `SAME_SESSION` and `TIMELINE_NEXT` skipped when `session_id == ""`.
- `SIMILAR_COSINE_HIGH` skipped for memories already filtered as duplicates (≥ dedupThreshold).
- Async mode (`nativeAsyncAddForCube`) does not emit structural edges because it only persists transient WorkingMemory nodes — the durable LTM is created later by the WM→LTM transfer worker. Edges from a WM ID would dangle the moment the worker deletes it.

## Files

| Path | Lines | Role |
|---|---|---|
| `memdb-go/internal/db/postgres_graph_edges.go` | +12 | `EdgeSameSession`, `EdgeTimelineNext`, `EdgeSimilarCosineHigh` constants |
| `memdb-go/internal/db/postgres_graph_edges_bulk.go` | new | `BulkInsertMemoryEdges`, `GetSessionMemoryNeighbors`, `MemoryEdgeRow`, `SessionMemoryNeighbor` |
| `memdb-go/internal/db/queries/queries_graph.go` | +50 | `BulkInsertMemoryEdges` (UNNEST), `SessionMemoryNeighbors` queries |
| `memdb-go/internal/handlers/add_structural_edges.go` | new | Orchestrator + SAME_SESSION + TIMELINE_NEXT helpers |
| `memdb-go/internal/handlers/add_structural_edges_cosine.go` | new | SIMILAR_COSINE_HIGH helpers (split for size cap) |
| `memdb-go/internal/handlers/add_structural_edges_test.go` | new | Pure-helper unit tests |
| `memdb-go/internal/handlers/add_structural_edges_livepg_test.go` | new | livepg end-to-end probe |
| `memdb-go/internal/handlers/add_fast.go` | +24 | `fastBatchLTMRefs` + `emitStructuralEdges` tail |
| `memdb-go/internal/handlers/add_raw.go` | +21 | `rawBatchRefs` + `emitStructuralEdges` tail |
| `memdb-go/internal/handlers/metrics_add.go` | +14 | `StructuralEdges`, `SameSessionCapped` counters |

No schema migration required — `memory_edges.relation` is `TEXT`, the new types slot in alongside D3-emitted ones.

## Trade-offs

**Cost per ingest call**:
- 1 extra `SELECT` (`GetSessionMemoryNeighbors`) — only when `session_id != ""`.
- 1 extra `VectorSearch` per new memory (`SIMILAR_COSINE_HIGH` candidate scan).
- 1 extra `INSERT` (`BulkInsertMemoryEdges`) — single round-trip for all edges via UNNEST.

Measured on the livepg probe: 5-turn raw-mode session, total `nativeRawAddForCube` time **242ms (avg 49ms/turn)**, of which the structural-edge tail is bounded by the three round-trips above. Well within the < 5ms budget the design called for under load (the headline 49ms is dominated by ONNX-stub embedding + Postgres connection RTT, not the new edges code).

**Storage cost**: ~10–30 INSERT per `/product/add` call. With 1KB average row size, 1k cubes × 100 sessions × 10 turns × 30 edges = ~3 GB. The existing `memory_edges_active_idx` partial index (WHERE invalid_at IS NULL) keeps reads cheap.

**Query cost**: D2's recursive CTE now has 10–30× more candidate edges to traverse. The `memory_edges` PK already covers `(from_id, to_id, relation)` so traversal stays index-bound.

## What this fixes

- D2 multi-hop traversal has dense connectivity from the moment a memory lands, instead of waiting for D3 (which may never fire on cubes that don't trigger reorganization).
- TIMELINE_NEXT carries `dt_seconds` so future D2 weight functions can decay edges across long temporal gaps.
- SAME_SESSION provides a "topical neighbourhood" prior independent of embedding quality.
- SIMILAR_COSINE_HIGH slots in just below the dedup threshold — captures "interesting but distinct" semantic neighbours that would otherwise need an LLM to infer RELATED.

## What this does NOT fix

- D2 retrieval logic itself (S3 owns that — `internal/search/multihop*.go`).
- LLM-grade RELATED / CAUSES / SUPPORTS edges (D3 still owns these; structural edges complement them).
- Pre-computed CE rerank scores (M9 backlog).
- PageRank / centrality metrics over `memory_edges` (M9 backlog).

## Validation

- Unit tests (15 functions, hermetic): `add_structural_edges_test.go`.
- Live Postgres probe: `add_structural_edges_livepg_test.go`. Five-turn session produces SAME_SESSION=10, TIMELINE_NEXT=4 (matching the math `n*(n-1)/2 = 10` partners and `n-1 = 4` chain links).
- Existing add tests (`add_test.go`, `add_fast_batched_test.go`, `add_raw_test.go`, `add_async_test.go`) all green.

## Reference

Pattern lifted from go-code commit `de40df1` — INHERITS / IMPLEMENTS edges emitted at AST parse time instead of waiting for a downstream pass. Same idea: cheap, deterministic edges at the moment data lands.
