# M14 — MathReranker integration

**Date:** 2026-04-29
**Owner:** controller (subagent-driven)
**Status:** ready to dispatch (after M12 baseline finishes)

## Goal

Wire `go-kit/rerank.MathReranker` (G5, v0.29.0) into MemDB in two **parallel, disjoint** streams:

1. **A1 — Stage-0 pre-CE diversity prefilter.** Quality lift on cat-1 (multi-fact aggregation) by handing CE a diversity-aware top-30 instead of plain cosine top-30.
2. **B1 — CE precompute background prefilter.** Latency win on background scheduler — drop pairs with cosine <0.3 before sending to bge-reranker. −30–40% embed-server CPU on `tree_ce_precompute`.

Both gated behind env-flags, default OFF. No quality/latency change unless explicitly enabled.

## Background

* M11 LoCoMo run revealed silent disable of LLM Judge → cost 5pp on cat-2. Reasserted: **NEVER silently change behavior.**
* M12 baseline (1986 QA, in flight) is the apples-to-apples reference. Don't ship anything before it finishes.
* go-kit v0.29.0 brings G5 `MathReranker` — pure-Go cosine + Carbonell-Goldstein MMR. Zero LLM, zero HTTP. Already vendored in PR #181.
* Current `internal/search/rerank/mmr.go` is **NOT** a true MMR — it's a greedy cosine-threshold *filter* (drop ≥0.8). MathReranker is a true λ-balanced *ranker*. Different algorithms, different roles. We're **adding** MathReranker, not replacing existing.

## Stream A1 — Stage-0 pre-CE diversity prefilter

### What

Insert MathReranker as the first stage in the rerank chain, with `Lambda=0.5` (relevance-vs-diversity balance). Output → CE input.

### Where

* `internal/search/service_postprocess.go` — chain build site at L147–180.
* `internal/search/rerank_adapter.go` — extend `mapItem` to expose embedding vector OR pass an `embByID` closure (same pattern as `NewMMRFromEnv` at L88).
* New file `internal/search/rerank/math_prefilter.go` — adapter `MathPrefilter` wrapping `gokitrerank.MathReranker` to satisfy local `Reranker` interface (`Rerank(ctx, query, []Item) ([]Item, error)`).

### Env

* `MEMDB_RERANK_MATH_PREFILTER` — default `0` (disabled). Set `1` to engage.
* `MEMDB_RERANK_MATH_LAMBDA` — default `0.5`. Range `[0, 1]`.

### Wiring sketch

```go
// service_postprocess.go (chain build, ~L173 area)
chain := []rerank.Reranker{}

if mathPrefilterEnabled() {
    chain = append(chain, rerank.NewMathPrefilterFromEnv(embByID, queryVec))
}

chain = append(chain, ce, llmJudge, rerank.NewMMRFromEnv(embByID))
```

```go
// internal/search/rerank/math_prefilter.go
type MathPrefilter struct {
    EmbeddingsByID map[string][]float32
    QueryVector    []float32
    Lambda         float32
}

func (m MathPrefilter) Name() string { return "math_prefilter" }

func (m MathPrefilter) Rerank(ctx context.Context, _ string, items []Item) ([]Item, error) {
    if m.QueryVector == nil || len(items) == 0 {
        RecordSkipped(ctx, m.Name())
        return items, nil
    }
    docs := make([]gokitrerank.Doc, 0, len(items))
    for _, it := range items {
        emb := m.EmbeddingsByID[it.ID()]
        if emb == nil {
            continue
        }
        docs = append(docs, gokitrerank.Doc{
            ID: it.ID(), Text: it.EmbeddingText(), EmbedVector: emb,
        })
    }
    mr := gokitrerank.MathReranker{QueryVector: m.QueryVector, Lambda: m.Lambda}
    res, err := mr.RerankWithResult(ctx, "", docs)
    if err != nil || res.Status != gokitrerank.StatusOk {
        RecordSkipped(ctx, m.Name())
        return items, nil
    }
    // Reorder items by res.Scored.OrigRank
    byID := make(map[string]Item, len(items))
    for _, it := range items {
        byID[it.ID()] = it
    }
    out := make([]Item, 0, len(items))
    for _, s := range res.Scored {
        if it, ok := byID[s.ID]; ok {
            it.SetMeta("math_prefilter_score", s.Score)
            out = append(out, it)
            delete(byID, s.ID)
        }
    }
    // Items without embedding fall through to the bottom.
    for _, it := range byID {
        out = append(out, it)
    }
    RecordSuccess(ctx, m.Name())
    return out, nil
}
```

### Metrics

* `memdb.search.math_prefilter_engaged_total` — counter (Q3 default OTel naming).
* `memdb.search.math_prefilter_lambda` — gauge (env-set Lambda).
* `memdb.search.math_prefilter_skipped_total{reason}` — counter (`no_query_vec | no_items | empty_embeddings`).

### Tests

1. `TestMathPrefilter_LambdaZero_PureCosineSort` — Lambda=0 → items sort by cosine to query.
2. `TestMathPrefilter_LambdaHalf_DiversityKicks` — synthetic 5 docs with 2 near-duplicate vecs; expect MMR penalty pushes one duplicate down.
3. `TestMathPrefilter_NoQueryVec_Passthrough` — env enabled but no queryVec → return items unchanged + `math_prefilter_skipped_total{reason=no_query_vec}` += 1.
4. `TestMathPrefilter_DisabledByEnv_Skipped` — `MEMDB_RERANK_MATH_PREFILTER=0` → MathPrefilter not in chain.
5. `TestMathPrefilter_ItemWithoutEmbed_FallsToBottom` — 3 docs, 1 missing embed → kept, but ranked after embed'd ones.
6. Parity test: with `MEMDB_RERANK_MATH_PREFILTER=0`, post-process output identical to pre-A1 baseline.

### Acceptance

* Build clean.
* New tests pass.
* Existing rerank tests untouched.
* Disabled-by-default verified by parity test.

### Risks & mitigation

* **Risk:** MathReranker reordering shifts CE input → cat-2/4 recall regression on edge cases. **Mitigation:** env-gated, off by default; A/B 50 QA before flipping default.
* **Risk:** `embByID` map allocation per query — measurable on hot path. **Mitigation:** map already built upstream for `NewMMRFromEnv`, reuse the same one.

---

## Stream B1 — CE precompute background prefilter

### What

In `tree_ce_precompute` scheduler job: before sending pairs `(anchor, neighbor)` to bge-reranker via `embed-server`, drop pairs where cosine(anchor.emb, neighbor.emb) < 0.3. These are obvious bottom — bge would rank them low anyway.

### Where

* `internal/scheduler/tree_ce_precompute.go` — main job loop.
* `internal/search/cross_encoder_precompute.go` — pair construction.
* `internal/search/rerank/staged_backend.go` — confirm bge-reranker entrypoint (no edits expected, observation only).

### Env

* `MEMDB_CE_PRECOMPUTE_MATH_PREFILTER` — default `0` (disabled). Set `1` to engage.
* `MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD` — default `0.3`. Range `[0, 1]`.

### Wiring sketch

```go
// internal/scheduler/tree_ce_precompute.go
// Before sending (anchor, neighbors) batch to embed-server bge call:

if cePrecomputeMathPrefilterEnabled() {
    threshold := envFloat("MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD", 0.3)
    filtered := make([]Neighbor, 0, len(neighbors))
    skippedCount := 0
    for _, n := range neighbors {
        cos := CosineSimilarity(anchor.Embedding, n.Embedding)
        if float64(cos) >= threshold {
            filtered = append(filtered, n)
        } else {
            skippedCount++
        }
    }
    if skippedCount > 0 {
        recordCEPrecomputeMathSkipped(ctx, skippedCount)
    }
    neighbors = filtered
}

// existing call: send (anchor, neighbors) to bge via embed-server
```

### Metrics

* `memdb.scheduler.ce_precompute_math_skipped_total` — counter (pairs dropped pre-CE).
* `memdb.scheduler.ce_precompute_math_kept_total` — counter (pairs sent to bge).
* Existing `embed_server` Prometheus shows the −30% load reduction directly.

### Tests

1. `TestCEPrecomputeMathPrefilter_DropsBelowThreshold` — synthetic 10 neighbors with cos in `[0.1, 0.9]`, threshold=0.3 → 3 dropped.
2. `TestCEPrecomputeMathPrefilter_DisabledByEnv_AllSent` — flag off → all 10 sent to bge.
3. `TestCEPrecomputeMathPrefilter_ZeroThreshold_AllSent` — threshold=0 → all kept.
4. `TestCEPrecomputeMathPrefilter_MissingAnchorEmbed_BypassFilter` — anchor.Embedding empty → all neighbors sent (don't drop on missing data).
5. `TestCEPrecomputeMathPrefilter_MissingNeighborEmbed_KeepNeighbor` — neighbor without embed → keep (graceful degrade, same pattern as `rerank/mmr.go:120`).

### Acceptance

* Build clean.
* Tests pass.
* Disabled-by-default → bytewise identical pair lists to bge.
* Operator can enable/disable at runtime via env restart of scheduler.

### Risks & mitigation

* **Risk:** Threshold 0.3 too aggressive → ce_score_topk cache misses on items that *would* have ranked high after CE refinement (CE can sometimes rescue cosine-bottom semantically-close items). **Mitigation:** 0.3 is far below cosine median (typically 0.4–0.7 in our corpus); items at <0.3 are essentially noise. Conservative threshold confirmed by `dedup.go::DedupMMR` exp-penalty also kicks at 0.9 (very aggressive only at high end), 0.3 is the safe low-end cliff.
* **Risk:** anchor without embedding → wrong filter behavior. **Mitigation:** explicit bypass — if anchor.Embedding empty, send all (test #4).

---

## Dispatch plan

Both streams: **disjoint files, parallel-safe via worktree** (per `feedback_backend_subagents_serial.md` exception).

### Worktree A1
```
git worktree add /tmp/worktree-m14-a1 -b feat/m14-a1-math-prefilter origin/main
```

### Worktree B1
```
git worktree add /tmp/worktree-m14-b1 -b feat/m14-b1-ce-precompute-math origin/main
```

Subagents run in their own checkouts. Each opens a PR; controller merges serially **after M12 baseline finishes**.

### Non-overlap proof
* A1 touches: `internal/search/service_postprocess.go`, `internal/search/rerank/math_prefilter.go` (NEW), `internal/search/rerank_adapter.go` (read only — uses existing `mapItem`).
* B1 touches: `internal/scheduler/tree_ce_precompute.go`, `internal/search/cross_encoder_precompute.go`.
* Zero file overlap.

---

## Sequencing (controller orchestration)

1. **Wait** for M12 full-corpus baseline run to finish (ETA ~1.5h, currently 1041/1986).
2. **Dispatch A1 + B1** subagents in parallel with worktree isolation. Sonnet 4.6 — both are routine code wiring, no architectural decisions left.
3. **Spec review + quality review** for each PR (two-stage review per `feedback_subagent_driven_development.md`).
4. **Merge order**: A1 first (low blast radius — search path, gated), then B1 (background scheduler, gated). Merge serially via controller, NOT via subagents.
5. **A/B run** post-merge: 50 QA with `MEMDB_RERANK_MATH_PREFILTER=1`, `MEMDB_RERANK_MATH_LAMBDA=0.5`. Compare cat-1 vs M12 baseline.
6. **B1 latency measurement**: 1h scheduler observation with flag on/off, `embed_server` Prometheus delta.

## Out of scope (explicitly NOT doing)

* Replacing `dedup.go::DedupMMR` — custom alpha tuned, separate sprint.
* Replacing `internal/search/rerank/mmr.go` — threshold filter ≠ ranker, different roles.
* B3 (cap K before staged bge) — wait for A1+B1 evidence first.

## Rollback

Both streams env-gated. To rollback: unset `MEMDB_RERANK_MATH_PREFILTER` / `MEMDB_CE_PRECOMPUTE_MATH_PREFILTER`. Code stays in tree (zero-cost when disabled).
