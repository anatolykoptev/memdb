#!/usr/bin/env python3
"""
smoke_multi_query_cat1.py — offline smoke test of the M13 J3-Q pipeline.

What this validates (offline, against the saved M12 cat-1 sample):
  1. Splitter LLM call works end-to-end (CLIProxyAPI :8317), prints variants.
  2. Reassembler LLM call works end-to-end and emits valid 5pt JSON.
  3. Compares the legacy binary judge vs the multi-query reassembler on the
     same 10 M12 cat-1 questions, using each prediction's existing
     ``retrieved`` list as the ``union`` (single-query fallback proxy).
  4. Prints per-question (single-judge, multi-judge) verdicts and a flip
     summary so the operator can see whether the reassembler alone (i.e.
     before adding live variant retrieval) already moves the needle on
     cat-1 multi-fact aggregation.

Live multi-query benefit (true union of N variant retrievals) requires the
memdb-go MASTER_KEY to be present in the env so query.py can be re-run with
--multi-query.  This smoke does NOT call memdb-go; it uses the saved JSON.

Usage:
    CLI_PROXY_API_KEY=... python3 smoke_multi_query_cat1.py \
        --predictions evaluation/locomo/results/predictions-m12-combined-sample.json
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

# Make locomo dir importable
_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

from judge_multi_query_reassembler import reassemble_verdict  # noqa: E402
from judge_query_splitter import SplitterConfig, split_query  # noqa: E402
from llm_judge import judge as binary_judge  # noqa: E402


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument(
        "--predictions",
        type=Path,
        default=Path(__file__).resolve().parent.parent
        / "results"
        / "predictions-m12-combined-sample.json",
    )
    p.add_argument("--category", type=int, default=1)
    p.add_argument(
        "--max-questions",
        type=int,
        default=10,
        help="Cap to keep the LLM cost low (default: 10).",
    )
    args = p.parse_args()

    with args.predictions.open() as f:
        doc = json.load(f)
    preds = [
        p for p in doc.get("predictions", []) if p.get("category") == args.category
    ][: args.max_questions]
    if not preds:
        print(f"No category-{args.category} predictions found.", file=sys.stderr)
        return 2

    print(f"== M13 J3-Q smoke on {len(preds)} cat-{args.category} predictions ==")
    print(f"   predictions file: {args.predictions}")
    print(f"   commit_sha (meta): {doc.get('meta', {}).get('commit_sha', '?')}")
    print()

    splitter_cfg = SplitterConfig(use_cache=False)

    flipped_to_correct = 0
    flipped_to_wrong = 0
    same_correct = 0
    same_wrong = 0
    rows: list[dict] = []

    for i, rec in enumerate(preds, 1):
        q = rec.get("question") or ""
        gold = str(rec.get("gold_answer") or "")
        chat_ans = rec.get("chat_answer") or ""
        retrieved = rec.get("retrieved") or []

        print(f"--- [{i}/{len(preds)}] {q[:90]} ---")
        print(f"    gold: {gold!r}")
        print(f"    pred: {chat_ans[:120]!r}")

        # 1. Binary baseline judge
        binary = binary_judge(q, gold, chat_ans)
        binary_score = int(binary.get("score") or 0)

        # 2. Splitter live
        variants = split_query(q, splitter_cfg)
        print(f"    sub-queries ({len(variants)}): {variants}")

        # 3. Reassembler — uses saved single-query retrieval as union
        #    (proxy: real multi-query union requires live memdb).
        v = reassemble_verdict(
            question=q,
            gold=gold,
            sub_queries=variants,
            union_memories=retrieved,
            use_cache=False,
        )
        print(
            f"    binary={binary_score}  reassembler.score_5pt={v.score_5pt}  "
            f"reassembler.binary={v.score}"
        )
        if v.matched_facts:
            print(f"    matched: {v.matched_facts}")
        if v.missing_facts:
            print(f"    missing: {v.missing_facts}")
        print()

        rows.append(
            {
                "qi": rec.get("question_idx"),
                "question": q,
                "gold": gold,
                "binary_score": binary_score,
                "reassembler_5pt": v.score_5pt,
                "reassembler_binary": v.score,
                "matched": v.matched_facts,
                "missing": v.missing_facts,
                "sub_queries": variants,
            }
        )

        if binary_score == 0 and v.score == 1:
            flipped_to_correct += 1
        elif binary_score == 1 and v.score == 0:
            flipped_to_wrong += 1
        elif binary_score == 1 and v.score == 1:
            same_correct += 1
        else:
            same_wrong += 1

    print("== Summary ==")
    print(f"  total                   : {len(preds)}")
    print(f"  binary CORRECT          : {sum(1 for r in rows if r['binary_score']==1)}")
    print(f"  reassembler CORRECT     : {sum(1 for r in rows if r['reassembler_binary']==1)}")
    print(f"  same CORRECT            : {same_correct}")
    print(f"  same WRONG              : {same_wrong}")
    print(f"  WRONG → CORRECT (flip+) : {flipped_to_correct}")
    print(f"  CORRECT → WRONG (flip-) : {flipped_to_wrong}")
    print()
    print("NOTE: this smoke uses the SAVED single-query retrieval as the union.")
    print("      The full multi-query benefit (true variant retrieval union)")
    print("      requires re-running query.py --multi-query against memdb-go.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
