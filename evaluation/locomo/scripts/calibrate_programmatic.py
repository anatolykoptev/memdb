"""
calibrate_programmatic.py — Run programmatic judges against an M11/M12
predictions-with-llm-judge file and report:

  1.  Coverage: % of cat-1 + cat-3 questions that get a high-confidence
      programmatic verdict (would skip LLM in production).
  2.  Agreement: when programmatic emits a high-conf verdict, how often does
      it match the LLM judge's verdict.  Target ≥ 95%.
  3.  Method breakdown: counts per extractor / verdict combo.

Usage:
    python evaluation/locomo/scripts/calibrate_programmatic.py \\
        --judged-file evaluation/locomo/results/m11-full-judged.json
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from pathlib import Path

_REPO = Path(__file__).resolve().parents[3]
_LOCOMO = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(_LOCOMO))

import judge_programmatic as jp  # noqa: E402


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--judged-file", type=Path, required=True)
    p.add_argument("--categories", default="1,3", help="comma-separated cats")
    args = p.parse_args()

    cats_to_audit = {int(c.strip()) for c in args.categories.split(",")}

    with args.judged_file.open() as f:
        doc = json.load(f)
    rows = doc.get("per_qa", [])

    by_cat: dict[int, list[dict]] = {}
    for r in rows:
        c = r.get("category")
        if c is None:
            continue
        by_cat.setdefault(int(c), []).append(r)

    print(f"\n=== Calibrating against {args.judged_file} ===")
    print(f"Total rows: {len(rows)}")
    for c, rs in sorted(by_cat.items()):
        marker = " [audit]" if c in cats_to_audit else ""
        print(f"  cat {c}: {len(rs)}{marker}")

    overall: dict[str, int] = Counter()
    method_counts: dict[str, Counter] = {}
    method_agree: dict[str, Counter] = {}

    for cat in sorted(cats_to_audit):
        rs = by_cat.get(cat, [])
        n = len(rs)
        if n == 0:
            continue
        prog_total = 0
        prog_high_conf = 0
        agree = 0
        disagree = 0
        prog_correct_v_llm: list[dict] = []
        method_counts[f"cat{cat}"] = Counter()
        method_agree[f"cat{cat}"] = Counter()
        for r in rs:
            gold = str(r.get("gold_answer") or "")
            pred = str(r.get("prediction") or "")
            llm_score = r.get("llm_score")
            v = jp.run_programmatic(cat, gold, pred)
            if v is None:
                continue
            prog_total += 1
            method_counts[f"cat{cat}"][v.method] += 1
            if v.confidence < jp.ROUTER_THRESHOLD:
                continue
            prog_high_conf += 1
            if llm_score is None:
                continue
            prog_v = 1 if v.verdict else 0
            if prog_v == int(llm_score):
                agree += 1
                method_agree[f"cat{cat}"][f"{v.method}:agree"] += 1
            else:
                disagree += 1
                method_agree[f"cat{cat}"][f"{v.method}:DISAGREE"] += 1
                if len(prog_correct_v_llm) < 5:
                    prog_correct_v_llm.append({
                        "q": (r.get("question") or "")[:80],
                        "gold": gold[:60],
                        "pred": pred[:120],
                        "prog": prog_v,
                        "llm": int(llm_score),
                        "method": v.method,
                        "conf": v.confidence,
                    })

        coverage = prog_high_conf / n if n else 0.0
        agreement = agree / (agree + disagree) if (agree + disagree) else 1.0
        print(f"\n--- cat {cat} ---")
        print(f"  total questions:               {n}")
        print(f"  programmatic returned verdict: {prog_total}  ({prog_total/n:.1%})")
        print(f"  high-confidence (would skip LLM): {prog_high_conf}  ({coverage:.1%})  ← coverage")
        print(f"  agreement with LLM judge: {agree}/{agree+disagree} = {agreement:.1%}  ← target ≥95%")
        if prog_correct_v_llm:
            print("  disagreements (sample):")
            for d in prog_correct_v_llm:
                print(f"    [{d['method']} conf={d['conf']:.2f}] prog={d['prog']} llm={d['llm']}")
                print(f"      Q: {d['q']}")
                print(f"      G: {d['gold']}")
                print(f"      P: {d['pred']}")
        print(f"  method counts: {dict(method_counts[f'cat{cat}'])}")
        overall["total"] += n
        overall["coverage"] += prog_high_conf
        overall["agree"] += agree
        overall["disagree"] += disagree

    if overall["total"]:
        print("\n=== Aggregate (audited cats) ===")
        cov = overall["coverage"] / overall["total"]
        agg = overall["agree"] / max(1, overall["agree"] + overall["disagree"])
        print(f"  coverage:   {cov:.1%}  ({overall['coverage']}/{overall['total']})")
        print(f"  agreement:  {agg:.1%}  ({overall['agree']}/{overall['agree']+overall['disagree']})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
