#!/usr/bin/env python3
"""
compare.py — diff two LoCoMo eval results JSONs.

Usage:
    python3 compare.py baseline.json candidate.json
"""

from __future__ import annotations

import argparse
import json
import sys

from pathlib import Path


def fmt_delta(v: float) -> str:
    sign = "+" if v >= 0 else ""
    return f"{sign}{v:.3f}"


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("baseline", type=Path)
    p.add_argument("candidate", type=Path)
    args = p.parse_args()

    with args.baseline.open() as f:
        base = json.load(f)
    with args.candidate.open() as f:
        cand = json.load(f)

    ba = base.get("aggregate", {})
    ca = cand.get("aggregate", {})
    metrics = ["em", "f1", "semsim", "hit_at_k"]

    print(f"# LoCoMo: {args.baseline.name} → {args.candidate.name}\n")
    print(f"baseline commit: {base.get('meta', {}).get('commit_sha', 'unknown')}")
    print(f"candidate commit: {cand.get('meta', {}).get('commit_sha', 'unknown')}")
    print(f"baseline n: {ba.get('n', 0)}  candidate n: {ca.get('n', 0)}\n")

    print("| Metric   | Baseline | Candidate | Delta    |")
    print("|----------|----------|-----------|----------|")
    for m in metrics:
        b = ba.get(m, 0.0)
        c = ca.get(m, 0.0)
        print(f"| {m:<8s} | {b:8.3f} | {c:9.3f} | {fmt_delta(c - b):>8s} |")

    # By category
    b_cats = ba.get("by_category", {})
    c_cats = ca.get("by_category", {})
    all_cats = sorted(set(b_cats) | set(c_cats))
    if all_cats:
        print("\n## By category\n")
        print("| cat | metric   | baseline | candidate | delta   | n_base | n_cand |")
        print("|-----|----------|----------|-----------|---------|--------|--------|")
        for cat in all_cats:
            b = b_cats.get(cat, {})
            c = c_cats.get(cat, {})
            for m in metrics:
                bv = b.get(m, 0.0)
                cv = c.get(m, 0.0)
                print(
                    f"| {cat:<3s} | {m:<8s} | {bv:8.3f} | {cv:9.3f} | {fmt_delta(cv - bv):>7s} "
                    f"| {b.get('n', 0):>6d} | {c.get('n', 0):>6d} |"
                )
    return 0


if __name__ == "__main__":
    sys.exit(main())
