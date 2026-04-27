# M11 Final Execution Plan (v2 — supersedes prior two)

> **Status:** this is the EXECUTION plan. It supersedes priority ordering in:
> - `2026-04-27-m11-locomo-leadership.md` (initial plan, 18 streams)
> - `2026-04-27-m11-amendment-1-mem0-arxiv.md` (amendment with F8-F14)
>
> Prior docs remain as audit trail for the analysis behind these decisions. **This file is what TL dispatches against.**
>
> **Mission:** ship MemDB **≥ 78.0% LLM Judge** (excl-cat-5) on LoCoMo chat-50 stratified, putting MemDB above Memobase 75.78% AND Zep 75.14% — leaderboard leadership. Floor: 76.0%. Stretch: 80.0%.
>
> **Discipline:** every PR has a hard latency gate. Most M11 features have a tendency to *raise* p95 chat / p95 add. We refuse that drift.

---

## 1. Final scope (cherry-picked, latency-aware)

### Phase 1 — Refactor prerequisites (all kept)

| ID | Stream | Branch | Owner | Latency budget |
|---|---|---|---|---|
| **R0** | `llm.ChatStructured[T]` generic helper + 10 callsite migrations | `refactor/r0-llm-chat-structured` | Opus | parity (no-op) |
| **R1** | Decompose `nativeFineAddForCube` (584 LOC, cyc 18) | `refactor/r1-add-fine-decompose` | Opus | parity |
| **R2** | `SearchService.Search` → `[]stage` pipeline | `refactor/r2-search-pipeline-stages` | Opus | parity |
| **R3** | Extract `internal/search/rerank/` strategy package | `refactor/r3-rerank-strategy-pkg` | Sonnet | parity |
| **R4** | Extract `periodicLoop` primitive | `refactor/r4-scheduler-loops` | Sonnet | parity |

Acceptance for refactors: behavior parity verified by LoCoMo 3-conv subset (F1 within ±0.005). Latency parity: `memdb_*_duration_ms_p95` panels show no regression > 5%.

### Phase 2 — Quick wins (all kept)

| ID | Stream | Branch | Lift | Latency |
|---|---|---|---|---|
| **Q1** | Timeout retry on /search and /chat/complete (harness + memdb-go shim) | `feat/q1-locomo-timeout-retry` | **+3.0pp headline** | improves (recovers 140 lost Q) |
| **Q2** | LoCoMo 5-topic taxonomy switch | `feat/q2-locomo-taxonomy` | +0.7pp | parity |
| **Q3** | Rerank metadata boost (`+user_id +tags +session_id`) | `feat/q3-rerank-metadata-boost` | +0.3pp | parity |
| **Q4** | MMR diversification (bar=0.8) | `feat/q4-mmr-diversify` | +0.3pp | +1ms p95 |
| **Q5** | Top-8 missing Prometheus metrics | `feat/q5-metrics-coverage` | 0pp | parity |

### Phase 3 — Features (8 of 14 — cherry-picked)

| ID | Stream | Branch | Lift | Latency strategy |
|---|---|---|---|---|
| **F8** | **Atomic per-fact extraction (mem0 ADDITIVE_PROMPT)** | `feat/f8-atomic-fact-extraction` | **+5.5pp / +12-18pp cat-2** | extraction is async background; ingest p95 may rise + 30-50% (acceptable for cat-2 dominance) |
| **F9** | Recall budget tuning (top_k=30 per speaker, low threshold cat-2, reverse-role ingest) | `feat/f9-recall-budget` | +1.5pp / +4-7pp cat-2 | adds 2× DB query per Q (parallel); chat p95 +5-10% (gate) |
| **F11** | Bi-temporal edges (Graphiti / Zep) — `valid_at` + `invalidated_at` | `feat/f11-bi-temporal-edges` | +1.5pp / +3pp cat-2 | invalidator runs in background `periodicLoop`; sync write path **untouched** |
| **F12** | `linked_memory_ids` at extract-time | `feat/f12-linked-ids-extract` | +0.7pp / +2-3pp cat-2 | batch 10 facts/LLM-call; sync ingest +1 batch call per blob |
| **F14** | Personalized PageRank (HippoRAG 2) | `feat/f14-personalized-pagerank` | +1.2pp / +3pp cat-2 | TTL 5min cache; cache-miss falls back to global PR (no extra latency on cold) |
| **F2** | Reflection-loop deep search | `feat/f2-reflection-loop` | +1.0pp | env `MEMDB_REFLECTION_ON_COMPLEX_ONLY=true`; only complex queries (heuristic gate) |
| **F3** | Memobase event extraction + entry_summary | `feat/f3-event-extract` | +1.5pp | async background extractor; sync add path untouched |
| **F7** | Temporal-index (event_dates first-class) | `feat/f7-temporal-index` | +0.7pp / partial cat-2 | extract-time only; query-time GIN lookup is O(log N) |

### Dropped (re-evaluate M12)

- **F1 VEC_COT** — subsumed by F8 atomic facts; if F8 fails, fall back via env flag — no separate stream.
- **F4 buffer flush** — cost optimization, defer M12.
- **F5 dialogue rerank** — P2 polish, defer.
- **F6 RECREATE-ENHANCE** — P2 polish, defer.
- **F10 reverse-role separately** — folded into F9.
- **F13 paragraph+triples prompt** — conditional ship: only if M1 ablation reveals D2 cat-2 regression > 1pp.
- **RAPTOR / PRISM Selector / Mem0g write-path resolver** — defer M12.

### Phase 4 — Measure + Release

| ID | Stream | Branch |
|---|---|---|
| **M1** | Full-corpus Stage 5 + ablation matrix (15 runs, 38h wall) | `eval/m1-stage5-locomo` |
| **M2** | Publish v0.24.0 (changelog, release notes, GH release, dozor auto-deploy) | `release/v0.24.0` |

---

## 2. Latency budget — universal gate

**Baseline measured before R0 dispatch** (will be captured in Q5 metrics or via curl probes).

| Path | Pre-M11 baseline (M10) | M11 hard ceiling | Soft target |
|---|---|---|---|
| `/product/search` p95 | TBD (capture day 1) | +5% over baseline | parity |
| `/product/chat/complete` p95 | TBD | +10% over baseline | +5% |
| `/product/add` p95 (sync) | TBD | +30% over baseline | +15% |
| Add background fanout p99 | TBD | +50% over baseline (less critical) | +25% |
| LLM proxy p95 (`gemini-2.5-flash-lite`) | TBD | n/a (external) | n/a |

**Per-PR gate procedure:**
1. Subagent runs `evaluation/locomo/scripts/latency_probe.sh` (NEW — Q5 ships it) before pushing PR.
2. Latency results attached as PR comment.
3. Reviewer rejects PR if any p95 exceeds the hard ceiling.

---

## 3. Subagent dispatch contract (TL discipline)

Every subagent prompt MUST include this preflight block:

```
PREFLIGHT (verify before any code change):
1. `git status --short` — must be clean (worktree gives you this)
2. `git branch --show-current` — must NOT be `main` or `master`
3. Branch name must match the spec; create with `git switch -c <branch>` if missing

CONSTRAINTS:
- Stop at `gh pr create`. NEVER `gh pr merge`. NEVER push to main.
- File-size cap: ≤200 LOC target, ≤300 hard. No func >100 LOC / cyc >15 / >4 params.
- New code MUST go through `internal/llm.ChatStructured[T]` (post-R0). No new raw http.Client to LLM.
- Background work MUST go through `internal/scheduler.periodicLoop` (post-R4). No new bare goroutines.
- All new behaviors MUST emit metrics: `memdb.<package>.<verb>_total{outcome,...}` + duration histogram.
- LATENCY GATE: capture p95 chat/add via probe script before push; attach to PR.

REVIEW: stop after `gh pr create`. TL dispatches spec reviewer + quality reviewer separately.
```

---

## 4. Execution timeline (17 days)

| Day | Phase | Streams in flight (worktree IDs) |
|---|---|---|
| 1 | latency baseline + R0/R3 dispatch | R0 (worktree A), R3 (worktree B) parallel — disjoint files |
| 2 | R0 PR + 2-stage review; R1 dispatch | R1 (worktree C) |
| 3 | R0 + R3 merged; R1 PR review; R2 dispatch | R2 (worktree D); R4 (worktree E) parallel |
| 4 | R1 + R2 + R4 merged | Phase 1 complete |
| 5 | Phase 2 kick — Q1, Q2, Q5 dispatched | 3 worktrees parallel |
| 6 | Q1 PR (highest lift); Q3, Q4 dispatched | Q3 + Q4 depend on R3 ✓ |
| 7 | All Q PRs merged | Phase 2 complete |
| 8 | F8 dispatch (highest single lever, longest stream) | F11 dispatched in parallel (disjoint files) |
| 9 | F11 PR review; F9 dispatched; F14 dispatched | 3 worktrees parallel |
| 10 | F8 PR (rolling) + canary on staging cube | F12 dispatched (depends on F8 schema) |
| 11 | F8 + F11 + F14 merged | F2, F3, F7 dispatched (3 worktrees parallel) |
| 12 | F12 + F2 PR | F3, F7 review |
| 13 | All F PRs merged | Phase 3 complete |
| 14 | M1 ablation runs (38h wall — span days 14-15) | overnight + day 15 morning |
| 15 | M1 results in MILESTONES.md; M2 dispatched | release prep |
| 16 | v0.24.0 published; auto-deploy fires | post-release smoke |
| 17 | Show HN / Reddit / Discord launch artifacts | done |

---

## 5. Acceptance criteria (rollup)

| ID | Criterion | Verification |
|---|---|---|
| **M11.HEADLINE** | ≥ 78.0% LLM Judge excl-cat-5 chat-50 stratified | `evaluation/locomo/results/m11-stage5-final.json` |
| **M11.CAT2** | cat-2 full-corpus LLM Judge ≥ 45%; hit@k ≥ 55% | per_qa breakdown |
| **M11.CAT4** | cat-4 full-corpus ≥ 65% (F11 bi-temporal lift) | per_qa breakdown |
| **M11.INFRA** | LoCoMo run timeout count ≤ 30 (down from 140) | error field count |
| **M11.LATENCY** | p95 chat ≤ baseline + 10%; p95 add ≤ baseline + 30% | Prometheus panels |
| **M11.CODE-HEALTH** | `nativeFineAddForCube` ≤ 80 LOC; `Search` ≤ 60 LOC; ≤ 1 raw HTTP client outside `internal/llm/` | go-code measurement |
| **M11.METRICS** | All 8 missing series live; alerts wired | promtool check rules |
| **M11.RELEASE** | v0.24.0 published, dozor auto-deploy successful | gh release view |

If **M11.LATENCY** fails on any PR — that PR is reverted before next dispatch. No latency debt accumulates.

---

## 6. Risk register (consolidated, with mitigations)

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| R1 add-pipeline regression | medium | high | Worktree + livepg pack + 3-conv F1 parity | TL during review |
| F8 schema migration breaks prod cubes | medium | high | env flag `MEMDB_ATOMIC_FACTS=false` default; canary on staging cube; legacy `kind='paragraph_legacy'`; rollback = flip env | TL |
| F8 re-extraction cost (10 cubes × 60min) | high | medium | Re-extract on staging only; prod opt-in via API; not blocking M11.HEADLINE | TL |
| D2 multi-hop hurts cat-2 (mem0-Graph regression) | medium | medium | M1 ablation run #13 tests D2-OFF; if positive delta, default `MEMDB_DISABLE_D2_FOR_CAT2=true` | M1 reviewer |
| F11 bi-temporal over-invalidates | medium | medium | LLM judge confidence ≥ 0.7 to invalidate; reversible | F11 reviewer |
| F12 doubles ingest cost | high | medium | Bounded batch (10 facts/call) + admission-control semaphore | F12 reviewer |
| Compound discount > -8pp | medium | low | F8 alone clears Memobase; F8+F11 clears Zep; floor 76.0% holds | TL post-M1 |
| Latency drift on F2 + F9 + F14 stack | medium | high | Per-PR latency gate; F2 gated by complexity heuristic; F9 query-side parallelism; F14 cache | TL |
| Two implementers stomp shared file | low | high | Worktree isolation mandatory; phase ordering prevents cross-stream conflict | TL preflight |
| Release readiness slip | low | medium | M2 only blocked by M1 results; M1 has 38h budget on day 14-15 | M2 owner |

---

## 7. Decision log (executive)

- **2026-04-27 — F1 VEC_COT dropped from M11.** F8 atomic facts subsumes the recall mechanism; keeping F1 as a separate stream costs effort without compounding lift. If F8 fails staging, F1 is M12 fallback.
- **2026-04-27 — Buffer flush deferred.** Cost optimization, not headline lever. M12.
- **2026-04-27 — Headline target raised 76.0% → 78.0%.** Justified by F8 atomic-facts evidence (mem0 cat-2 paradox) + F11 ICLR-tier evidence.
- **2026-04-27 — Latency budget made hard.** Per-PR gate, no exceptions. Senior call: a fast-but-72% MemDB beats a slow-but-78% MemDB for production adoption.
- **2026-04-27 — F8 schema migration: legacy preservation mandatory.** No paragraph rows are deleted; new extraction lives alongside old until M11+1 cutover.

---

## 8. References

- Original plan (analysis): `2026-04-27-m11-locomo-leadership.md`
- Amendment 1 (mem0 + arxiv): `2026-04-27-m11-amendment-1-mem0-arxiv.md`
- M10 plan (foundation): `2026-04-27-m10-user-profiles-and-perf.md`
- LoCoMo MILESTONES: `evaluation/locomo/MILESTONES.md`
- mem0 source: `/home/krolik/src/compete-research/mem0`
- Zep source: `/home/krolik/src/compete-research/zep`
- MemOS source: `/home/krolik/src/compete-research/memos`
- Memobase source: `/home/krolik/src/compete-research/memobase`
- Cited papers: `arxiv.org/abs/2501.13956` (Graphiti), `arxiv.org/abs/2502.14802` (HippoRAG 2), `arxiv.org/abs/2504.19413` (Mem0g).
