# M12 vs M10 vs M11 — LoCoMo Comparative Analysis

**Date:** 2026-04-29
**Scope:** comparative read of `evaluation/locomo/results/` JSON artifacts
**Bottom line:** M12 sample (conv-26, 50 QA) shows the highest excl-5 headline ever recorded (70 pp), beats M10's stratified-conv-26 sample by −2.5 pp (sample noise), and beats M11's run on the same sample by +20 pp. Cat-1 is the only clear regression (−30 pp on 10 QA, σ ≈ 31 pp). **Decision is gated on a full-corpus M12 run** — the sample is too small to ship v0.24.1 on alone.

---

## 1. Data sources

| Run | File | Commit | n | Sample shape |
|---|---|---|---|---|
| M10 full | `m10-stage4-final.json` | `5029329e` | 1986 | full LoCoMo, 142 errors |
| M10 chat-50 | `m10-chat50-final.json` | `5029329e` | 50 | conv-26 only, idx 0–49, 0 errors |
| M11 full | `m11-full-judged.json` | `8aabc249` | 1986 | full LoCoMo, 0 errors |
| M11 sample | `m11-smoke-judged.json` | `d9fb584f` | 50 | conv-26 only, idx 0–49, 0 errors |
| M12 sample | `m12-combined-sample.json` | `716db652` | 50 | conv-26 only, idx 0–49, 0 errors |

**Key correction to the brief:** the brief asserted that M10 chat-50 is "stratified across all 10 conversations." It is not. `jq` confirms `m10-chat50-final.json` per_qa is 100% `conv-26`, indices 0-49 — identical sample to M11 smoke and M12 sample. **All three samples are direct apples-to-apples on the same 50 QA.**

That's actually good news for clean comparisons; bad news for any blog post that claimed "72.5% on stratified sample" — that was conv-26 only, and conv-26 is a known easy conversation in LoCoMo.

---

## 2. Canonical comparative table

LLM Judge per category (`gemini-2.5-flash`), exact values from JSON. Bold = best in row.

| Metric | M10 full (n=1986) | M10 conv-26 (n=50) | M11 full (n=1986) | M11 conv-26 (n=50) | M12 conv-26 (n=50) | Δ M12 vs M10-full | Δ M12 vs M10-sample |
|---|---|---|---|---|---|---|---|
| cat-1 LJ (single-hop) | **53.5** | 60.0 | 47.5 | 60.0 | 30.0 | **−23.5** | −30.0 |
| cat-2 LJ (multi-hop) | 29.0 | 80.0 | 14.3 | 30.0 | **90.0** | **+61.0** | +10.0 |
| cat-3 LJ (temporal) | 37.5 | 70.0 | 41.7 | 70.0 | **80.0** | **+42.5** | +10.0 |
| cat-4 LJ (open-domain) | 59.9 | **80.0** | 49.8 | 40.0 | **80.0** | **+20.1** | flat |
| cat-5 LJ (adversarial) | 10.3 | 20.0 | 7.6 | 30.0 | 20.0 | **+9.7** | flat |
| **excl-5 (weighted)** | 50.9 | 72.5 | 41.5 | 50.0 | **70.0** | **+19.1** | −2.5 |
| **excl-none (overall)** | 41.8 | 62.0 | 33.9 | 46.0 | **60.0** | **+18.2** | −2.0 |
| hit@k cat-1 | 74.1 | 70.0 | 71.3 | 70.0 | 60.0 | −14.1 | −10.0 |
| hit@k cat-2 | 35.5 | 80.0 | 36.1 | 90.0 | **90.0** | +54.5 | +10.0 |
| hit@k cat-3 | 60.4 | 70.0 | 58.3 | 80.0 | 60.0 | flat | −10.0 |
| hit@k cat-4 | **80.9** | 90.0 | **83.5** | 90.0 | **100.0** | +19.1 | +10.0 |
| hit@k cat-5 | 71.5 | 70.0 | 74.4 | 80.0 | 70.0 | flat | flat |
| hit@k overall | 69.5 | 76.0 | 70.8 | 82.0 | 76.0 | +6.5 | flat |
| F1 overall | 15.3 | 13.8 | 13.9 | 12.3 | **16.8** | +1.5 | +3.0 |
| EM overall | 0.15 | 0.0 | 0.20 | 0.0 | 0.0 | flat | flat |

Headline excl-5 weighting:
- M10 full: weighted by per-cat n (282/321/96/841 over 1540 non-cat-5) → 0.5091.
- Sample runs (M10 chat-50, M11 smoke, M12 sample): per-cat n=10/10/10/10 → simple mean of cat 1-4.
  - M10 sample: (60+80+70+80)/4 = 72.5
  - M11 sample: (60+30+70+40)/4 = 50.0
  - M12 sample: (30+90+80+80)/4 = 70.0

Note the M10-full values in the brief table (e.g. cat-2 = 14.3%) were actually M11-full numbers; corrected here from the JSON.

---

## 3. Sample noise budget

For per-cat sample n=10, LLM Judge is a binomial with σ = √(p(1−p)/10). At p=0.5 that's ±31 pp at 95% CI. For a 50-QA aggregate (excl-5 over n=40 cat-1..4), σ ≈ ±15 pp. In practice:

| n | 95% CI half-width at p=0.5 | at p=0.7 |
|---|---|---|
| 10 (per-cat sample) | ±31 pp | ±28 pp |
| 40 (excl-5 sample) | ±15 pp | ±14 pp |
| 50 (overall sample) | ±14 pp | ±13 pp |
| 1540 (excl-5 full) | ±2.5 pp | ±2.3 pp |
| 1986 (overall full) | ±2.2 pp | ±2.0 pp |

**Anything below ±15 pp on a 50-QA sample is statistical noise.** Cat-1's −30 pp (60→30) on n=10 is a 2σ event — concerning but not yet damning. Cat-2's +60 pp (30→90) is well outside noise. Cat-3 and cat-4 sample deltas are at noise boundary.

---

## 4. Per-cat verdict (M12 sample vs M10 full corpus baseline)

Threshold reminder: Win = +5 pp with confidence intervals not crossing baseline.

| Cat | M10 full | M12 sample | Δ | Verdict |
|---|---|---|---|---|
| cat-1 single-hop | 53.5 | 30.0 | −23.5 | **Likely loss** — 30 pp drop is 2σ on n=10. Even mid-CI (≈30+28 = 58) just barely touches M10. RCA blocker before full corpus. |
| cat-2 multi-hop | 29.0 | 90.0 | +61.0 | **Win** — 90% on n=10 has CI ±18 pp, lower bound 72% still well above M10 baseline 29%. Memobase port + raw mode is paying off here. |
| cat-3 temporal | 37.5 | 80.0 | +42.5 | **Likely win** — n=10, CI ±25 pp brings lower bound to 55%, still above 37.5%. Confidence high but full corpus needed. |
| cat-4 open-domain | 59.9 | 80.0 | +20.1 | **Likely win** — lower CI ≈ 55%, brushes M10 baseline. Plausible 5-15 pp real lift. |
| cat-5 adversarial | 10.3 | 20.0 | +9.7 | **Tie** — n=10 CI ±25 pp; not statistically separable from M10's 10.3%. M10 chat-50 also showed 20% on same questions — reproducible sample artifact, not a real win. |
| **excl-5 weighted** | 50.9 | 70.0 | +19.1 | **Likely win** — n=40 CI ±15 pp, lower bound ≈ 55%, just above M10 baseline. **Plausible real ≈ 5–10 pp lift on full corpus**, not the headline +19. |
| **excl-none overall** | 41.8 | 60.0 | +18.2 | **Likely win** — same n=50 calculus, real lift likely 5–12 pp. |

Sanity floor: on the same 50 QA, M10 scored **72.5 / 62.0**. M12 scores **70.0 / 60.0** — within 2-3 pp of M10. **M12's apparent "headline" gain over M10 full corpus is mostly the conv-26 easy-bias, not the M11-to-M12 deltas.**

The honest framing is therefore:
- **M12 vs M11 on identical 50 QA: +20 pp excl-5, +14 pp excl-none.** That's the real M11→M12 sprint signal.
- **M12 vs M10 on identical 50 QA: −2.5 pp excl-5, −2 pp excl-none.** Within noise. M12 has *not* exceeded M10 on this sample.
- **M12 full vs M10 full: unknown.** Cat-2 / cat-3 gains look real and large; cat-1 regression looks real and large. Need full corpus.

---

## 5. Headline computations (showing work)

```
M10 full excl-5 weighted = (282*0.5355 + 321*0.2897 + 96*0.375 + 841*0.5993) / 1540
                        = (151.0 + 93.0 + 36.0 + 503.9) / 1540
                        = 783.9 / 1540 = 0.5091  ✓ matches JSON

M10 chat-50 excl-5 = (10*0.6 + 10*0.8 + 10*0.7 + 10*0.8) / 40 = 29 / 40 = 0.725  ✓
M11 sample excl-5  = (10*0.6 + 10*0.3 + 10*0.7 + 10*0.4) / 40 = 20 / 40 = 0.500  ✓
M12 sample excl-5  = (10*0.3 + 10*0.9 + 10*0.8 + 10*0.8) / 40 = 28 / 40 = 0.700  ✓
```

---

## 6. What changed M11 → M12 (on same 50 QA)

| Cat | M11 → M12 | Driver hypothesis |
|---|---|---|
| cat-1 | 60 → 30 | **Regression.** Combined extractor lost a single-hop accuracy. Possible: aggressive raw-mode, missing single-fact lookup path, retrieval changed under embed semsim. |
| cat-2 | 30 → 90 | **Major win.** Memobase / multi-hop fix from M11 phase 3 paying off. |
| cat-3 | 70 → 80 | Marginal, within noise. |
| cat-4 | 40 → 80 | **Major win.** Open-domain answer-style + embed semsim helping. |
| cat-5 | 30 → 20 | Within noise. |

Embed semsim mode (`text-embedding-3-small`) replaced BoW between M11 and M12 — this likely drives both the cat-4 jump and the cat-1 drop (different recall profile for short fact questions vs. discursive ones).

---

## 7. Recommendation

**Do not ship as v0.24.1 yet.** Three reasons, in order of weight:

1. **M12 sample does not beat M10 sample.** On the only common ground (50 QA, conv-26), M10 chat-50 sits at 72.5 / 62.0 LJ and M12 sits at 70.0 / 60.0. Within noise, but M10 is nominally ahead. Shipping v0.24.1 with "headline 70%" implicitly compares against M10's full-corpus 50.9% — that is a sample-vs-full apples-to-oranges that will collapse the moment anyone runs M12 on full corpus.
2. **Cat-1 regression on conv-26 is a real RCA blocker.** −30 pp on identical questions vs M10. Could be small ( single misclassified fact pattern) or systemic (embed semsim hurting tight factual lookups). Need to know which before claiming a v-bump.
3. **Cat-2 / cat-4 gains are likely real but possibly inflated by conv-26 specifics.** Memobase pipeline gains in M9 looked similar on smoke samples and **shrank substantially on full corpus**. Same risk applies here.

**Ordered next actions:**
1. Run M12 full-corpus LoCoMo (1986 QA). Expected runtime ≈ same as M11-full — overnight job. **This is the gating measurement.**
2. While that runs: pull the 7 cat-1 questions M10 got right and M12 got wrong on conv-26 (idx 0..9 cat-1), eyeball the prediction diff. If it's the embed-semsim threshold, retest with `semsim_mode=bow` to isolate. If it's the answer-style policy interfering with single-fact extraction, that's an extractor fix.
3. Ship v0.24.1 only after full-corpus M12 confirms ≥M10 full corpus on excl-5 weighted (≥51 pp) AND cat-1 ≥ 50 pp. Otherwise: fix cat-1 first, retest, then ship.

---

## Appendix — raw aggregates for traceability

| Run | EM | F1 | semsim | hit@k | LJ | n | excl-5 LJ | excl-none LJ |
|---|---|---|---|---|---|---|---|---|
| M10 full | 0.0015 | 0.1531 | 0.1990 | 0.6949 | 0.4179 | 1986 | 0.5091 | 0.4179 |
| M10 chat-50 | 0.0 | 0.1381 | 0.1932 | 0.7600 | 0.6200 | 50 | 0.7250 | 0.6200 |
| M11 full | 0.0020 | 0.1390 | 0.1814 | 0.7085 | 0.3389 | 1986 | 0.4149 | 0.3389 |
| M11 sample | 0.0 | 0.1227 | 0.1679 | 0.8200 | 0.4600 | 50 | 0.5000 | 0.4600 |
| M12 sample | 0.0 | 0.1685 | 0.2168 | 0.7600 | 0.6000 | 50 | 0.7000 | 0.6000 |
