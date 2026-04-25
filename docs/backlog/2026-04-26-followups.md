# M7+M8 Follow-ups — 2026-04-26+ (refactored 2026-04-26 PM)

Items from M7 + M8 sprints not in scope for the same-day merge cycle. Status updated after M8 completion.

**Legend**: ✅ DONE = shipped in M7/M8. ⏸️ DEFERRED = scoped but not started. 🔬 M9-CANDIDATE = ranked for next sprint. ⚠️ NEEDS-DECISION = requires product/measurement input before scheduling.

---

## ✅ DONE in M7/M8 (5 items closed)

1. ✅ **Document `window_chars < 1024` latency cliff in `WindowChars` godoc.**
   - **Closed**: M8 PR #69 (S1 INFRA companion) — godoc updated with explicit latency numbers.

3. ✅ **Register `/debug/pprof/*` routes in `server_routes.go`.**
   - **Closed**: M8 PR #72 (S3 PPROF) — mounted behind `X-Service-Secret` via `isAuthExempt` + 6 unit tests + sub-route test for `/heap`.

4. ✅ **Batch embed calls per request in `extractFastMemories` path.**
   - **Closed**: M8 PR #71 (S2 EMBED-BATCH) — single `Embed([]texts)` call replaces N×embedSingle. Livepg shows window=512 latency 13s → 1.0s.

10. ✅ **Extract `X-Service-Secret` / `X-Internal-Service` header constants + `CheckServiceSecret` helper.**
    - **Closed**: M8 PR #78 (S5 CLEANUP) — promoted to exported `mw.CheckServiceSecret`, header constants in `middleware/auth.go`, all 3 callers updated.

7. ✅ **cat-5 adversarial F1 dropped 0.133 → 0.092 — investigate.**
   - **Closed**: M8 PR #85 (S7 CAT5) — original hypothesis WRONG (0% false positives). Real cause: 60% retrieval failures + 32% attribution suppressions (cross-speaker). The "regression" was sample-composition artifact (Stage 1 had 20% no-answer questions inflating partial credit). Real fix → Memobase-style dual-speaker retrieval (M9 candidate #11 below).

---

## 🔬 M9 CANDIDATES — ranked by ROI

### 🥇 Tier 1 — High lift, low effort (do first in M9)

11. **🆕 Dual-speaker retrieval in LoCoMo harness** (Memobase-derived)
    - Source: `docs/competitive/2026-04-26-memobase-deep-dive.md` Part 4 #1
    - What: modify `evaluation/locomo/query.py` to query BOTH `<conv>__speaker_a` and `<conv>__speaker_b` user stores, merge results in chat prompt. Pure harness change — no server diff.
    - Why: closes 32% attribution-suppression class (S7 finding) + matches what Memobase does to hit 75.78% LLM Judge.
    - Effort: **S** (~4h with parallelisation via asyncio).
    - Expected lift: cat-5 substantially; cat-1 + cat-4 modestly via wider evidence.

12. **🆕 LLM Judge metric in our harness** (apples-to-apples with Memobase/Mem0/Zep)
    - Source: `docs/competitive/2026-04-26-memobase-deep-dive.md` Part 4 #3
    - What: extend `evaluation/locomo/score.py` with `--llm-judge` flag — call Gemini Flash (cliproxyapi) to judge each prediction binary correct/incorrect alongside F1/EM/semsim. Cache by hash(question,prediction) to avoid recompute.
    - Why: lets us publish numbers comparable to public bar (75.78% Memobase vs our F1=0.238 are NOT comparable). Likely reveals we're closer to MemOS-tier than F1 suggested.
    - Effort: **S** (~4h, similar to existing scoring infra).
    - Expected outcome: higher published numbers without any model change — pure measurement honesty.

13. **🆕 `[mention DATE]` time-anchoring in extract prompt** (Memobase-derived)
    - Source: `docs/competitive/2026-04-26-memobase-deep-dive.md` Part 4 #2
    - What: modify our `add_fine.go` LLM extract prompt to require `[mention <ISO_DATE>]` tags on time-sensitive facts. Add instruction "use specific ISO dates, never relative". Verify tags propagate to chat prompt.
    - Why: cat-3 temporal — Memobase 85.05 is the public best, ours 0.201. This single trick is reportedly responsible for Memobase's temporal lead.
    - Effort: **M** (~1 day — prompt change + LoCoMo re-measurement on cat-3 only to verify).
    - Expected lift: cat-3 F1 should ≥ 0.30 (50% relative).

14. **🆕 Cat-5 exclusion flag in score.py** (honest dual-track reporting)
    - Source: `docs/competitive/2026-04-26-memobase-deep-dive.md` Part 4 #5
    - What: `--exclude-categories=5` flag in `evaluation/locomo/score.py`. Publish two numbers: "Overall (all cats)" + "Overall (cat-1-4 only, comparable to Memobase/Mem0/Zep)".
    - Why: Memobase explicitly excludes cat-5 (`memobase_search.py:147`); their 75.78 isn't directly comparable to our number which includes cat-5. This is pure measurement methodology, not gaming.
    - Effort: **S** (<1h).

### 🥈 Tier 2 — Cleanups + perf (M9 if time)

2. **A/B test `answer_style=factual` as default for QA workloads.**
   - Status: ⏸️ **Infrastructure ready** (M8 PR #80 S8 PRODUCT canary lives). Need 24h prod observation.
   - Action in M9: enable canary, capture latency p50/p95 + acceptance signal, decision GO/HOLD/ROLLBACK.
   - Effort: 1-2 days observation + analysis.

5. **Stage 3 — full 10-conv 1986-QA run (M8 retry).**
   - Status: ⏸️ DEFERRED (3-5h benchmark, infra ready: GOMEMLIMIT=4915MiB + recovery script). M8 attempt died on dozor restart mid-ingest.
   - Action: rerun in dedicated session. Script ready at `/tmp/m8-stage3-runner.sh`.

6. **cat-2 multi-hop F1 = 0.091 — D2 still under-firing.**
   - Status: 🟡 PARTIAL — M8 PR #81+#84 (S3 GRAPH v1 then v2) restored M7 parity (0.097) but didn't reach the 0.18 stretch gate. Remaining gap is **D3 reorganizer hub-and-spoke topology** (per S3 diagnosis doc).
   - Action: M9 dedicated diagnosis of D3 relation detector — does it produce CAUSES/SUPPORTS edges that D2 can traverse, or only consolidation edges that turn D2 into "all siblings of consolidator"?
   - Effort: 2-3 days.

8. ⏸️ **Background Python processes spawned by Agent subagents die when the worktree is reaped.**
   - Status: PARTIALLY MITIGATED — recovery_template.sh from M8 S1 INFRA has heartbeat + checkpoint files. M8 S6 MEASURE confirmed runner survives subagent death. BUT controller still must restart from main session for true durability.
   - Memory entry: `~/.claude/projects/-home-krolik/memory/feedback_set_e_recovery_scripts.md` ✅ already saved.

9. ⏸️ **PR title commit-lint requires `^(?![A-Z]).+$` subject.**
   - Status: ✅ workaround documented (use `gh api -X PATCH` directly, not `gh pr edit`).
   - Memory entry warranted? Actually banked already in feedback notes.

15. **`InvalidateEdgesByMemoryID` only handles `from_id`, not `to_id`.**
   - Effort: S (~1h).
   - Action: extend to handle both directions OR add periodic GC sweep. M8 S10 STRUCTURAL-EDGES amplified edge volume ~30× per memory so orphan accumulation is now meaningful.

16. **D7 + D11 share fanout pattern.**
   - Effort: M.
   - Action: when D12/D13 ships → extract `fanoutSubqueryToScope` helper. Project rule "3rd → extract" not yet hit (only 2 copies).

### 🥉 Tier 3 — go-code lifted patterns (M9-M10)

17. **Pre-compute CE rerank scores at ingest, persist in AGE Memory vertices**
    - Pattern source: go-code commits `520b3e9` + `417ed1b`.
    - Application: `internal/search/cross_encoder_rerank.go` currently fires per query (~100-400ms). Pre-compute pair-wise CE scores during D3 reorganizer (background), persist in `memos_graph."Memory".properties->>'ce_score_topk'`. Query-time CE → graph lookup.
    - Expected effect: -50-300ms p95 chat (compound with M7 factual -52% = up to 3-5× faster).
    - Effort: M.

18. **PageRank on `memory_edges`**
    - Pattern source: go-code commit `30373c9`.
    - Application: PageRank on memory_edges graph (background goroutine), store in `Memory.properties->>'pagerank'`, boost in D1 rerank.
    - Expected effect: cat-1 + cat-3 retrieval recall lift via better top-K ranking.
    - Effort: S.

19. **`BulkCopyInsert` / `CypherWriter` for AGE writes** (perf)
    - Pattern source: go-code commits `79b8791` + `a1adb38` — direct text-format COPY INTO AGE, bypass Cypher parser, `synchronous_commit=off`.
    - Application: heavy AGE write paths (Stage 3 ingest, D3 batch, S10 structural edges). 2-5× speedup.
    - Effort: M.

### Tier 4 — Architectural (M10+)

20. **🆕 Structured user profile layer** (Memobase-derived, BIG)
    - Source: `docs/competitive/2026-04-26-memobase-deep-dive.md` Part 4 #4
    - What: new first-class `user_profiles` table with `topic/sub_topic/memo` schema. LLM-extracted at ingest, queryable separately from raw memories. Port `extract_profile.py` + `merge_profile.py` + `pick_related_profiles.py` prompts.
    - Why: Memobase's structural advantage on cat-1 (single-hop) + cat-4 (open-domain) comes from this layer. Lookup `work/title` instead of cosine-search "what's user's job".
    - Effort: **XL** (~3-4 weeks — design doc, schema migration, pipeline rewrite, query API extension).
    - Schedule: own M-sprint (M10 or M11), not a follow-up item.

---

## Process improvements (still open)

- Memory bank discipline: M7 + M8 produced new `feedback_*.md` entries each sprint. Continue per `compound-sprint-orchestration` skill.
- Code review: at least one M8 PR (#81 S3 GRAPH v1) was merged "approve with condition" and DID regress prod. Lesson banked: empirical verification before merge for any change touching scoring/ranking.

---

## Notes for next-session controller

- Tier 1 items (#11-#14) compose: dual-speaker (#11) + LLM Judge (#12) + cat-5 exclusion (#14) lets us publish a Memobase-comparable number; (#13) lifts our cat-3 specifically.
- All four Tier 1 items together: ~2 days. Worth running as M9 sprint with same subagent-driven pattern as M7/M8.
- Stage 3 retry (#5) is the empirical gate that closes M8 properly. Should ship before declaring M8 "done".
- Major architectural items (#20 profile layer) need design-doc → brainstorm session BEFORE planning sprint.
