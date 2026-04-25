#!/usr/bin/env python3
"""
score.py — compute EM / F1 / semsim / hit@k / LLM Judge on a predictions JSON.

Reads the output of query.py, writes a results JSON:

{
  "meta": {...},
  "aggregate": {"em": 0.33, "f1": 0.51, "semsim": 0.78, "hit_at_k": 0.72,
                "llm_judge": 0.62,  # only when --llm-judge
                "n": 20, "by_category": {...}},
  "aggregate_with_excl_none": {...},   # all categories included (same as aggregate)
  "aggregate_with_excl_5": {...},       # category 5 excluded (mirrors Memobase benchmark)
  "per_qa": [{"conv_id", "question_idx", "em", "f1", "semsim", "hit_at_k",
              "llm_score": 0|1, "llm_reason": str}, ...]  # llm_* only with --llm-judge
}

Two-track reporting: the JSON always contains BOTH aggregate_with_excl_none
(no exclusions) and aggregate_with_excl_5 (category 5 excluded) regardless of
any --exclude-categories flag. This mirrors Memobase's hardcoded exclusion of
category 5 in their published 75.78% score
(memobase_search.py:147 `exclude_category={5}`), enabling fair apples-to-apples
comparison without hiding our full-inclusive score.

If --exclude-categories is passed, additional aggregate_with_excl_<N> keys are
also emitted for each requested exclusion set.

Scoring uses the chat_answer if present, else the top-retrieved memory
content. If you want pure retrieval scoring, pass --retrieval-only.

Pass --llm-judge to additionally score each prediction via Gemini Flash
(CLIProxyAPI at http://127.0.0.1:8317/v1). Results are cached in
evaluation/locomo/results/.llm_judge_cache.json and reused on re-runs —
the second pass over unchanged predictions takes <1s.

Pre-M9 MILESTONES.md numbers are F1-only. LLM Judge (--llm-judge) produces
a number directly comparable to public leaderboard figures:
Memobase 75.78%, Mem0, Zep, LangMem all use the same binary judge.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import string
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

from collections import Counter, defaultdict
from pathlib import Path

import requests


# --------------------- text normalization ---------------------
def normalize(text: str) -> str:
    if text is None:
        return ""
    text = str(text).lower()
    text = re.sub(r"\b(a|an|the)\b", " ", text)
    text = text.translate(str.maketrans("", "", string.punctuation))
    text = re.sub(r"\s+", " ", text).strip()
    return text


def tokens(text: str) -> list[str]:
    return normalize(text).split()


# --------------------- metrics ---------------------
def exact_match(pred: str, gold: str) -> float:
    return float(normalize(pred) == normalize(gold))


def token_f1(pred: str, gold: str) -> float:
    p_toks = tokens(pred)
    g_toks = tokens(gold)
    if not p_toks or not g_toks:
        return float(p_toks == g_toks)
    common = Counter(p_toks) & Counter(g_toks)
    num_same = sum(common.values())
    if num_same == 0:
        return 0.0
    precision = num_same / len(p_toks)
    recall = num_same / len(g_toks)
    return 2 * precision * recall / (precision + recall)


def hit_at_k(retrieved_contents: list[str], gold: str) -> float:
    if not retrieved_contents:
        return 0.0
    g_toks = set(tokens(gold))
    if not g_toks:
        return 0.0
    for content in retrieved_contents:
        if g_toks & set(tokens(content)):
            return 1.0
    return 0.0


# --------------------- semantic similarity ---------------------
def bow_cosine(a: str, b: str) -> float:
    """Fallback: bag-of-words cosine on normalized tokens."""
    ca, cb = Counter(tokens(a)), Counter(tokens(b))
    if not ca or not cb:
        return 0.0
    vocab = set(ca) | set(cb)
    dot = sum(ca[t] * cb[t] for t in vocab)
    na = math.sqrt(sum(v * v for v in ca.values()))
    nb = math.sqrt(sum(v * v for v in cb.values()))
    if not na or not nb:
        return 0.0
    return dot / (na * nb)


class Embedder:
    """Calls an OpenAI-compatible embeddings endpoint via LLM_URL/LLM_API_KEY."""

    def __init__(self, base: str, api_key: str, model: str):
        self.base = base.rstrip("/")
        self.api_key = api_key
        self.model = model
        self._cache: dict[str, list[float]] = {}

    def embed(self, text: str) -> list[float]:
        if text in self._cache:
            return self._cache[text]
        resp = requests.post(
            f"{self.base}/embeddings",
            json={"input": text, "model": self.model, "encoding_format": "float"},
            headers={"Authorization": f"Bearer {self.api_key}"},
            timeout=30,
        )
        resp.raise_for_status()
        emb = resp.json()["data"][0]["embedding"]
        self._cache[text] = emb
        return emb


def cosine(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if not na or not nb:
        return 0.0
    return dot / (na * nb)


# --------------------- main ---------------------
def pick_prediction_text(rec: dict, retrieval_only: bool) -> str:
    if not retrieval_only and rec.get("chat_answer"):
        return rec["chat_answer"]
    retrieved = rec.get("retrieved") or []
    # concatenate top-3 retrieved contents as pseudo-answer
    top = retrieved[:3]
    return " ".join(item.get("content", "") for item in top)


def summarize(per_qa: list[dict]) -> dict:
    if not per_qa:
        return {"em": 0.0, "f1": 0.0, "semsim": 0.0, "hit_at_k": 0.0, "n": 0, "by_category": {}}
    n = len(per_qa)
    agg: dict = {
        "em": sum(r["em"] for r in per_qa) / n,
        "f1": sum(r["f1"] for r in per_qa) / n,
        "semsim": sum(r["semsim"] for r in per_qa) / n,
        "hit_at_k": sum(r["hit_at_k"] for r in per_qa) / n,
        "n": n,
    }
    # LLM Judge aggregate — only when scores are present
    llm_rows = [r["llm_score"] for r in per_qa if "llm_score" in r]
    if llm_rows:
        agg["llm_judge"] = sum(llm_rows) / len(llm_rows)
        agg["llm_judge_n"] = len(llm_rows)

    by_cat: dict[str, list[dict]] = defaultdict(list)
    for r in per_qa:
        by_cat[str(r.get("category"))].append(r)

    cat_summaries: dict[str, dict] = {}
    for cat, rs in by_cat.items():
        cat_agg: dict = {
            "em": sum(r["em"] for r in rs) / len(rs),
            "f1": sum(r["f1"] for r in rs) / len(rs),
            "semsim": sum(r["semsim"] for r in rs) / len(rs),
            "hit_at_k": sum(r["hit_at_k"] for r in rs) / len(rs),
            "n": len(rs),
        }
        cat_llm = [r["llm_score"] for r in rs if "llm_score" in r]
        if cat_llm:
            cat_agg["llm_judge"] = sum(cat_llm) / len(cat_llm)
        cat_summaries[cat] = cat_agg

    agg["by_category"] = cat_summaries
    return agg


def git_sha() -> str:
    try:
        return (
            subprocess.check_output(
                ["git", "-C", str(Path(__file__).resolve().parent), "rev-parse", "HEAD"],
                stderr=subprocess.DEVNULL,
            )
            .decode()
            .strip()
        )
    except Exception:
        return "unknown"


def parse_exclude_categories(value: str) -> frozenset[int]:
    """Parse comma-separated ints like '5' or '4,5' into a frozenset."""
    if not value.strip():
        return frozenset()
    return frozenset(int(x.strip()) for x in value.split(",") if x.strip())


def excl_key(excl: frozenset[int]) -> str:
    """Return the JSON key suffix for a given exclusion set.

    frozenset()  → 'aggregate_with_excl_none'
    frozenset({5}) → 'aggregate_with_excl_5'
    frozenset({4,5}) → 'aggregate_with_excl_4_5'
    """
    if not excl:
        return "aggregate_with_excl_none"
    return "aggregate_with_excl_" + "_".join(str(c) for c in sorted(excl))


def build_aggregate_tracks(
    per_qa: list[dict], extra_excl_sets: list[frozenset[int]]
) -> dict[str, dict]:
    """Return a dict of aggregate dicts keyed by excl_key().

    Always includes excl_none (full) and excl_{5} (cat-5 excluded).
    extra_excl_sets adds further tracks if requested via --exclude-categories.
    """
    # Canonical set of tracks: always emit these two regardless of flags
    tracks: dict[str, frozenset[int]] = {
        "aggregate_with_excl_none": frozenset(),
        "aggregate_with_excl_5": frozenset({5}),
    }
    for s in extra_excl_sets:
        k = excl_key(s)
        if k not in tracks:
            tracks[k] = s
        # Also emit the union with {5} for publication-comparable scoring
        # (Memobase convention: cat-5 always excluded).  Skip when s already
        # contains 5 — the union would be identical to the track just added.
        union = s | frozenset({5})
        uk = excl_key(union)
        if uk not in tracks:
            tracks[uk] = union
    result: dict[str, dict] = {}
    for key, excl in tracks.items():
        filtered = [r for r in per_qa if int(r.get("category") or 0) not in excl]
        result[key] = summarize(filtered)
    return result


def print_dual_track_summary(agg_tracks: dict[str, dict]) -> None:
    """Print a side-by-side summary block for all aggregate tracks."""
    keys = sorted(agg_tracks.keys())
    if not keys:
        return
    col_w = 12
    header = f"{'Metric':<12s}" + "".join(f"  {k:<{col_w}s}" for k in keys)
    print("\n── Dual-track aggregate ──")
    print(header)
    print("-" * len(header))
    # Include llm_judge row only if at least one track has it
    metrics = ["n", "em", "f1", "semsim", "hit_at_k"]
    if any("llm_judge" in agg_tracks[k] for k in keys):
        metrics.append("llm_judge")
    for metric in metrics:
        row = f"{metric:<12s}"
        for k in keys:
            val = agg_tracks[k].get(metric, 0)
            if metric == "n":
                row += f"  {int(val):<{col_w}d}"
            else:
                row += f"  {val:<{col_w}.3f}"
        print(row)


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--predictions", type=Path, required=True)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument(
        "--retrieval-only", action="store_true", help="Score top-retrieved instead of chat_answer."
    )
    p.add_argument(
        "--embed-model", default=os.getenv("LOCOMO_EMBED_MODEL", "text-embedding-3-small")
    )
    p.add_argument("--llm-url", default=os.getenv("LLM_URL", ""))
    p.add_argument("--llm-api-key", default=os.getenv("LLM_API_KEY", ""))
    p.add_argument("--no-embed", action="store_true", help="Skip embedding API, use BoW cosine.")
    p.add_argument(
        "--exclude-categories",
        default="",
        help=(
            "Comma-separated category ints to exclude, e.g. '5' or '4,5'. "
            "Output always contains aggregate_with_excl_none and aggregate_with_excl_5 "
            "regardless of this flag. Flag adds additional tracks."
        ),
    )
    p.add_argument(
        "--llm-judge",
        action="store_true",
        help=(
            "Score each prediction via Gemini Flash LLM judge (CLIProxyAPI). "
            "Adds llm_score/llm_reason per QA and llm_judge aggregate. "
            "Results cached in results/.llm_judge_cache.json."
        ),
    )
    p.add_argument(
        "--llm-judge-model",
        default=os.getenv("LOCOMO_JUDGE_MODEL", "gemini-2.5-flash"),
        help="Model for LLM judge (default: gemini-2.5-flash).",
    )
    p.add_argument(
        "--llm-judge-workers",
        type=int,
        default=10,
        help="Concurrent judge calls (default: 10).",
    )
    args = p.parse_args()
    extra_excl = parse_exclude_categories(args.exclude_categories)

    with args.predictions.open() as f:
        pred_doc = json.load(f)
    predictions = pred_doc.get("predictions", pred_doc if isinstance(pred_doc, list) else [])
    predictions.sort(key=lambda r: (r.get("conv_id", ""), r.get("question_idx", 0)))

    embedder = None
    semsim_mode = "bow"
    if not args.no_embed and args.llm_url and args.llm_api_key:
        embedder = Embedder(args.llm_url, args.llm_api_key, args.embed_model)
        semsim_mode = f"embed:{args.embed_model}"

    per_qa: list[dict] = []
    for rec in predictions:
        gold = str(rec.get("gold_answer", ""))
        pred = pick_prediction_text(rec, args.retrieval_only)
        retrieved_contents = [i.get("content", "") for i in (rec.get("retrieved") or [])]
        em = exact_match(pred, gold)
        f1 = token_f1(pred, gold)
        h = hit_at_k(retrieved_contents, gold)
        if embedder and pred.strip() and gold.strip():
            try:
                sim = cosine(embedder.embed(pred[:4000]), embedder.embed(gold[:4000]))
            except (requests.RequestException, KeyError, ValueError) as exc:
                print(f"  embed fail ({exc}) — fallback to BoW", file=sys.stderr)
                sim = bow_cosine(pred, gold)
        else:
            sim = bow_cosine(pred, gold)
        per_qa.append(
            {
                "conv_id": rec.get("conv_id"),
                "question_idx": rec.get("question_idx"),
                "question": rec.get("question"),
                "category": rec.get("category"),
                "gold_answer": gold,
                "prediction": pred[:500],
                "em": em,
                "f1": f1,
                "semsim": sim,
                "hit_at_k": h,
                "error": rec.get("error"),
            }
        )

    # ----- LLM Judge (concurrent) -----
    if args.llm_judge:
        from llm_judge import get_shared_cache, judge  # noqa: PLC0415

        judge_cache = get_shared_cache()
        judge_api_base = os.getenv("LLM_API_BASE", "http://127.0.0.1:8317/v1")
        judge_api_key = os.getenv("CLI_PROXY_API_KEY") or os.getenv("LLM_API_KEY") or ""

        print(
            f"Running LLM Judge ({args.llm_judge_model}, "
            f"workers={args.llm_judge_workers}, n={len(per_qa)}) ...",
            file=sys.stderr,
        )

        def _judge_row(row: dict) -> dict:
            result = judge(
                question=row.get("question") or "",
                gold=row.get("gold_answer") or "",
                prediction=row.get("prediction") or "",
                model=args.llm_judge_model,
                api_base=judge_api_base,
                api_key=judge_api_key,
                cache=judge_cache,
            )
            return result

        try:
            with ThreadPoolExecutor(max_workers=args.llm_judge_workers) as executor:
                futures = {executor.submit(_judge_row, row): i for i, row in enumerate(per_qa)}
                done = 0
                for fut in as_completed(futures):
                    idx = futures[fut]
                    result = fut.result()
                    per_qa[idx]["llm_score"] = result["score"]
                    per_qa[idx]["llm_reason"] = result["reason"]
                    done += 1
                    if done % 50 == 0:
                        print(f"  judge {done}/{len(per_qa)} ...", file=sys.stderr)
        finally:
            judge_cache.flush()

        errors = sum(1 for r in per_qa if str(r.get("llm_reason", "")).startswith("judge_error:"))
        if errors:
            print(f"  judge_error count: {errors}/{len(per_qa)}", file=sys.stderr)

    agg = summarize(per_qa)
    # Build dual-track (and any extra) aggregate dicts
    extra_excl_sets: list[frozenset[int]] = [extra_excl] if extra_excl else []
    agg_tracks = build_aggregate_tracks(per_qa, extra_excl_sets)

    # by_category is available both nested in aggregate (backward compat with compare.py)
    # and as a top-level key per M2 spec so callers can read it directly.
    out: dict = {
        "meta": {
            "commit_sha": git_sha(),
            "predictions_file": str(args.predictions),
            "retrieval_only": args.retrieval_only,
            "semsim_mode": semsim_mode,
            "exclude_categories": sorted(extra_excl) if extra_excl else [],
            "llm_judge_model": args.llm_judge_model if args.llm_judge else None,
            **pred_doc.get("meta", {}),
        },
        "aggregate": agg,
        "by_category": agg.get("by_category", {}),
        **agg_tracks,
        "per_qa": per_qa,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)

    judge_line = ""
    if "llm_judge" in agg:
        judge_line = f"  llm_judge={agg['llm_judge']:.3f}"
    print(
        f"n={agg['n']}  em={agg['em']:.3f}  f1={agg['f1']:.3f}  "
        f"semsim={agg['semsim']:.3f}  hit@k={agg['hit_at_k']:.3f}"
        + judge_line
    )
    # Print per-category summary if more than one category present
    by_cat = agg.get("by_category", {})
    has_llm = any("llm_judge" in cv for cv in by_cat.values())
    if len(by_cat) > 1:
        cat_names = {
            "1": "single-hop",
            "2": "multi-hop",
            "3": "temporal",
            "4": "open-domain",
            "5": "adversarial",
        }
        hdr_llm = f"  {'llm_j':>6}" if has_llm else ""
        print("\nPer-category breakdown:")
        print(
            f"  {'cat':<3}  {'name':<12}  {'n':>4}  {'em':>6}  {'f1':>6}  {'semsim':>7}  {'hit@k':>6}"
            + hdr_llm
        )
        for cat in sorted(by_cat.keys(), key=lambda c: int(c) if c.isdigit() else 99):
            cv = by_cat[cat]
            name = cat_names.get(cat, "unknown")
            llm_col = f"  {cv['llm_judge']:>6.3f}" if "llm_judge" in cv else ""
            print(
                f"  {cat:<3}  {name:<12}  {cv['n']:>4}  "
                f"{cv['em']:>6.3f}  {cv['f1']:>6.3f}  "
                f"{cv['semsim']:>7.3f}  {cv['hit_at_k']:>6.3f}"
                + llm_col
            )
    # Always print dual-track summary
    print_dual_track_summary(agg_tracks)
    print(f"wrote → {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
