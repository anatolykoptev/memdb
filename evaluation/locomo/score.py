#!/usr/bin/env python3
"""
score.py — compute EM / F1 / semsim / hit@k on a predictions JSON.

Reads the output of query.py, writes a results JSON:

{
  "meta": {...},
  "aggregate": {"em": 0.33, "f1": 0.51, "semsim": 0.78, "hit_at_k": 0.72,
                "n": 20, "by_category": {...}},
  "per_qa": [{"conv_id", "question_idx", "em", "f1", "semsim", "hit_at_k"}, ...]
}

Scoring uses the chat_answer if present, else the top-retrieved memory
content. If you want pure retrieval scoring, pass --retrieval-only.
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
    agg = {
        "em": sum(r["em"] for r in per_qa) / n,
        "f1": sum(r["f1"] for r in per_qa) / n,
        "semsim": sum(r["semsim"] for r in per_qa) / n,
        "hit_at_k": sum(r["hit_at_k"] for r in per_qa) / n,
        "n": n,
    }
    by_cat: dict[str, list[dict]] = defaultdict(list)
    for r in per_qa:
        by_cat[str(r.get("category"))].append(r)
    agg["by_category"] = {
        cat: {
            "em": sum(r["em"] for r in rs) / len(rs),
            "f1": sum(r["f1"] for r in rs) / len(rs),
            "semsim": sum(r["semsim"] for r in rs) / len(rs),
            "hit_at_k": sum(r["hit_at_k"] for r in rs) / len(rs),
            "n": len(rs),
        }
        for cat, rs in by_cat.items()
    }
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
    args = p.parse_args()

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

    out = {
        "meta": {
            "commit_sha": git_sha(),
            "predictions_file": str(args.predictions),
            "retrieval_only": args.retrieval_only,
            "semsim_mode": semsim_mode,
            **pred_doc.get("meta", {}),
        },
        "aggregate": summarize(per_qa),
        "per_qa": per_qa,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)
    agg = out["aggregate"]
    print(
        f"n={agg['n']}  em={agg['em']:.3f}  f1={agg['f1']:.3f}  "
        f"semsim={agg['semsim']:.3f}  hit@k={agg['hit_at_k']:.3f}"
    )
    print(f"wrote → {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
