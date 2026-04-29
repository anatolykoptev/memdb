# Uniform Extraction Pipeline — STATUS: SCOPE NARROWED 2026-04-29

> **Final scope:** Task 1 only — schema unification (every Memory row gets `extraction_state` + null placeholder atomic keys). Tasks 2–5 (raw=pending writes, background filler, filler metrics, opt-in backfill) are **cancelled**. Reason: senior call (2026-04-29) — `mode=raw` is a documented "no LLM" zone (commit `a82c99f7`, 2026-04-10) and any background filler that runs LLM extract on raw rows would violate that contract. The supported path for atomic enrichment is to **switch to `mode=fine` (or `mode=fast`)** at the call site, not to retroactively LLM-extract raw rows.

## What landed (Task 1, commit `4fd5df82`)

- `memdb-go/migrations/0028_extraction_state.sql` — backfills every existing row to `extraction_state='extracted'` and creates `idx_memory_extraction_pending` partial index.
- `memdb-go/internal/handlers/add_props.go` — adds 4 `extractionState*` constants (`pending|extracting|extracted|failed`); `buildMemoryProperties` and `buildMemoryPropertiesAt` now accept and write `state, attemptedAt, completedAt` to `properties`.
- 9 caller files updated to pass `extractionStateExtracted` (raw / fast / fine / async / atomic / update) — current behaviour preserved.
- `memdb-go/internal/handlers/add_props_test.go` — 4 new tests covering all state values + timing-field present/absent.
- All handler tests pass (`40.7 s`, `0` failures).

This task is a **forward-compatible schema cleanup**. It does not change runtime behaviour and is safe to merge on its own.

## What was cancelled (and why)

| Original task | Cancelled because |
|---------------|-------------------|
| Task 2 — raw/fast write `state=pending` | Would silently mark raw rows for LLM extract. Raw is "no LLM" by contract. |
| Task 3 — `extraction_filler` scheduler loop | Background LLM extractor on raw rows breaks the raw contract; the supported path is mode-switch at the call site. |
| Task 4 — filler metrics (filled/latency/pending/retry) | No filler → no metrics needed. Existing `add_atomic_facts_extracted_total` already covers the fine-path success rate. |
| Task 5 — `cmd/extraction-backfill` opt-in re-extraction | Same reason — anything that bulk-flips raw rows to `pending` is the same contract violation in a CLI wrapper. If ops genuinely needs to re-LLM-extract a cube, they should re-ingest with `mode=fine`. |

## Allowed follow-ups (NOT auto-scheduled — sign-off required)

1. **Per-request opt-in flag** — add `extract_async=true` body field. When the caller (not the system) explicitly opts in, raw can be written with `state=pending`. The default is still `extracted`. Pair with a metric `attribution_filter_total{outcome=opt_in}`.
2. **Non-LLM enrichment pass on raw** — regex ISO-date extraction → fill `event_dates` without any LLM call. Stays inside the raw contract because no LLM rewrites the text. Cheap proxy for F11 on raw cubes.
3. **Eval mode switch** — for LoCoMo runs that specifically want to measure atomic-facts contribution, set `LOCOMO_INGEST_MODE=fine` (or `fast`) explicitly. Do NOT change the eval default — M7 picked raw for per-message granularity, and that decision still stands for the chat_time / per-msg-metadata pipeline.

## Self-review (post-cancel)

- **Schema migration alone is safe to merge** — covered by 4 unit tests, idempotent SQL, partial index keyed off `pending` (zero rows post-migration), no rollback risk.
- **No dead code introduced** — every new field / constant has at least one caller after Task 1's caller updates.
- **CLAUDE.md updated** — `/home/krolik/src/MemDB/CLAUDE.md` now documents the raw / fast / fine contract and the "no LLM in raw" rule, so future plans don't repeat this proposal.
- **Original plan stays in this file** below for archival reference. Do NOT execute it.

---

## ARCHIVED (do not execute) — original Task 1–5 plan

The text below is the original plan as drafted on 2026-04-29 before the senior-call cancellation of Tasks 2–5. Kept for archival so future readers can see what was proposed and rejected.

### Goal (archived)

All `/product/add` modes (raw / fast / fine) produce Memory rows with the IDENTICAL property schema. The `mode` parameter only controls **when** atomic extraction runs (sync inline vs async via background filler) — never **what** ends up in the row. Eventual consistency through a scheduler loop.

### Architecture (archived — REJECTED)

Add new property keys to every Memory row (`extraction_state`, `extraction_attempted_at`, `extraction_completed_at`) plus null placeholders for atomic-extracted fields (`kind`, `attributed_to`, `event_dates`, `linked_memory_ids`). Sync-fine path keeps writing extracted rows immediately. Raw + fast paths write `state=pending`, return 200, and a new periodic scheduler loop (`extraction_filler`) walks pending rows, runs `AtomicExtractor.ExtractAtomicFacts`, and updates the same row in place.

**Why rejected:** raw mode was introduced (`a82c99f7`, 2026-04-10) explicitly to protect structured payloads from any LLM rewrite. A background filler that runs LLM extract on every raw row would silently re-introduce the LLM step that raw was designed to bypass. The correct architectural answer is mode-switching at the call site (raw → fine when you want atomic) plus a per-request opt-in flag for the rare case where async-extract semantics on raw are actually what the caller wants.

### Tasks 2–5 (archived — DO NOT EXECUTE)

The detailed task breakdown for Tasks 2 (raw/fast=pending), 3 (filler loop), 4 (filler metrics), and 5 (backfill cmd) lived in earlier revisions of this file. They are removed from this document to prevent accidental execution. If they need to be revived under different framing (e.g. opt-in flag, non-LLM enrichment), write a NEW plan rather than reviving this one.
