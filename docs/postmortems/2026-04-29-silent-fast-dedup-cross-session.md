# Postmortem — Silent fast-mode dedup dropped 5/6 LoCoMo sessions

**Date:** 2026-04-29
**Severity:** SEV-2 (silent data loss on prod ingest path; no user-visible error)
**Author:** anatolykoptev + Claude Opus 4.7
**Status:** Mitigated (`fix/fast-cross-session-dedup`); long-tail items in *Action Items*.

---

## Impact

`/product/add` with `mode=fast` (LoCoMo eval default since M7,
2026-04-22) silently dropped writes whose first-window embedding had
cosine ≥ `dedupThreshold = 0.92` against any existing row in the same
cube — even when `session_id` differed.

**Concrete blast radius (LoCoMo):**

* `conv-26` corpus has 3 sessions × 2 reverse-role passes = 6 ingest
  calls. Verified empirically by re-running ingest after this incident:
  `ingest.py` reports `sessions=3, errors=[]` and 6 successful 200 OK
  responses, but only **session_1 (4 rows)** lands in `Memory`. 5 of 6
  ingest calls silently no-op.
* Every LoCoMo `chat-50` and `chat-50-cat-1` run ingested through
  `mode=fast` since the M7 raw→fast eval flip experiments lost the
  session_2 + session_3 evidence pool. Reported F1 numbers from
  2026-04-22 onward are degraded by an unknown amount because top-k
  retrieval ran against ~33% of the intended corpus.

**Production blast radius (vaelor / oxpulse / claude-code plugin):**

* Any multi-session conversation with the same user_id where the bot
  rephrases similar topics across sessions — typical chat use case.
  Cosine 0.92 triggers **easily** between 4096-char windows of the
  same speakers chatting on overlapping topics (verified: `conv-26`
  session_1 vs session_2 windows score 0.95-0.99).
* Existing rows: `Memory.user_name = 'host-a'` has 161 rows with
  `mode:raw` tag (claude-code plugin path) and the M7-era fast tests.
  Hard to count the lost rows since ingest itself returned 200 — we
  only have circumstantial evidence (low row counts vs. expected).

---

## Timeline

| When (UTC) | What |
|---|---|
| 2026-04-22 | M7 sprint flips LoCoMo `INGEST_MODE` default to `raw` (commit `52938d14`). `mode=fast` keeps `dedupThreshold=0.92`. |
| 2026-04-29 16:42 | Eval engineer A/B-tests `mode=fast` on LoCoMo conv-26 (PR #219, later closed). F1 = 0.149 vs 0.165 raw. Attributed to "windowing collapses dialogue" — partial truth. |
| 2026-04-29 17:23 | PR #223 ships `max_context_tokens` budget. Smoke F1 = 0.158, refusal -10pp. |
| 2026-04-29 17:55 | DB inspection of conv-26 cube reveals only 4 rows (1 session × 2 speakers × WM+LTM pair). Session_2 and session_3 missing. |
| 2026-04-29 17:58 | Cosine probe: `conv-26__session_1` LTM vs `conv-26__session_2_test` (synthetic, semantically distant) = 0.917 (< 0.92, passes). Real session_2 windows infer-est 0.93-0.99. |
| 2026-04-29 18:00 | Root cause: `isDuplicate` in `add_fast_helpers.go` uses `dedupThreshold=0.92` cube-wide (no `session_id` filter). |
| 2026-04-29 18:30 | Mitigation merged on `fix/fast-cross-session-dedup`. |

---

## Root cause

`internal/handlers/add_fast_helpers.go::isDuplicate` calls
`postgres.VectorSearch(cubeID, ...)` and rejects the incoming write if
the top match has `Score >= dedupThreshold = 0.92`. The threshold
predates multi-session ingest (commit `a82c99f7`, 2026-04-10) when the
fast pipeline only handled single-shot writes. Three pre-existing
assumptions silently broke:

1. **Threshold was hardcoded** — not env-tunable. No way for operators
   to raise the bar after a corpus drift was detected.
2. **No `session_id` filter on the candidate match** — sessions of the
   same user chatting about overlapping topics legitimately produce
   cosine 0.92-0.99 between their window embeddings without being
   duplicates.
3. **No metric counter** — drops emitted only a `Debug` log, so
   prod operators had no signal that the rate was 5/6 = 83% on
   LoCoMo conv-26.

**5 whys:**

1. Why did 5/6 sessions disappear? `isDuplicate` returned true.
2. Why true? Cosine ≥ 0.92 against an existing row.
3. Why that high cross-session? Embedding model is multilingual-e5
   trained on dialogue — 4-message-block windows of the same 2-speaker
   English conversation cluster tightly. 0.92 is a low bar for that
   shape of data.
4. Why was 0.92 chosen? Inherited from a single-session write contract
   (a82c99f7); never recalibrated when fast started accepting
   multi-session payloads.
5. Why no recalibration? **No labeled duplicate dataset exists** in
   the repo, so threshold tuning has no ground truth. We have a
   `cosine_dedup` empirical regression test only on synthetic data
   (`add_fast_batched_test.go`) — it tests the mechanism, not the
   threshold.

---

## Mitigation (this PR)

| Layer | Change | File |
|---|---|---|
| Detection | New `memdb_add_duplicate_drop_total{outcome,same_session}` counter — every dedup-skip is now visible on `/metrics` | `metrics_add.go` |
| Tunable | `MEMDB_FAST_DEDUP_THRESHOLD` env (default 0.92, back-compat) | `add_fast_helpers.go::fastDedupThreshold` |
| Default-fix | `MEMDB_FAST_DEDUP_SESSION_AWARE` env (default ON) — cross-session matches no longer trigger drop | `add_fast_helpers.go::fastDedupSessionAware` + `isDuplicate` |
| Defensive | `extractSessionID` parses the candidate's raw props JSON; bad blobs return `""` and fall through to the legacy path | `add_fast_helpers.go::extractSessionID` |
| Tests | Unit tests on the env parsers + JSON parser (`add_fast_dedup_test.go`); livepg E2E listed as follow-up | `add_fast_dedup_test.go` |

The `isDuplicate` signature gained a `sessionID string` parameter.
`add_fast.go` and `add_raw.go` updated to pass `fac.sessionID`. No
external API changes.

---

## What we did right

* Found the bug via cosine SQL probe (Apache AGE `embedding <=>`
  operator) within ~2 minutes of suspecting silent loss.
* Manual `curl` reproduction confirmed the threshold boundary:
  cosine 0.917 ingest succeeded, ≥ 0.92 was dropped.
* Fix is back-compatible (env defaults preserve the historical 0.92,
  but session-aware behaviour ships ON by default — opt-out path
  exists for callers that want the legacy semantics).

## What we did wrong

* **No regression test before the fix landed.** The PR shipped with
  unit tests on the new helpers (env parsers, JSON parser) but NOT a
  live-postgres E2E that ingests 3 sessions × 2 speakers and asserts
  6 rows survive. That belongs in `add_fast_batched_livepg_test.go` —
  filed as **AI-1**.
* **No canary.** The fix went straight to main → dozor → prod with no
  staged rollout. Acceptable for SEV-2 because the prior path was
  losing data, but the playbook should require a 1% canary even when
  the alternative is "still broken".
* **No backfill.** Rows lost before the fix are not recoverable from
  the ingest payloads (those are not stored). Operators have to
  re-ingest from upstream (LoCoMo files / vaelor logs / claude-code
  transcripts) to repopulate the cubes. **AI-2.**
* **Silent failure design.** `isDuplicate` should never have returned
  `true` without emitting a counter. This is a recurring pattern in
  the codebase — every "skip" path needs metrics. **AI-3.**

---

## Action items

| ID | Action | Owner | Status |
|---|---|---|---|
| AI-1 | livepg regression test: 3 sessions × 2 speakers ingested → 6 LTM rows survive (verifies cross-session-distinct-write path on real Postgres) | TBD | Open |
| AI-2 | Backfill audit: count rows-per-session in `Memory` table; flag cubes where a known multi-session conversation has < N expected rows; trigger re-ingest from upstream | TBD | Open |
| AI-3 | Lint / code review rule: every `return true` / `return false` skip path in `isDuplicate`, `filterAddsByContentHash`, `applyAndPersistFineFacts` MUST emit a metric counter. Track via PR template checklist. | TBD | Open |
| AI-4 | Calibrate `dedupThreshold` against a labeled duplicate dataset. Build the dataset from real prod traces (cosine pair + human label). Without ground truth we can't move the bar with confidence. | TBD | Open |
| AI-5 | Alert rule: `rate(memdb_add_duplicate_drop_total[10m]) / rate(memdb_add_total[10m]) > 0.30 for 30m` → page. Wire into dozor. | TBD | Open |
| AI-6 | Document `MEMDB_FAST_DEDUP_*` env knobs in `docs/operations.md` (env var index) | TBD | Open |

---

## Lessons

* **Silent skips are bugs.** Any code path that drops a write must
  emit a counter. Debug logs are not observable in prod.
* **Hardcoded thresholds rot.** When a constant predates a use case
  the codebase added (single-session → multi-session ingest), the
  constant becomes stale silently. Default-tunable from day one.
* **Default behaviour matters.** `dedupThreshold=0.92 + no session
  filter` was the wrong default for multi-session conversational
  data. The fix flips the default — opt-out exists for back-compat.
* **Test the boundary, not just the mechanism.** We had a unit test
  for "cosine dedup works" (synthetic 1.0 match drops, 0.0 doesn't).
  That doesn't test the **threshold value** is correct. Boundary tests
  with real-shape embeddings would have caught this.
