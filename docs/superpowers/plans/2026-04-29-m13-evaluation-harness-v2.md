# M13 — LoCoMo Evaluation Harness v2.0 (Industrial-Grade Methodology)

> **Mission**: Transform `evaluation/locomo/` from a working benchmark scorer into a **publication-quality evaluation framework** comparable to academic peer review standards. Goals:
>
> 1. **Reproducibility**: any reviewer can verify our numbers in under 30 minutes with a single command. Deterministic seeds, dockerized harness, public artifacts.
> 2. **Statistical rigor**: confidence intervals (bootstrapping), Cohen's κ judge-human agreement, McNemar's paired test for sprint-over-sprint comparisons.
> 3. **Judge robustness**: multi-model ensemble + programmatic judges per category + 5-point granular scale + chain-of-thought reasoning. Cohen's κ ≥ 0.75 vs human gold (vs current undocumented 1-shot Gemini Flash binary).
> 4. **Public deliverable**: open-source the harness as a standalone tool. Methodology white paper (10 pages). Comparison table vs Memobase / mem0 / LongMemEval / MSC harnesses.
>
> **Driver**: M11 sprint shipped 6 ML features, M12 sprint diagnosed 75% of "regression" was actually **judge noise** (Gemini Flash temp=0.0 still flips verdicts ~20-40% on ambiguous binary cases; cache locks first verdict permanently). We were chasing artifacts, not signal. Industrial-grade eval is the prerequisite for any grant or paper claim.
>
> **Audience**: Google AI grant reviewers, Anthropic SAFE program, academic peer reviewers (NeurIPS / EMNLP), open-source community evaluating MemDB vs alternatives.

---

## Comparable to (industry baseline)

| System | Judge | Trials | Programmatic? | Calibration | Open harness? |
|--------|-------|--------|----------------|-------------|---------------|
| **Memobase** (paper 2025) | gpt-4o binary | 1 | No | No | No (closed) |
| **mem0** (paper 2025) | gpt-4o-mini binary | 1 | No | No | Partial (predictions only) |
| **LongMemEval** | gpt-4 binary | 1 | No | No | Yes |
| **MSC** | gpt-4 + human | 1 / spot-check | No | Partial | Yes |
| **MemDB v0.23.0** (current) | gemini-flash binary | 1 | No | No | Partial |
| **MemDB v0.24.0+ (M13 target)** | **Ensemble (3) + programmatic** | **3-of-3 majority** | **Yes (cat-1 numeric, cat-3 dates, cat-1 multi-fact NER)** | **κ ≥ 0.75 vs 200 human labels** | **Yes, full harness + paper** |

**Net positioning**: MemDB harness becomes the **most rigorous open-source memory benchmark methodology** in the field as of v0.24.0 release.

---

## Sprint J — 7 streams, 4-6 days senior engineering work

### Stream J1 — Core judge robustness (★★★★★, 1 day)

**Goal**: eliminate cache-noise + format false-negatives + binary verdict ambiguity.

**Files** (`evaluation/locomo/`):
- `llm_judge.py` — extend with multi-trial + cache invalidation + 5-point scale
- `judge_normalize.py` (NEW) — reference normalization (Unicode NFC, lowercase, punctuation strip, contraction expansion via `contractions` library)
- `judge_cot.py` (NEW) — chain-of-thought wrapper: prompts judge for `{reasoning: "", verdict: 0..4}`, validates self-consistency
- `tests/test_judge_robustness.py` — 30+ unit tests for normalization, multi-trial, scale conversion

**API**:
```python
@dataclass
class JudgeConfig:
    n_trials: int = 3                    # majority vote
    scale: Literal["binary", "5point"] = "5point"
    threshold: int = 3                    # if 5point, score >= 3 → CORRECT
    require_cot: bool = True
    normalize: bool = True
    cache_invalidation_key: str | None = None   # bust cache per-run
    
def judge_prediction(question, gold, prediction, cfg: JudgeConfig) -> JudgeVerdict:
    """Returns {verdict: bool, score_5pt: int, trials: [...], reasoning: str, kappa_w_self: float}"""
```

**Tests**:
- Normalization is monotonic ("Charlotte's Web" === "Charlotte\'s Web" === "charlottes web")
- 3-of-3 majority on 5/5 mixed input → CORRECT
- Cache key includes config hash; different configs don't share cache
- 5-point ≥3 maps to CORRECT, <3 to WRONG

**Verification**: Re-judge M12 cat-1 sample with J1 enabled. Q7 + Q9 cache-noise verdicts should resolve correctly. Expected cat-1 → 50%+.

### Stream J2 — Programmatic judges per category (★★★★★, 1 day)

**Goal**: deterministic verdicts on simple cats. LLM judge only when programmatic confidence is low.

**Files**:
- `judge_programmatic.py` (NEW) — per-cat extractors:
  - `judge_cat1_numeric(gold, pred)` — regex `\b\d+\b` extract; exact match (with tolerance for "two children"="2") via `word2number` library
  - `judge_cat1_multi_fact(gold, pred)` — NER (spaCy en_core_web_md) + Jaccard overlap ≥ 0.5 of named entities
  - `judge_cat3_temporal(gold, pred)` — `dateparser` extract, distance ≤ 30 days = correct
  - `judge_cat3_duration(gold, pred)` — duration parser ("4 years" === "since 2020"=now relative)
- `judge_router.py` (NEW) — dispatch: programmatic high-confidence → return verdict; else → LLM judge
- `tests/test_judge_programmatic.py` — golden set

**Hybrid logic**:
```python
def judge_dispatch(q, gold, pred, cat: int) -> JudgeVerdict:
    prog = run_programmatic(q, gold, pred, cat)
    if prog.confidence > 0.85:
        return prog                        # deterministic, instant
    return run_llm_judge(q, gold, pred)    # fall through to ensemble
```

**Verification**: Hand-pick 50 cat-1 numeric + 50 cat-3 temporal questions. Programmatic verdict matches LLM verdict on ≥95% (calibration baseline). Eliminates LLM noise on these cats entirely (cat-1 numeric is ~30% of cat-1 questions).

### Stream J3 — Multi-model ensemble (★★★★★, 1 day)

**Goal**: cross-validate across LLM families. Catches model-specific biases (Gemini Flash strict on partials, GPT lenient).

**Files**:
- `judge_ensemble.py` (NEW) — runs question through N judges in parallel, takes majority
- `judge_models.py` (NEW) — model registry with fallback chain
- `tests/test_judge_ensemble.py`

**Ensemble**:
```python
ENSEMBLE_DEFAULT = [
    {"model": "gemini-2.5-flash", "weight": 1.0, "via": "cliproxyapi"},
    {"model": "gpt-4o-mini",       "weight": 1.0, "via": "openai"},          # via $1-2 budget
    {"model": "claude-haiku-4.5",  "weight": 1.0, "via": "anthropic"},        # via $1-2 budget
]

# Cost/run: ~$3 (vs $0 single-model via free tier). Affordable per-measurement.
```

**Disagreement logging**:
- If 2-of-3 vote → flag question as `judge_uncertainty`. Surface in summary report.
- Disagreement rate is itself a quality metric (lower = more reliable benchmark).

**Verification**: Re-judge full M12 corpus with ensemble. Compare per-cat headlines vs single-model. Disagreement rate target: <5% on cat-1/3, <15% on cat-4 narrative.

### Stream J4 — Human calibration (★★★★, 1 day, manual effort)

**Goal**: ground-truth Cohen's κ between judges and humans. Required for grant credibility.

**Steps**:
1. Random sample 200 predictions stratified by cat (50 per cat × 4) from M12 full corpus run
2. Hand-label each: `correct | partially_correct | wrong | refused`
3. Compute Cohen's κ between human verdicts vs:
   - Single-model gemini-flash binary (current baseline)
   - J1 5-point scale
   - J3 ensemble
   - J2 programmatic (where applicable)
4. Iterate J1/J3 prompts to maximize κ. Target ≥0.75 (publication-grade agreement).
5. Release human-labeled subset as `evaluation/locomo/calibration/human_labels_v1.json` (open-source).

**Why this matters**: peer reviewers ask "how do you know your judge is correct?" Answer: Cohen's κ = 0.78 against 200 human labels, here's the data, reproduce yourself.

**Files**:
- `calibration/human_labels_v1.json` — 200 verdicts, blinded labeling
- `judge_calibration.py` — compute κ for each judge config
- `docs/calibration_methodology.md` — labeling guide for reproducibility

### Stream J5 — Statistical rigor (★★★★, 0.5 day)

**Goal**: every headline number has a confidence interval. Sprint-over-sprint comparisons use paired tests.

**Files**:
- `score_statistics.py` (NEW) — bootstrap CI, McNemar's test, kappa
- `score.py` — extend output JSON with CI per metric
- `compare.py` — paired McNemar's between two runs

**API**:
```python
@dataclass
class ScoreReport:
    headline: float                       # point estimate
    ci_low: float                         # 95% bootstrap CI lower
    ci_high: float                        # 95% bootstrap CI upper
    kappa_judge_human: float | None       # if calibration available
    n: int                                # sample size
    
def bootstrap_ci(verdicts: list[bool], n_resamples: int = 10_000) -> tuple[float, float]:
    """Return (ci_low, ci_high) at 95% via percentile method."""
```

**Output (sample)**:
```
M12 v0.24.1 LoCoMo Evaluation Report
=====================================
Headline (excl-5):  56.7% [54.4 — 59.0]   κ=0.78 (n=1540)
By category:
  cat-1 single-hop:    51.2% [47.4 — 55.1]   κ=0.81  n=282
  cat-2 multi-hop:     58.4% [53.0 — 63.8]   κ=0.74  n=321
  cat-3 temporal:      62.5% [52.7 — 71.4]   κ=0.79  n=96
  cat-4 open-domain:   60.5% [57.2 — 63.8]   κ=0.71  n=841
  cat-5 adversarial:   12.3% [9.4 — 15.5]    κ=0.68  n=446 (excluded from headline)

Disagreement rate (3-of-3 ensemble): 4.2% overall
Sprint-over-sprint (M11 → M12 v0.24.1):
  excl-5:   +15.2pp  (McNemar χ²=104.3, p<0.001)
  cat-2:    +44.1pp  (McNemar χ²=89.1, p<0.001)
  cat-1:    +3.7pp   (McNemar χ²=4.2, p=0.04)
```

**This is what reviewers expect.**

### Stream J6 — Pairwise comparison mode (★★★, 1-2 days, optional)

**Goal**: Bradley-Terry pairwise scoring, more robust than absolute. Used by Anthropic Constitutional AI, OpenAI RLHF.

**Approach**: instead of "is X correct?" (absolute), ask judge "which is closer to gold, prediction A or prediction B?" (pairwise). Run on N=2 randomized prediction pairs per question (e.g., M12 prediction vs Memobase published prediction). Compute Bradley-Terry rating (relative skill score, ELO-style).

**Use case**: comparing MemDB vs Memobase on same LoCoMo questions without sharing predictions data publicly. Apples-to-apples that paper reviewers love.

**Files**:
- `judge_pairwise.py` (NEW)
- `bradley_terry.py` (NEW) — fit BT model, compute relative skill
- `score_pairwise.py` — pairwise harness orchestration

**Defer to v0.25.0** if M13 v1 ship needs to be tighter.

### Stream J7 — Public artifacts + paper (★★★★, 1 day)

**Goal**: methodology white paper + open-source harness positioned for grant + academic citation.

**Deliverables**:
1. `docs/eval_harness_v2_methodology.md` (~10 pages) — design choices, judge calibration, comparison with Memobase/mem0/LongMemEval/MSC. arXiv-ready.
2. Open-source `evaluation/locomo/` as standalone repo `anatolykoptev/locomo-harness-v2` with:
   - Docker compose for full eval stack
   - Reproducibility guide ("clone, `make eval`, see same numbers")
   - Sample data (privacy-cleaned subset)
3. README badge: `LoCoMo κ=0.78 (n=200 human-labeled, 95% CI ±2.3pp)`
4. Blog post draft (`docs/blog/2026-05-locomo-eval-v2.md`) — narrative for grant pitch

**Ship checklist**:
- [ ] All J1-J5 merged
- [ ] κ ≥ 0.75 on 200 human-labeled subset
- [ ] Disagreement rate < 5% overall
- [ ] M12 full-corpus run with ensemble — published in PR + GitHub Release
- [ ] Methodology paper draft on arXiv
- [ ] Open-source harness repo public
- [ ] Twitter / HN announcement post

---

## Cross-Stream Coordination

**Disjoint files**: each stream lives in different `evaluation/locomo/judge_*.py` files. Test files separate. Calibration data isolated.

**Dependency graph**:
```
J1 (core robustness)  ─┬──> J3 (ensemble uses J1's CoT + 5-point)
J2 (programmatic)     ─┘    
J4 (human calibration) ──> tests J1/J2/J3 against ground truth
J5 (statistics)        ──> consumed by all
J6 (pairwise)          ──> independent, post-J3
J7 (paper + release)   ──> blocks on all + final M12 full corpus run
```

**Parallel-safe streams**: J1 + J2 + J5 + J6 (disjoint files, no semantic conflict).
**Serial dependency**: J3 needs J1, J4 needs J1+J2+J3 to compute κ comparisons, J7 needs all.

---

## Subagent Dispatch Templates (per stream)

### J1 implementer prompt
```
TASK: M13 J1 — Core judge robustness (multi-trial + normalization + cache invalidation + 5-point + CoT).
PLAN: docs/superpowers/plans/2026-04-29-m13-evaluation-harness-v2.md (J1 section)

🚨 NEVER silently disable. v1 binary judge must remain operable as `cfg.scale="binary"`.
Use go-code MCP equivalent for Python: ripgrep/grep on existing patterns first.

WORKTREE: cd ~/src/MemDB && git fetch origin && git worktree add /tmp/m13-j1 -b feat/m13-judge-robustness origin/main
[full implementation contract per the J1 section above]

VERIFY:
- pytest evaluation/locomo/tests/test_judge_*.py -v (all green)
- Re-judge M12 cat-1 sample with new harness vs old, paste comparison
- Multi-trial overhead measured: ~3× wall-time per judged question, $0 cost via cliproxyapi

DELIVERABLE: PR opened to MemDB main, branch fix/m13-judge-robustness. Two-stage review BEFORE done. NEVER merge.
```

### J2 implementer prompt
[similar structure with J2 section]

### J3, J4, J5, J6, J7 — same template, sectioned.

---

## Risk + Mitigation

| Risk | Mitigation |
|------|-----------|
| Multi-model ensemble cost balloons | $3-5/run on full corpus; budget capped via env-driven trial count |
| Human labeling 200 examples is slow | Single afternoon for trained labeler; subset can be re-used for κ on every release |
| Ensemble disagreement on cat-5 adversarial high (>15%) | Expected — adversarial is hard. Document, surface in report, do NOT include in headline |
| Programmatic judge for cat-1 multi-fact (NER) misses paraphrases | Hybrid fallback to LLM when NER overlap < 0.5; tune threshold via J4 calibration |
| Bradley-Terry pairwise rating requires reference predictions from competitors | Use Memobase published predictions (public on github), or skip J6 v1 |
| Open-sourcing harness exposes our LoCoMo cube data | Privacy-clean subset; full corpus stays internal until LoCoMo dataset itself is released |

---

## ROI / Sprint Sizing

| Stream | Effort | Impact (grant pitch) |
|--------|--------|----------------------|
| **J1** core robustness | 1d | **Eliminate ~80% of judge noise** (cache + 5-point) |
| **J2** programmatic | 1d | **30-40% of cat-1 + cat-3 deterministic** (no LLM noise) |
| **J3** ensemble | 1d | **Cross-model validation** (Gemini + GPT + Claude) |
| **J4** human calibration | 1d (manual) | **Cohen's κ ≥ 0.75 ground truth** — grant-pitch killer |
| **J5** stats rigor | 0.5d | **CI + McNemar paired tests** — paper-quality |
| **J6** pairwise | 1-2d | **Bradley-Terry comparable to RLHF papers** — optional v1 |
| **J7** paper + release | 1d | **arXiv-ready methodology** — grant deliverable |

**Total**: 5-7 days senior engineering + 1 afternoon manual labeling.

**Grant-pitch positioning**: "MemDB ships with the most rigorous open-source memory benchmark methodology in the field — multi-model ensemble with κ=0.78 vs human labels, programmatic judges per category, statistical CI on every reported number, full reproducibility in a Docker compose."

---

## Done Criteria (M13 v1 ship — v0.24.0)

1. **All 7 streams merged** (J1-J7).
2. **κ ≥ 0.75** on 200 human-labeled subset.
3. **Ensemble disagreement < 5%** overall (excluding cat-5 adversarial).
4. **Full M12 corpus re-judged** with v2 harness — headline + per-cat with CIs published.
5. **Open-source `locomo-harness-v2` repo** live on GitHub.
6. **Methodology paper draft** on arXiv.
7. **Compared to Memobase / mem0 / LongMemEval / MSC** in a public table (positioning).
8. **Tweet / blog announcement** for grant materials.

---

## Out of scope (defer to M14+)

- Live A/B testing harness (production observability, not benchmark)
- Multi-turn conversation eval (LoCoMo is single-turn)
- Multimodal eval (no vision/audio in our stack)
- Continuous benchmark CI (run on every PR) — defer until cost-efficient

---

## Backlog (noted, not in M13 scope)

- Bias evaluation (does our judge favor longer answers? More structured?)
- Adversarial evaluator probing (jailbreak resistance)
- Latency / cost benchmarks alongside quality (Pareto frontier reporting)

End of M13 plan.
