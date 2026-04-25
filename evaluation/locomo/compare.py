#!/usr/bin/env python3
"""
compare.py — diff two LoCoMo eval results JSONs.

Usage:
    python3 compare.py baseline.json candidate.json

Auto-detects aggregate_with_excl_* keys and prints a side-by-side table for
each exclusion track (e.g. excl_none = all categories, excl_5 = cat-5 excluded
as per Memobase's published benchmark methodology).
"""

from __future__ import annotations

import argparse
import json
import sys

from pathlib import Path


def fmt_delta(v: float) -> str:
    sign = "+" if v >= 0 else ""
    return f"{sign}{v:.3f}"


def _excl_tracks(doc: dict) -> list[str]:
    """Return sorted list of aggregate_with_excl_* keys present in doc."""
    return sorted(k for k in doc if k.startswith("aggregate_with_excl_"))


def _print_track_table(
    title: str, base_agg: dict, cand_agg: dict, metrics: list[str]
) -> None:
    print(f"\n## {title}\n")
    print(f"baseline n: {base_agg.get('n', 0)}  candidate n: {cand_agg.get('n', 0)}\n")
    print("| Metric   | Baseline | Candidate | Delta    |")
    print("|----------|----------|-----------|----------|")
    for m in metrics:
        b = base_agg.get(m, 0.0)
        c = cand_agg.get(m, 0.0)
        print(f"| {m:<8s} | {b:8.3f} | {c:9.3f} | {fmt_delta(c - b):>8s} |")


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("baseline", type=Path)
    p.add_argument("candidate", type=Path)
    args = p.parse_args()

    with args.baseline.open() as f:
        base = json.load(f)
    with args.candidate.open() as f:
        cand = json.load(f)

    metrics = ["em", "f1", "semsim", "hit_at_k"]

    print(f"# LoCoMo: {args.baseline.name} → {args.candidate.name}\n")
    print(f"baseline commit: {base.get('meta', {}).get('commit_sha', 'unknown')}")
    print(f"candidate commit: {cand.get('meta', {}).get('commit_sha', 'unknown')}")

    # ── legacy single-aggregate comparison (backward compat) ──────────────────
    ba = base.get("aggregate", {})
    ca = cand.get("aggregate", {})
    _print_track_table("Aggregate (all categories)", ba, ca, metrics)

    # ── dual-track comparison (auto-detect excl keys) ─────────────────────────
    b_tracks = _excl_tracks(base)
    c_tracks = _excl_tracks(cand)
    all_tracks = sorted(set(b_tracks) | set(c_tracks))

    if all_tracks:
        cat_names = {
            "5": "adversarial",
            "4": "open-domain",
            "3": "temporal",
            "2": "multi-hop",
            "1": "single-hop",
        }

        def _track_label(key: str) -> str:
            suffix = key.removeprefix("aggregate_with_excl_")
            if suffix == "none":
                return "All categories included"
            cats = suffix.split("_")
            names = [cat_names.get(c, f"cat-{c}") for c in cats]
            return f"Excluding cat-{'/'.join(cats)} ({', '.join(names)})"

        print("\n## Dual-track comparison\n")
        for track_key in all_tracks:
            b_agg = base.get(track_key, {})
            c_agg = cand.get(track_key, {})
            if not b_agg and not c_agg:
                continue
            _print_track_table(_track_label(track_key), b_agg, c_agg, metrics)

    # ── by category ──────────────────────────────────────────────────────────
    b_cats = base.get("by_category") or ba.get("by_category", {})
    c_cats = cand.get("by_category") or ca.get("by_category", {})
    all_cats = sorted(set(b_cats) | set(c_cats), key=lambda c: int(c) if c.isdigit() else 99)
    cat_names_full = {
        "1": "single-hop",
        "2": "multi-hop",
        "3": "temporal",
        "4": "open-domain",
        "5": "adversarial",
    }
    if all_cats:
        print("\n## By category\n")
        print(
            "| cat | name         | metric   | baseline | candidate | delta    | n_base | n_cand |"
        )
        print(
            "|-----|--------------|----------|----------|-----------|----------|--------|--------|"
        )
        for cat in all_cats:
            b = b_cats.get(cat, {})
            c = c_cats.get(cat, {})
            name = cat_names_full.get(cat, "unknown")
            for m in metrics:
                bv = b.get(m, 0.0)
                cv = c.get(m, 0.0)
                print(
                    f"| {cat:<3s} | {name:<12s} | {m:<8s} | {bv:8.3f} | {cv:9.3f} | {fmt_delta(cv - bv):>8s} "
                    f"| {b.get('n', 0):>6d} | {c.get('n', 0):>6d} |"
                )
    return 0


if __name__ == "__main__":
    sys.exit(main())
