# M12 Recovery — Root-Cause Analysis (M11 Full-Corpus Regression)

> **Mission**: explain why M11 full-corpus LLM Judge collapsed from M10's 50.9% (excl-5) → 41.5%, with cat-2 −15pp and cat-4 −10pp the dominant losses, despite hit@k being **stable to slightly up**.
>
> **Setup baseline**: M10 file `m10-stage4-final.json` (run sha `5029329e`, 142 errors) and M11 file `m11-full-judged.json` (sha `8aabc249`, 0 errors) — same 1986 QA, same `gemini-2.5-flash` judge, same dataset.
>
> **Reconciliation note**: the headline "72.5%" in M10 release notes refers to chat-50 stratified excl-5; the equivalent **full-corpus** figure is 50.9% excl-5 (29.0% cat-2). M11 full-corpus is 41.5% excl-5 (14.3% cat-2). The M10 vs M11 deltas in the brief are correct.

---

## 1. Wrong-Reason Distribution (cat-2 + cat-4, 20 random samples per cat)

Sample frame: random.seed=42, drawn from the 275 wrong cat-2 and 422 wrong cat-4 in `m11-full-judged.json::per_qa`. Programmatic classifier in `/tmp/m12-analysis/` — see `wrong_samples.txt` for the full sample text. Categories:

- **(a) retrieval miss** — chat said "memories do not contain" + `hit@k=0`
- **(b) date confab → today** — prediction stamps the conversation event as 2026-04-26..28 or 2025 when gold is 2022/2023
- **(c) gold present, chat refused** — `hit@k=1` but chat replied "memories do not contain"
- **(d) reasoning/format fail with context** — `hit@k=1`, chat gave a wrong answer drawing from the right doc
- **(e) other** — `hit@k=0`, no obvious "today leak"

| Reason | cat-2 (n=275 wrong) | cat-4 (n=422 wrong) | Severity |
|---|---|---|---|
| **(b) date confab → today** | **104 (37.8%)** | not classified separately (see Note) | **Critical, dominant cat-2** |
| (c) gold present, chat refused | 39 (14.2%) | **221 (52.4%)** | **Critical, dominant cat-4** |
| (a) retrieval miss | 79 (28.7%) | 78 (18.5%) | High |
| (d) reasoning/format fail | 18 (6.5%) | 95 (22.5%) | Medium |
| (e) other no-hit | 35 (12.7%) | 28 (6.6%) | Low |

**Whole-corpus quantification (programmatic, full populations, not the 20-sample)**:

- **42.7% of all cat-2 predictions** (137/321) leak "today" (2026 / 2025 / "April 2[5-8]") into the answer when the gold is 2022/2023.
- **26.2% of all cat-4 predictions** (220/841) had `hit@k=1` AND chat said "memories do not contain". The chat refused valid context outright.
- **12.1% of all cat-2 predictions** (39/321) had the same `hit@k=1` chat-refused pattern.

> Note on cat-4: the date-confab pattern overlaps with refusal in cat-4 (the chat says "memories do not contain… for *October 6, 2022*. They state Nate watched Inception on April 26, 2026" — see sample [2]). So 220 refusals + 95 reasoning fails captures most of cat-4 wrong; date-confab is implicit inside both and not double-counted.

### Concrete examples (worst offenders)

| # | Cat | Q | Gold | Pred | Issue |
|---|----|---|------|------|-------|
| 14 | 2 | When was John in Seattle for a game? | early August, 2023 | "John will be in Seattle for a game in May 2026." | Date-confab — model treated "next month" relative to today |
| 12 | 2 | When did Jon start to go to the gym? | March, 2023 | "Jon started going to the gym last week." | Verbatim parroting — model lost the absolute reference |
| 16 | 2 | When did Melanie buy the figurines? | 21 October 2023 | "Melanie bought the figurines on 2026-04-27." | Resolved "yesterday" against today |
| 5 | 4 | What did Calvin book a flight ticket for on 1st September 2023? | Boston | "memories do not contain... booked a flight to Boston 'last week' from April 26, 2026." | Refusal despite verbatim "Boston" + "I booked a flight" being retrieved |
| 12 | 4 | What did Nate share a photo of when mentioning unwinding at home? | a bookcase filled with dvds and movies | "memories do not state that Nate shared a photo... He mentioned playing video games or watching movies" | Refusal despite top-1 retrieved being the gold sentence |

These are not isolated. The pattern is mechanical and reproducible.

---

## 2. Stage-Suspect Ranking (ordered by evidence strength)

### S1 — **Harness `_build_dual_speaker_system_prompt` injects today's date as the per-memory ts prefix and as `Current time:`** *(definitive cause, ≥40% of cat-2 loss alone)*

**File**: `evaluation/locomo/query.py:760-803`

**Bug**:

```python
# line 783-784
ts = it.get("ts")
entry = f"{ts}: {content}" if ts else content
# line 788
now = time.strftime("%Y-%m-%d %H:%M (%A)")
# emitted into the prompt as "Current time: {now}"
```

The harness reads `ts` from `metadata.created_at` (`query.py::_extract_ts`, fallback chain `chat_time|created_at|created_time|time|date`). In LoCoMo:

1. `chat_time` is set per-message at ingest (`ingest.py:153`) to the conversation reference date — but this ends up on the *raw blob*, not on every extracted memory.
2. memdb-go's extractors stamp `created_at` = `time.Now()` on every memory row (`add_episodic.go:88`, `add_skill.go:173`, `add_tool.go:116`, `feedback_ops.go:240/319`).
3. So the harness sees `metadata.created_at = "2026-04-26"` on **every** retrieved memory.

**Quantified evidence (this analysis, not theory)**:
```
First 500 questions, retrieved memory ts-prefix distribution
M11: 9783 memories with ts="2026-04..", 217 with ts=""
M10: 0 memories with ts (the F9 ts-prefix block did not exist)
```

The `ts:` prefix block was added by **PR #155** (F9, commit `81e5ee47`, 2026-04-27) and exists ONLY in M11. M10's harness emitted bare `f"{i}. {content}"`. The factual server-prompt template at `chat_prompt_tpl.go:107` *also* injects `time.Now()` as "Current Time: %s (Baseline for freshness)", but in M10 this had no per-memory contradicting `ts:` prefix to amplify it. In M11 every memory line contradicts itself ("2026-04-26: last week I started lifting weights one year ago" — the LLM has no way to know "today" is the conversation's today, not the prompt's today).

**Direct effect**: 137/321 = **42.7% of cat-2 wrong predictions are date-confab to today** (anchored on "April 2026" / "2025" / "next/last week from today").

Test/measurement: `evaluation/locomo/tests/test_f9_recall_budget.py:93-94` — pins the ts-prefix behaviour. The bug is encoded as expected behaviour.

### S2 — **Factual server-prompt is too refusal-eager** *(dominant cause of cat-4 −10pp)*

**File**: `memdb-go/internal/handlers/chat_prompt_tpl.go:103-126` (`factualQAPromptEN`)

**Mechanism**: rule 6: `If no memory supports an answer, reply exactly: no answer`. Combined with rule 1 ("SHORTEST factual phrase") and rule 4 ("bare name only"), the model errs on the side of refusal. **220/841 = 26.2% of cat-4 predictions had `hit@k=1` AND model said "memories do not contain"** — refusing on perfectly good evidence. cat-2 same pattern: 39/321 = 12.1%.

This prompt was authored M7 (`c57cc904`, 2026-04-23, server-side answer_style=factual). It survived to M10. What changed in M11 that pushes cat-4 from 60% → 50%?

Two compounding causes upstream:
1. **The `ts:` prefix** (S1) makes 100% of memory lines look like they are dated 2026-04-26 — when the model sees a 2023-dated question and every memory is "2026-04-26", it concludes the memories are off-topic and refuses.
2. **F12 linked_expand + D2 graph_expand + F3 inject_events** dilute the top-K with semantically adjacent but off-question rows. Combined with adaptive rerank (`rerankClusteredSpread=0.05`, `rerank_gate.go:12`) the noisy bottom of the list is more likely to win when scores cluster — and `top_k_per_speaker=30` (F9 doubled the budget vs M10's 20) pushes the spread further into "clustered" territory where LLMJudge fires on dilute candidates.

**Why hit@k went UP but quality went DOWN**: top-k=30 finds the gold doc more often (cat-4 hit@k 80.9 → 83.5), but the prompt sees 30 candidates with identical fake `2026-04-26:` prefixes and refuses. Hit@k measures retrieval; quality is harness-prompt × server-prompt × candidate-pool.

### S3 — **F9 reverse-role ingest doubles fact density per cube → admission control / PPR / D2 indirectly noisier**

**File**: `evaluation/locomo/ingest.py:191-208` (`ingest()` with `reverse_role=True` default).

Each conversation is ingested twice, once with A/B swapped. Doubles raw memory rows. Doesn't itself confabulate dates, but doubles the candidate pool size — every retrieval surfaces near-duplicates from both ingest passes that the rerank cannot tell apart, increasing the "clustered" cohort hitting LLMJudge. Possibly contributes ≤2pp of S2's amplification. Verifiable by ablation (`reverse_role=false`).

### S4 — **D10 answer_enhance and F2 reflection prepend synthetic LLM answers to the candidate pool**

**File**: `memdb-go/internal/search/answer_enhance.go:196-236` (`applyAnswerEnhancement` → `prependEnhancedAnswer`); `pipeline_reflect.go:96`.

When `MEMDB_SEARCH_ENHANCE=true` (default unknown — verify env), the LLM produces a synthetic top-1 candidate with `relativity=0.99`. This makes `rerankStrategy()` see `topRel > 0.93 = high-confidence` (line 34) → skips LLMJudge → no rescue on bad cosine ordering. Either skip-good-rescue or inject-bad-fact, depending. **Impact: secondary; needs ablation to size.**

### S5 — **F11 bi-temporal validator** *(non-issue, ruled out)*

DB query: `SELECT count(*) FROM entity_edges WHERE invalid_at IS NOT NULL` returns **0**. Validator was a no-op during the M11 run. Drop from suspect list.

### S6 — **F8 atomic facts** *(non-issue, ruled out)*

DB query: `SELECT properties::text::jsonb->>'kind' FROM memos_graph."Memory" WHERE properties::text::jsonb->>'user_id' LIKE 'conv-%' GROUP BY 1` returns 13047 rows with `kind=NULL` — no `atomic_fact` rows. Confirms M11 ingest was raw mode, F8 default-OFF held. Drop from suspect list.

### S7 — **PR #167 / agtype migration regression** *(ruled out, fixed pre-run)*

PR #167 merged at 2026-04-28 11:45 UTC. M11 prediction file mtime is 22:32 UTC. Indexes from PR #169 (`8aabc249`) commit time is 20:36 UTC — predictions ran from this exact commit. So the F8/F11/F12/F7/F3/F14 silent-disable window did NOT affect this prediction set. Per PR #167 body, that bug already cost an earlier M11 run; this run is post-fix.

### Suspect ranking (final)

| # | Stage | Confidence | Estimated cat-2 loss | Estimated cat-4 loss |
|---|---|---|---|---|
| **S1** | F9 harness `ts:` prefix + `Current time` poisoning | **HIGH (definitive)** | -8 to -12pp | -3 to -5pp (compounds with S2) |
| **S2** | Factual server-prompt over-refusal | **HIGH (definitive)** | -2 to -4pp | -7 to -10pp |
| S3 | F9 reverse-role ingest noise | medium | -1 to -2pp | -1 to -2pp |
| S4 | D10/F2 synthetic candidate top-rank | low | -0 to -1pp | -0 to -1pp |
| S5 | F11 validator | ruled out | 0 | 0 |
| S6 | F8 atomic facts | ruled out | 0 | 0 |
| S7 | PR #167 silent disable | ruled out (pre-run fix) | 0 | 0 |

**S1 + S2 alone explain ~−12pp of −15pp cat-2 and ~−8pp of −10pp cat-4**. The remaining few pp are compound effects of S3 + S4 in the long tail.

---

## 3. Concrete Ablation Runbook (env-var driven)

All flags below are read at process start. Required deploy cycle: `docker compose up -d --no-deps --force-recreate memdb-go`. Run via `evaluation/locomo/run.sh` smoke (300-Q stratified, ~25 min) before full corpus.

| Run | Hypothesis | Setting | Expected delta vs M11 baseline (excl-5 LLM Judge) |
|---|---|---|---|
| A0 | M11 baseline pinned | (no env changes) | 41.5% |
| A1 | **Drop ts prefix from harness prompt** | patch `query.py:784` to always `entry = content`; rerun | **+5 to +8pp on cat-2**, +1 to +2pp cat-4 |
| A2 | **Drop "Current time:" line** from harness prompt | patch `query.py:797` to omit it | +1 to +2pp cat-2 (after A1, marginal) |
| A3 | **Replace `Current time:` with conversation date** (real fix) | thread the conv's max `chat_time` through `query_chat_dual` | +6 to +10pp cat-2, +2pp cat-4 (best-case) |
| A4 | **Soften factual prompt rule 6** | server: change "no answer" to "answer with the closest date phrase from the memories, even if approximate" | +5 to +8pp cat-4, +1 to +3pp cat-2 |
| A5 | Disable F9 reverse-role ingest | `ingest.py --reverse-role=false`, full re-ingest | -1pp expected (recall drops) but reveals S3 magnitude |
| A6 | Disable F12 linked_expand | `MEMDB_F12_LINKED=false` | mostly parity; isolates F12 cost |
| A7 | Disable F3 event inject | `MEMDB_F3_EVENTS=false` | parity to slight + on cat-3 (already +4pp) |
| A8 | Disable F7 temporal augment | `MEMDB_F7_TEMPORAL=false` | -2pp cat-3 (F7 was the only winner — keep) |
| A9 | Disable D2 graph expand | `MEMDB_SEARCH_MULTIHOP=false` | parity to +1pp; isolates D2 noise |
| A10 | Disable D10 answer_enhance | `MEMDB_SEARCH_ENHANCE=false` | reveals S4 magnitude |
| A11 | Tighten cat-2 threshold | `MEMDB_CAT2_THRESHOLD=0.20` (was 0.05) | recall drops 1-2pp but rerank pool cleaner |
| A12 | Disable F2 reflection | `MEMDB_F2_REFLECTION=false` | parity (gate is default-only-on-complex) |
| A13 | Tighten rerank wide-spread | edit `rerankWideSpread=0.20` (was 0.25) | +1pp cat-2 (more queries get rerank) |
| A14 | **Combo**: A3 + A4 | both fixes + smoke | **best-case M12 floor: 51-54% excl-5** |
| A15 | A14 + tighten top_k_per_speaker | `top_k_per_speaker=20` (was 30) | parity to slight + (less dilution) |

Subagent dispatch contract: each ablation is a single-PR patch + a single smoke + posted Prometheus latency probe (Q5 metrics). `latency_probe.sh` was promised in the M11 plan — verify it shipped or build it as A0 prep.

---

## 4. New Metrics Required (currently invisible to operators)

These should ship as part of M12 stream "Q-obs" and be emitted from memdb-go on the chat path (and harness on the eval path).

### Server-side (memdb-go, Prometheus)

```
# Histogram — how many tokens does the chat see?
memdb_chat_context_tokens{cube,user,answer_style} histogram
  buckets: 256, 512, 1024, 2048, 4096, 8192

# Counter — how many candidates carried a synthetic ts that does NOT match conv reference time?
memdb_chat_ts_mismatch_total{cube,answer_style}

# Histogram — fraction of retrieved candidates whose `ts` is post-conv-end-date
memdb_chat_irrelevant_ts_ratio{cube} summary

# Counter — how often did LLMJudge fire AND change the top-1 vs cosine ordering?
memdb_search_rerank_judge_changed_top1_total{outcome="agree|swap|reject_all"}

# Counter — chat refusal: model said "no answer" / "do not contain" while hit@k>0
memdb_chat_refused_with_evidence_total{category,answer_style}
  # Requires harness to POST per-question category back as a header — server tags

# Gauge — F11 invalidator effects per cube
memdb_db_invalidated_edges{cube_id} gauge

# Histogram — D2 graph_expand candidate count added per query
memdb_search_d2_added_candidates{cube} histogram
  buckets: 0, 5, 10, 20, 50, 100

# Counter — D10 enhance outcome
memdb_search_d10_enhance_outcome_total{outcome="answered|unknown|skipped|error"}
  # already partially exists — verify all 4 outcomes are wired
```

### Harness-side (`evaluation/locomo/query.py`, log + final summary)

```
locomo_chat_today_leak_count       — predictions that referenced "April 2026" / "2025" when gold was pre-2024
locomo_chat_refused_count          — predictions matching r"do not (contain|state|mention)"
locomo_chat_refused_with_hit_count — same as above AND hit_at_k > 0  (the smoking-gun signal)
locomo_ts_prefix_emit_rate         — fraction of memory lines where ts != "" was emitted
```

The harness already prints retry stats; bolt these onto the same `print_summary()` (`query.py:204`-ish).

### Alert rules (M12 sprint deliverable)

```
- alert: ChatRefusalSpike
  expr: rate(memdb_chat_refused_with_evidence_total[10m]) / rate(memdb_chat_total[10m]) > 0.15
  for: 30m
  severity: page
- alert: ContextTimestampDrift
  expr: avg_over_time(memdb_chat_irrelevant_ts_ratio[5m]) > 0.4
  for: 30m
  severity: ticket
```

---

## 5. M12 Sprint Scope Sketch (5–8 streams, ROI-ordered)

| # | Stream | Effort | Impact (pp on excl-5) | ROI rank | Notes |
|---|---|---|---|---|---|
| **M12.1** | **Harness fix: replace `Current time:` and per-memory `ts:` prefix with conversation reference time** | S (1 day) | **+8 to +12pp** | **★★★★★** | Thread `iso_date` from ingest into search/chat call (`query.py:806-`). Use `max(chat_time)` of retrieved memories as the per-conv "now" in the prompt. Drop the `ts:` prefix (or, better, replace the value with the conv's `chat_time` from memory metadata when present). Single-PR. |
| **M12.2** | **Server prompt softening: rule-6 fallback + dual-time anchor** | S (1 day) | **+5 to +8pp** | **★★★★★** | Edit `factualQAPromptEN`: replace rule 6 with "If no memory supports an answer, reply with the closest date phrase or entity present in the memories, even if approximate. Only reply 'no answer' when the topic itself is absent." Add explicit rule 0: "The 'Current Time' header refers to your processing time, not when memories were created. When memories use 'last week / yesterday', resolve them against the dated context inside each memory, not against Current Time." |
| M12.3 | Pipe `chat_time` through extractor → `created_at` → `metadata.created_at` survives extraction so the harness `_extract_ts` returns the real conversation date | M (2-3 days) | +3 to +5pp (durable; complements M12.1) | ★★★★ | Touch: `add_fine_*.go`, `add_skill.go`, fact extractor. Migration to backfill existing rows from session metadata. Larger blast radius — needs careful PR. |
| M12.4 | Ablation matrix runs (15 LoCoMo-300 smokes + 1 LoCoMo-full final) | M (2 days wall, ~2 days API) | discovery — sizes S3/S4/F12/F3 magnitudes | ★★★★ | `evaluation/locomo/scripts/ablation.sh` driver. Mandatory before M12.5/6 land — kill streams that don't pay. |
| M12.5 | New observability: 5 server metrics + 4 harness metrics + 2 alerts (Section 4 above) | M (2 days) | 0pp directly; saves 5+ days on M13 RCA | ★★★★ | Wire into existing `kitmetrics`/Prometheus. Alerts go to dozor. |
| M12.6 | Disable / re-tune F9 reverse-role ingest if A5 ablation shows ≥0pp loss | S (0.5 day) | +1 to +2pp (if positive) | ★★★ | Half the ingest cost as a free side-effect. |
| M12.7 | LLMJudge "evidence-aware" rerank gate: when adaptive rerank fires, also bias candidates by `ts ∈ [conv_start, conv_end]` matching | M (3 days) | +2 to +3pp | ★★★ | Edit `rerank_gate.go` + new `tsConsistencyBoost` stage. Requires M12.3 first (real `created_at` on memories). |
| M12.8 | Re-baseline M12 + ship v0.24.1 | S (0.5 day) | 0pp; locks in gains | ★★ | LoCoMo full corpus + chat-50 stratified, refresh README badge, GitHub Release notes, dozor auto-deploy. |

**Aggregate target**: M12.1 + M12.2 + M12.4 + M12.5 alone should clear **52-56% excl-5** (vs 50.9% M10, 41.5% M11). Add M12.3 + M12.6 + M12.7 to push toward **60-65% excl-5**, which is a net positive vs M10 baseline. The original 78% M11 stretch goal is M13+ work and should be re-scoped after M12 lands and reveals the next bottleneck.

**Critical**: **do M12.4 (ablations) BEFORE M12.7 / 6**. The current sprint failed precisely because we shipped 14 streams compounded without per-stream measurement. We don't repeat that.

---

## 6. Exact go-code MCP traces used to ground this analysis

For audit:
- `mcp__go-code__symbol_search buildSystemPrompt` → confirmed `time.Now()` injection at `chat_prompt.go:52`
- `mcp__go-code__symbol_search defaultStages` → confirmed 22-stage pipeline order in `service.go:134`
- `mcp__go-code__understand stageTemporalAugment / stageInjectEvents / stageLinkedExpand / stageD2GraphExpand` → confirmed each stage's call shape, scoped to merged-pool inputs (none rewrite per-memory `ts`)
- `mcp__go-code__symbol_search rerankStrategy / applyAnswerEnhancement / runIterativeExpansion` → confirmed gate thresholds (`rerankTopCosineThreshold=0.93`, `rerankClusteredSpread=0.05`)
- `mcp__go-code__symbol_search applyCat2Threshold + cat2QueryRe` → confirmed regex covers all "When/How long/How many years..." but NOT "Where", "What kind", etc. cat-2 questions like sample [11] q=72 ("How does John plan...") get the standard threshold and bleed into the rerank pool noisily.
- DB live queries:
  - `entity_edges WHERE invalid_at IS NOT NULL` → 0 (F11 ruled out)
  - `Memory.properties.kind` group-by → all NULL (F8 ruled out)
- PR review: #167 body confirms the silent-disable window closed 11:45 UTC on 2026-04-28, before the 22:32 UTC predictions file mtime. PR #169 ran with hot-path indexes already applied.

---

## 7. Postmortem signals (for memory + future M-sprints)

1. **Prompt-engineering bugs are invisible to hit@k.** Retrieval was healthy across the board; the regression came entirely from how candidates were rendered into the chat prompt. M12.5 metrics directly target this blind spot.
2. **Compound feature shipping without per-feature ablation cost ~10pp this sprint.** M11 plan's "Phase 4 — M1 ablation matrix (15 runs, 38h wall)" was scoped but apparently the regression analysis hit before those runs landed. Future protocol: ablation BEFORE merge to main, not after the headline run.
3. **Date-sensitive harnesses are landmine territory.** LoCoMo's 2022-2023 dates clash with our 2026-04 ingest stamp in ways the test corpus won't catch. Any benchmark with relative time references needs the real conversation date threaded end-to-end. This applies to the next datasets (LongMemEval, MSC) we plan to add.
4. **`time.Now()` in any prompt template is an anti-pattern for offline / replay benchmarks.** Add a server-side env `MEMDB_PROMPT_REFERENCE_TIME` to override; defaults to `time.Now()` for production but lets benchmarks pin the value.

---

## 8. Backlog observations (not in M12 scope, surface for triage)

- `Noted`: `factualQAPromptZH` rule 6 keeps literal English "no answer" — comment confirms intent, but for non-LoCoMo Chinese consumers this leaks English into otherwise-Chinese output. (`chat_prompt_tpl.go:155`)
- `Noted`: `ingest.py:153` writes `chat_time` to message, but no path forwards it to memory metadata so it survives extraction. Migration cost for M12.3.
- `Noted`: `cat2QueryRe` pattern only matches cat-2 questions starting with literal "when/how long/etc." — questions like "What did Audrey buy in November 2023?" are temporal-sensitive but get the default threshold.
- `Noted`: `pipeline_finalize.go:18` `stageFormatItems` is the obvious hook to add per-memory ts-validation, but currently silent on `ts` semantics — single source of truth for Section 4 metric `memdb_chat_irrelevant_ts_ratio`.
- `Noted`: `rerank_gate.go` constants are baked at compile time — not env-tunable. Blocks A13 ablation. Make `rerankClusteredSpread` / `rerankWideSpread` / `rerankTopCosineThreshold` env-overridable for ablation runs.
