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

### TODO — write-path repair (tracked separately)

Fix `cubes` table schema (Go code writes to `cube_id` text column; actual table is AGE vertex with `properties agtype`) + resolve `agtype_in(text)` function missing. After fix, rerun this harness — first milestone with non-zero numbers will establish the true post-infrastructure baseline. Every Phase D task (D1–D8, D10) measures against that new baseline.

## How to record a new milestone

```bash
export MEMDB_SERVICE_SECRET=$(docker exec memdb-go env | grep INTERNAL_SERVICE_SECRET | cut -d= -f2)
LOCOMO_SKIP_CHAT=1 OUT_SUFFIX=<tag-or-slug> bash evaluation/locomo/run.sh
python3 evaluation/locomo/compare.py results/baseline-v1.1.0.json results/<tag-or-slug>.json
```

Take the compare.py output, add a new `### <date> — <milestone name>` section above, commit the MILESTONES.md update in the same PR as the feature that produced the delta.
