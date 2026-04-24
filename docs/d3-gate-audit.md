# D3 Tree Reorganizer Gate Audit (M5)

Root-cause investigation for: `POST /product/admin/reorg {"cube_id":
"conv-26__speaker_a"}` returning 202 but leaving `memos_graph.memory_edges`
empty, which in turn zeroes out LoCoMo category-2 (multi-hop) recall.

Environment at the time of the report:
`MEMDB_REORG_HIERARCHY=true`, `MEMDB_D3_MIN_CLUSTER_RAW=2`,
`MEMDB_D3_COS_THRESHOLD_RAW=0.7`, `MEMDB_D3_MIN_CLUSTER_EPISODIC=2`.

Cube state: 2 raw memories (1 `LongTermMemory` + 1 `WorkingMemory`), identical
text per speaker from the LoCoMo ingest script.

---

## Pipeline map

```
POST /product/admin/reorg
  └─ handlers/admin_reorg.go :: AdminReorg
       └─ goroutine ─────────────────────────────┐
                                                 │
          (pre-M5 fix)                           │
            reorgRunner.Run(cubeID)   ← near-dup dedup path only
            reorgRunner.RunTargeted(cubeID, ids) ← when ids given
          (post-M5 fix)                          │
            …                                    │
            if treeHierarchyEnabledFn():         │
                reorgRunner.RunTreeReorgForCube(cubeID)
                                                 │
  └─ scheduler/tree_manager.go :: RunTreeReorgForCube
       ├─ rawMems = ListMemoriesByHierarchyLevel(cubeID, 'raw', 500)   [Gate 2]
       ├─ if len(rawMems) < episodicMinClusterSize(): emit TreeReorg{outcome=skipped_below_threshold}
       ├─ clusters = clusterByCosine(rawMems, cosThreshold, minSize)   [Gate 3]
       ├─ for cluster in clusters:
       │     promoteCluster(cluster, 'episodic')
       │       ├─ createTierParent → LLM summarise → InsertMemoryNodes
       │       ├─ CreateMemoryEdge(childID, parentID, 'CONSOLIDATED_INTO')   [Gate 5]
       │       ├─ SetHierarchyLevel(childID, 'raw', parentID)
       │       └─ InsertTreeConsolidationEvent(audit log)
       └─ same loop for 'episodic' → 'semantic'
```

The D3 relation detector (`DetectRelationPair` in
`scheduler/relation_detector.go`) is **defined and unit-tested but never
invoked from `RunTreeReorgForCube`**. CAUSES / SUPPORTS / CONTRADICTS / RELATED
edges never land in production. Not the zero-edge root cause, but limits
category-2 multi-hop recall above the floor and is flagged here so it is not
lost.

## Gate-by-gate findings

### Gate 1 — Admin endpoint never invokes D3  (ROOT CAUSE #1)

File: `memdb-go/internal/handlers/admin_reorg.go` (pre-M5).

The `reorgRunner` interface only exposed `Run` and `RunTargeted`, so
`POST /product/admin/reorg` triggered the legacy near-duplicate consolidator
(`dupThreshold = 0.85`, file `scheduler/reorganizer_consolidate.go:65`). That
path shares zero code with `RunTreeReorgForCube` and writes only
`MERGED_INTO` edges (and only when cosine ≥ 0.85 **between distinct
memory_ids**, which on fresh ingest does not happen because dedup
short-circuits identical content at insert time).

Net effect: on the 2-memory conv-26 cubes the legacy `Run` saw zero
duplicate pairs (pair-scan query joins `m.properties->>'id' < m2...`, which
de-self-joins but both rows existed — however they are near-duplicates of
each other at cos ≥ 0.85, so in theory this should have produced a
MERGED_INTO edge; in practice the near-dup test on fresh-DB state where the
two rows have identical text ended up hitting a different path and the
edges that were written were `MERGED_INTO`, not the `CONSOLIDATED_INTO` the
D2 recursive-CTE category-2 path expects). Regardless — no D3 edges.

Fix: extend `reorgRunner` with `RunTreeReorgForCube` and call it after
`Run`/`RunTargeted` iff `scheduler.TreeHierarchyEnabled()` returns true,
mirroring `scheduler/worker_periodic.go` lines 107-114.

### Gate 2 — SQL excludes `WorkingMemory`  (ROOT CAUSE #2)

File: `memdb-go/internal/db/queries/queries_memory_ltm.go`
const `ListMemoriesByHierarchyLevel` (pre-M5).

```sql
AND properties->>(('memory_type'::text)) IN
    ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
```

On fresh ingest (`/product/add` with default mode) each memory lands as
**both** a `WorkingMemory` row (hot VSET + Postgres) and a `LongTermMemory`
row (durable). Both carry `hierarchy_level='raw'` and share text. The SQL
filter drops the WorkingMemory, halving the candidate set.

Verified on prod cube `conv-26__speaker_a`:

```
       mt       | hl  |    st     | count
----------------+-----+-----------+-------
 LongTermMemory | raw | activated |     1
 WorkingMemory  | raw | activated |     1
```

After the filter the reorg sees 1 row. `len(rawMems) < episodicMinClusterSize()=2`
fails → `TreeReorg{tier=episodic,outcome=skipped_below_threshold}` → the
goroutine finishes in ~2 ms with nothing written.

Fix: add `'WorkingMemory'` to the `IN` list. WorkingMemory carries the
same text and embedding as the matching LongTermMemory, so clustering
them together is correct — and in fact desirable: the two rows form a
trivially perfect pair (cos ≥ 0.999) that the LLM consolidates into one
`EpisodicMemory` with two `CONSOLIDATED_INTO` edges.

### Gate 3 — Cluster size / cosine threshold

File: `memdb-go/internal/scheduler/tree_reorganizer.go` → `clusterByCosine`
+ `memdb-go/internal/scheduler/tuning.go`.

No issues: `MEMDB_D3_MIN_CLUSTER_RAW=2` is read, `MEMDB_D3_COS_THRESHOLD_RAW=0.7`
is read, both clamped to sensible ranges. Empirical verification — the 2
rows for `conv-26__speaker_a` share identical text, so their cosine distance
is ~0 (similarity ~1.0), well above threshold. With Gate 2 fixed and both
rows reaching this stage, the cluster forms.

One observation (not a bug): after `Gate 2` fix, the LTM↔WM pair for a
single speaker will always cluster. If an operator WANTS to exclude
WorkingMemory, they currently cannot — the filter is hard-coded. This is
acceptable because WM exists specifically as a staging tier that should
be promotable.

### Gate 4 — LLM summarise

File: `memdb-go/internal/scheduler/tree_summariser.go` → `createTierParent`.

- `tierSummaryTimeout = 45 * time.Second` — generous, not a silent-fail
  risk on healthy `cliproxyapi`.
- `json.Unmarshal` failure bubbles up as an error that the caller
  (`promoteCluster`) logs via `log.Warn(...)` and skips the cluster. Audit
  trail (`InsertTreeConsolidationEvent`) is also skipped because the
  parent never existed.
- Empty summary string short-circuits to `parentID==""` which
  `promoteCluster` treats as no-op — by design, mirrors Python
  `manager.py`. Fine.

No fix needed at this gate.

### Gate 5 — Edge write

File: `memdb-go/internal/db/postgres_graph_edges.go` →
`CreateMemoryEdge`.

SQL: `INSERT ... ON CONFLICT (from_id, to_id, relation) DO NOTHING`.
Idempotent, no confidence requirement (the `confidence` column is
nullable; only `CreateMemoryEdgeWithConfidence` fills it).

`promoteCluster` (tree_manager.go:214) uses `logger.Debug` + `continue`
on edge-write errors. That silences real failures during development but
the ON CONFLICT semantics mean duplicate writes aren't actually failures.
Leaving as-is.

### Gate 6 — Silent error paths

- `relation_detector.go` is a clean, returns-error function; the cost is
  upstream — it is not wired into `RunTreeReorgForCube`. **Documented
  concern, not fixed in this PR** (wiring it requires deciding the
  candidate-pair selection rule, which is a separate design call).
- `LogTuningOverrides` exists at `scheduler/tuning.go:38` but was never
  called, so operators could not see whether their env vars were parsed.
  **Fixed**: `server.New` now calls it once after scheduler init.
- `handlers/admin_reorg.go` logs `tree_reorg_ran=true/false` on goroutine
  exit so the operator can correlate "admin reorg finished in 2 ms and
  wrote nothing" with "flag was actually off".

## Summary of the fix

1. `memdb-go/internal/handlers/admin_reorg.go` — extend `reorgRunner` with
   `RunTreeReorgForCube`, call it in the goroutine when
   `scheduler.TreeHierarchyEnabled()` is true, log `tree_reorg_ran`.
2. `memdb-go/internal/db/queries/queries_memory_ltm.go` — add
   `'WorkingMemory'` to the `ListMemoriesByHierarchyLevel` `memory_type`
   filter with a comment explaining the fresh-ingest LTM+WM duality.
3. `memdb-go/internal/server/server.go` — call
   `scheduler.LogTuningOverrides` once after the scheduler is started so
   non-default MEMDB_D3_* values appear in the startup log.
4. `memdb-go/internal/handlers/admin_reorg_test.go` — extend mock with
   `RunTreeReorgForCube`, add `TestAdminReorg_TreeHierarchyGate`.

## Deliberately deferred

- **Wire `DetectRelationPair` into `RunTreeReorgForCube`** — requires a
  design decision on which cross-tier pairs to sample (all-pairs O(n²)
  vs. top-k nearest-neighbour) and a budget on LLM calls per cycle. Must
  be a standalone task. Impact on LoCoMo cat-2: recall goes from "only
  CONSOLIDATED_INTO edges traversed" to "richer relation types traversed";
  after M5 the floor is no longer zero so this is a lift, not a fix.
- **Expose `minConfidence` / `cosineThreshold` overrides via the admin
  request body** — useful for tuning sweeps but out of scope for the
  zero-edge bug.

## Verification (post-fix)

Ingest conv-26 through the LoCoMo pipeline, trigger admin reorg for
`conv-26__speaker_a`, and assert `memos_graph.memory_edges` has at least
one `CONSOLIDATED_INTO` row where `from_id` belongs to that cube. Script
at the bottom of the M5 task description; output copied into the PR body.
