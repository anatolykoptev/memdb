# M7 Follow-ups — 2026-04-26+

Captured during the M7 sprint (2026-04-25). One-day-sprint discipline kept these out of scope; they go here for the next session.

## From Stream F (perf/latency report `38c87eda`)

1. **Document `window_chars < 1024` latency cliff in `WindowChars` godoc.**
   - Observed: synthetic 30-msg conversation with `window_chars=512` produced p95 = 20.0s vs 1.2s at default 4096 (+1551%) due to 24× sequential embed calls.
   - Fix scope: 3-line addition to `memdb-go/internal/handlers/types.go` `WindowChars` field godoc explaining latency cost grows linearly with window count, recommending ≥1024 for latency-sensitive paths.
   - Effort: <30 min.

2. **A/B test `answer_style=factual` as default for QA workloads.**
   - Observed: factual prompt is 2.1× faster at p95 (14.7s → 7.0s) AND scores higher F1 (compound result). Two-for-one win.
   - Risk: changing default behavior breaks chat clients expecting verbose conversational replies.
   - Approach: add env `MEMDB_DEFAULT_ANSWER_STYLE` with deploy-time default still `conversational`; let go-nerv / vaelor opt in to `factual` per-route. Run a 10% canary on the `memos` cube to compare answer-acceptance rates.
   - Effort: 1-2 days (canary infra + measurement).

3. **Register `/debug/pprof/*` routes in `server_routes.go`.**
   - Observed: pprof endpoint returned 404 during M7 perf run, blocking CPU-profile diagnosis of the window=512 latency.
   - Fix: add `_ "net/http/pprof"` import in `cmd/server/main.go` + register `/debug/pprof/` group behind the existing internal-auth middleware (do NOT expose publicly — would leak goroutine state and stack traces).
   - Effort: <30 min.

4. **Batch embed calls per request in `extractFastMemories` path.**
   - Observed: 24 sequential ~550ms embed calls dominate window=512 latency. Embed-server already supports batching (`POST /v1/embeddings` with multi-input).
   - Fix scope: collect all window texts first, single batched embed call, then dedup+insert per memory. Should cut window=512 p95 from ~20s to ~2s (≥10× lift).
   - Risk: changes the embed call shape — embed-server timeout / batch-limit semantics may need tuning.
   - Effort: 2-4h.

## From Stream E (LoCoMo Stage 1+2 measurement `f9f16132`)

5. **Stage 3 — full 10-conv 1986-QA run.**
   - Status: deferred. Plan command in MILESTONES.md section "Stage 3 plan".
   - Expected duration: ~15h end-to-end (1986 × ~27s with chat) or ~1h with `LOCOMO_SKIP_CHAT=1`.
   - Approach: run retrieval-only first (gates whether per-conv cosine signal holds beyond conv-26), then selective chat scoring on a stratified subset (one per cat per conv = 50 chat scores, ~25 min).
   - Decides: whether the 0.238 aggregate F1 generalises across all 10 conversations or is conv-26-specific.

6. **cat-2 multi-hop F1 = 0.091 — D2 still under-firing.**
   - Observed: even with raw ingest + threshold fix, multi-hop scoring (37 QAs) didn't lift like cat-1/3/4. Indicates D2 graph traversal isn't surfacing connected facts during retrieval.
   - Diagnosis next: log D2 hop attempts vs hits in cat-2 question runs; check if `D2_MAX_HOP=3` is being honored.
   - Effort: 1-2 days (instrumentation + tuning).

7. **cat-5 adversarial F1 dropped 0.133 → 0.092 between Stage 1 sample and Stage 2 full.**
   - Hypothesis: with more raw memories available, factual prompt is finding *something* relevant for adversarial questions instead of correctly saying "no info".
   - Action: review failure modes on cat-5 in `m7-stage2.json` predictions — see if false-positive answers are substituting for `no answer`.
   - Effort: 4h investigation.

## Process improvements (TL self-notes)

8. **Background Python processes spawned by Agent subagents die when the worktree is reaped.**
   - Witnessed: Stream E Stage 2 query (PID 2868668) was killed mid-flight when the agent's session ended, even though started with `nohup ... &`. Lost ~10 min of work; had to re-run.
   - Workaround: long-running benchmarks should be launched from the controller's main session (which persists), not from inside an agent subagent.
   - Memory entry warranted? Yes — add to `~/.claude/projects/-home-krolik/memory/` as feedback.

9. **PR title commit-lint requires `^(?![A-Z]).+$` subject (lowercase first letter after colon).**
   - Witnessed: PRs #66, #67, #68 all initially failed the title check because subject started with "M7" (capital M). Fixed via `gh api -X PATCH` (gh pr edit was failing silently due to a GraphQL projects-classic deprecation warning).
   - Action: include explicit "lowercase first letter after colon" guidance in implementer prompts that ask for PR creation.

10. **Extract `X-Service-Secret` / `X-Internal-Service` header constants + `CheckServiceSecret` helper to middleware package.**
    - Currently 3rd-copy: `middleware/auth.go`, `middleware/ratelimit.go`, `server_routes.go (pprofHandler)`.
    - Per project rule "3rd near-duplicate of logic → extract".
    - Effort: 1h.
    - Surfaced by code review of PR #72.

15. **`InvalidateEdgesByMemoryID` only handles `from_id`, not `to_id`.**
    - Pre-existing bug: when a memory is deleted, edges WHERE `to_id = <deleted>` become orphans pointing nowhere.
    - M8 PR #83 (structural edges) materially amplifies edge volume (~30× more edges per memory) so the orphan accumulation is now meaningful.
    - Fix options: (a) extend `InvalidateEdgesByMemoryID` to handle both directions, (b) add periodic GC sweep.
    - Effort: S.
    - Surfaced by code review of PR #83.
