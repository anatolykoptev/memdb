#!/usr/bin/env python3
"""
m13_j1_smoke_retest.py — Re-judge the M12 cat-1 sample under v1 (binary, 1-trial)
vs v2 (5pt, 3-trial, normalized) judge configs and print the per-question delta.

Run:
    cd /tmp/m13-j1
    export CLI_PROXY_API_KEY=$(grep -E "^CLI_PROXY_API_KEY=" ~/deploy/krolik-server/.env | cut -d= -f2-)
    python3 evaluation/locomo/scripts/m13_j1_smoke_retest.py

Output: prints a table with per-Q v1 verdict, v2 verdict, v2 5pt-score, kappa,
and a flip summary. Writes results to results/m13-j1-cat1-smoke.json.
"""

from __future__ import annotations

import json
import sys
import tempfile
from pathlib import Path

_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(_ROOT))

import llm_judge as lj  # noqa: E402

PRED_FILE = _ROOT / "results" / "predictions-m12-combined-sample.json"
OUT_FILE = _ROOT / "results" / "m13-j1-cat1-smoke.json"


def main() -> int:
    with PRED_FILE.open() as f:
        doc = json.load(f)
    preds = doc.get("predictions", doc if isinstance(doc, list) else [])
    cat1 = [p for p in preds if p.get("category") == 1]
    print(f"Loaded {len(preds)} predictions, {len(cat1)} cat-1.")

    # Project predictions into the {question, gold_answer, prediction} shape
    # the judge expects. Predictions in M12 use "chat_answer" (or fallback to
    # top retrieved content) — match score.py:pick_prediction_text logic.
    rows: list[dict] = []
    for r in cat1:
        gold = str(r.get("gold_answer", ""))
        pred = r.get("chat_answer") or ""
        if not pred:
            retrieved = r.get("retrieved") or []
            pred = " ".join(item.get("content", "") for item in retrieved[:3])
        rows.append({
            "conv_id": r.get("conv_id"),
            "question_idx": r.get("question_idx"),
            "question": r.get("question") or "",
            "gold_answer": gold,
            "prediction": pred,
        })

    # Use a TEMP cache so this run is isolated from any pre-existing cache.
    with tempfile.TemporaryDirectory() as tmp:
        cache = lj.LLMJudgeCache(path=Path(tmp) / "cache.json")

        # ----- V1 baseline: binary, 1-trial, no normalize -----
        cfg_v1 = lj.JudgeConfig(
            n_trials=1,
            scale="binary",
            require_cot=False,
            normalize=False,
            cache_invalidation_key="m13-j1-v1-baseline",
        )
        # ----- V2: 5pt, 3-trial, normalized -----
        cfg_v2 = lj.JudgeConfig(
            n_trials=3,
            scale="5point",
            threshold=3,
            require_cot=True,
            normalize=True,
            cache_invalidation_key="m13-j1-new",
        )

        print(f"Judging V1 (binary, 1-trial)... cfg_sig={cfg_v1.cache_signature}")
        v1_verdicts = lj.judge_predictions_batch(rows, cfg_v1, workers=5, cache=cache)
        print(f"Judging V2 (5pt, 3-trial, normalized)... cfg_sig={cfg_v2.cache_signature}")
        v2_verdicts = lj.judge_predictions_batch(rows, cfg_v2, workers=5, cache=cache)

    # ----- Comparison table -----
    flips_correct = 0   # V1 wrong → V2 correct
    flips_wrong = 0     # V1 correct → V2 wrong
    same = 0
    perq: list[dict] = []
    print()
    print(f"{'idx':>3}  {'V1':>2}  {'V2':>2}  {'5pt':>3}  {'κ':>4}  question")
    print("-" * 90)
    for i, (row, v1, v2) in enumerate(zip(rows, v1_verdicts, v2_verdicts)):
        v1b = int(v1.verdict)
        v2b = int(v2.verdict)
        flag = ""
        if v1b == 0 and v2b == 1:
            flips_correct += 1
            flag = "  ↑ FLIP→CORRECT"
        elif v1b == 1 and v2b == 0:
            flips_wrong += 1
            flag = "  ↓ FLIP→WRONG"
        else:
            same += 1
        q = (row.get("question") or "")[:50]
        print(
            f"{i:>3}  {v1b:>2}  {v2b:>2}  {v2.score_5pt:>3}  {v2.kappa_w_self:>.2f}  {q}{flag}"
        )
        perq.append({
            "conv_id": row["conv_id"],
            "question_idx": row["question_idx"],
            "question": row["question"],
            "gold_answer": row["gold_answer"],
            "prediction": row["prediction"][:200],
            "v1_verdict": v1b,
            "v2_verdict": v2b,
            "v2_score_5pt": v2.score_5pt,
            "v2_kappa_w_self": v2.kappa_w_self,
            "v2_reasoning": v2.reasoning_summary,
            "v2_trials": [
                {"score_5pt": t.get("score_5pt"), "self_consistent": t.get("self_consistent")}
                for t in v2.trials
            ],
        })

    n = len(rows)
    v1_correct = sum(int(v.verdict) for v in v1_verdicts)
    v2_correct = sum(int(v.verdict) for v in v2_verdicts)
    print()
    print(f"V1 cat-1 correct: {v1_correct}/{n}  ({v1_correct/n:.1%})")
    print(f"V2 cat-1 correct: {v2_correct}/{n}  ({v2_correct/n:.1%})")
    print(
        f"Delta:  +{flips_correct} (wrong→correct)   "
        f"-{flips_wrong} (correct→wrong)   "
        f"={same} unchanged   "
        f"net {(v2_correct-v1_correct)/n:+.1%}pp"
    )

    summary = {
        "v1_correct": v1_correct,
        "v2_correct": v2_correct,
        "n": n,
        "flips_to_correct": flips_correct,
        "flips_to_wrong": flips_wrong,
        "unchanged": same,
        "net_delta_pp": (v2_correct - v1_correct) / n if n else 0,
        "v2_avg_kappa": sum(v.kappa_w_self for v in v2_verdicts) / max(1, n),
        "v2_avg_score_5pt": sum(v.score_5pt for v in v2_verdicts) / max(1, n),
        "per_q": perq,
    }
    OUT_FILE.parent.mkdir(parents=True, exist_ok=True)
    with OUT_FILE.open("w") as f:
        json.dump(summary, f, indent=2, ensure_ascii=False)
    print(f"\nWrote → {OUT_FILE}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
