#!/usr/bin/env python3
"""
query.py — run every LoCoMo QA against memdb-go, capture predictions.

For each question:
  1. GET /product/search  → retrieved memory texts (top-k).
  2. (optional) POST /product/chat/complete → full generated answer.

Predictions JSON layout:
  [
    {
      "conv_id": "...",
      "question_idx": 0,
      "question": "...",
      "gold_answer": "...",
      "evidence": ["D1:3"],
      "category": 2,
      "retrieved": [{"content": "...", "score": 0.87, "id": "..."}, ...],
      "chat_answer": "...",
      "search_ms": 123,
      "chat_ms": 456
    }, ...
  ]

Deterministic: QAs sorted by (conv_id, question_idx).

Category modes:
  --sample (default)         : 10 category-1 QAs from conv-26 (backward compat)
  --sample --categories=1,2,3,4,5 : 50 QAs from conv-26, 10 per category
  --full                     : all QAs from all 10 convs

Env:
  LOCOMO_CATEGORIES  comma-separated list (e.g. "1,2,3,4,5"). Overridden
                     by --categories.  Default: "1" (backward compat).
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time

from pathlib import Path

import requests


def build_headers() -> dict:
    """Auth headers from env: MEMDB_API_KEY (Bearer) or MEMDB_SERVICE_SECRET."""
    headers = {"Content-Type": "application/json"}
    if key := os.getenv("MEMDB_API_KEY"):
        headers["Authorization"] = f"Bearer {key}"
    if secret := os.getenv("MEMDB_SERVICE_SECRET"):
        headers["X-Service-Secret"] = secret
    return headers


EVAL_DIR = Path(__file__).resolve().parent
REPO_ROOT = EVAL_DIR.parent.parent
FULL_DATA = REPO_ROOT / "evaluation" / "data" / "locomo" / "locomo10.json"
SAMPLE_GOLD = EVAL_DIR / "sample_gold.json"

# LoCoMo category constants
ALL_CATEGORIES = {1, 2, 3, 4, 5}
# How many QAs to sample per category when building a balanced sample
CATEGORY_SAMPLE_SIZE = 10


def parse_categories(raw: str | None) -> list[int]:
    """Parse a comma-separated category string like '1,2,3,4,5' → [1, 2, 3, 4, 5].

    Returns the parsed list or raises ValueError on bad input.
    """
    if not raw:
        return [1]
    parts = [p.strip() for p in raw.split(",") if p.strip()]
    cats: list[int] = []
    for part in parts:
        try:
            c = int(part)
        except ValueError:
            raise ValueError(f"Invalid category {part!r} — must be an integer 1-5.") from None
        if c not in ALL_CATEGORIES:
            raise ValueError(f"Category {c} out of range — valid: 1-5.")
        if c not in cats:
            cats.append(c)
    return sorted(cats)


def load_gold(path: Path) -> list[dict]:
    with path.open() as f:
        return json.load(f)


def load_gold_from_locomo10(path: Path, categories: list[int] | None = None) -> list[dict]:
    """Load QAs from locomo10.json, optionally filtered to specific categories.

    When categories is None or [1,2,3,4,5], all QAs with a valid answer are
    returned.  When a subset is given (e.g. [1]), only QAs of those categories
    are included.

    Category 5 (adversarial) uses 'adversarial_answer' instead of 'answer'.
    Both fields are normalised to 'answer' in the returned records.
    """
    with path.open() as f:
        data = json.load(f)
    cat_filter = set(categories) if categories else None
    out: list[dict] = []
    for conv in data:
        sample_id = conv.get("sample_id", "locomo_unknown")
        raw_qas = conv.get("qa", [])
        qas_by_cat: dict[int, list[dict]] = {}
        for qa in raw_qas:
            cat = qa.get("category")
            if cat_filter and cat not in cat_filter:
                continue
            # Normalise answer field: category 5 uses 'adversarial_answer'
            answer = qa.get("answer") or qa.get("adversarial_answer") or ""
            if answer == "":
                continue
            qas_by_cat.setdefault(cat, []).append(
                {
                    "question": qa["question"],
                    "answer": answer,
                    "evidence": qa.get("evidence", []),
                    "category": cat,
                }
            )
        # Sort within each category for determinism; preserve category order
        q_idx = 0
        for cat in sorted(qas_by_cat.keys()):
            cat_qas = sorted(qas_by_cat[cat], key=lambda q: q["question"])
            for qa in cat_qas:
                out.append(
                    {
                        "conv_id": sample_id,
                        "question_idx": q_idx,
                        "question": qa["question"],
                        "answer": qa["answer"],
                        "evidence": qa["evidence"],
                        "category": qa["category"],
                    }
                )
                q_idx += 1
    return out


def build_sample_gold(
    path: Path, categories: list[int], per_cat: int = CATEGORY_SAMPLE_SIZE
) -> list[dict]:
    """Build a balanced sample from locomo10.json: up to per_cat QAs per category.

    Uses conv-26 (first conversation) as the fixed sample conversation so
    the ingest step only needs to push a single conversation.

    Deterministic: QAs within each category sorted alphabetically by question.
    """
    with path.open() as f:
        data = json.load(f)
    # Find conv-26 (first conversation in the dataset)
    conv = data[0]
    sample_id = conv.get("sample_id", "locomo_unknown")
    raw_qas = conv.get("qa", [])

    qas_by_cat: dict[int, list[dict]] = {}
    for qa in raw_qas:
        cat = qa.get("category")
        if cat not in set(categories):
            continue
        answer = qa.get("answer") or qa.get("adversarial_answer") or ""
        if answer == "":
            continue
        qas_by_cat.setdefault(cat, []).append(
            {
                "question": qa["question"],
                "answer": answer,
                "evidence": qa.get("evidence", []),
                "category": cat,
            }
        )

    out: list[dict] = []
    q_idx = 0
    for cat in sorted(qas_by_cat.keys()):
        cat_qas = sorted(qas_by_cat[cat], key=lambda q: q["question"])[:per_cat]
        for qa in cat_qas:
            out.append(
                {
                    "conv_id": sample_id,
                    "question_idx": q_idx,
                    "question": qa["question"],
                    "answer": qa["answer"],
                    "evidence": qa["evidence"],
                    "category": qa["category"],
                }
            )
            q_idx += 1
    return out


def extract_memory_items(payload) -> list[dict]:
    """Normalize /product/search response → list of {content, score, id}.

    memdb-go layout:
      data.text_mem[].memories[].memory
      data.pref_mem[].memories[].memory
      data.skill_mem[].memories[].memory
      data.tool_mem[].memories[].memory
    Also tolerant of older/flat layouts.
    """
    if not isinstance(payload, dict):
        return []
    data = payload.get("data", payload)
    items: list[dict] = []

    def pull_memory(m, memory_type):
        if not isinstance(m, dict):
            return
        metadata = m.get("metadata") or {}
        content = (
            m.get("memory")
            or metadata.get("memory")
            or metadata.get("context_summary")
            or m.get("content")
            or m.get("text")
            or ""
        )
        score = m.get("score") or m.get("relativity") or metadata.get("relativity")
        items.append(
            {
                "content": content,
                "score": score,
                "id": m.get("id") or m.get("memory_id") or metadata.get("id") or "",
                "type": memory_type,
            }
        )

    if isinstance(data, dict):
        for key in ("text_mem", "pref_mem", "skill_mem", "tool_mem"):
            buckets = data.get(key)
            if not isinstance(buckets, list):
                continue
            for bucket in buckets:
                if not isinstance(bucket, dict):
                    continue
                for m in bucket.get("memories") or []:
                    pull_memory(m, key)
        # flat fallbacks
        for key in (
            "long_term_memory",
            "user_memory",
            "working_memory",
            "memory",
            "memories",
            "results",
        ):
            val = data.get(key)
            if isinstance(val, list):
                for m in val:
                    pull_memory(m, key)
    elif isinstance(data, list):
        for m in data:
            pull_memory(m, "results")
    return items


def query_search(
    memdb_url: str,
    user_id: str,
    query: str,
    top_k: int,
    session_id: str | None,
    timeout: int = 60,
) -> tuple[list[dict], int]:
    payload = {
        "user_id": user_id,
        "query": query,
        "top_k": top_k,
        "mode": "fast",
        "include_preference": False,
        "search_tool_memory": False,
        "include_skill_memory": False,
    }
    if session_id:
        payload["session_id"] = session_id
    start = time.time()
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/search",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    elapsed_ms = int((time.time() - start) * 1000)
    resp.raise_for_status()
    items = extract_memory_items(resp.json())
    return items, elapsed_ms


def query_chat(
    memdb_url: str,
    user_id: str,
    query: str,
    top_k: int,
    timeout: int = 120,
) -> tuple[str, int]:
    payload = {
        "user_id": user_id,
        "query": query,
        "top_k": top_k,
        "mode": "fast",
        "include_preference": False,
    }
    start = time.time()
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/chat/complete",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    elapsed_ms = int((time.time() - start) * 1000)
    resp.raise_for_status()
    body = resp.json()
    # memdb-go wraps body as {"code", "message", "data"}; data can be str or dict
    if isinstance(body, dict):
        data = body.get("data", body)
        if isinstance(data, str):
            return data, elapsed_ms
        if isinstance(data, dict):
            for key in ("response", "answer", "content", "message", "text"):
                val = data.get(key)
                if isinstance(val, str) and val:
                    return val, elapsed_ms
    return json.dumps(body)[:2000], elapsed_ms


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    g = p.add_mutually_exclusive_group(required=True)
    g.add_argument("--sample", action="store_true")
    g.add_argument("--full", action="store_true")
    g.add_argument("--gold", type=Path, help="Custom gold JSON.")
    p.add_argument("--memdb-url", default="http://localhost:8080")
    p.add_argument("--top-k", type=int, default=20)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument(
        "--skip-chat",
        action="store_true",
        help="Only call /product/search, skip /product/chat/complete.",
    )
    p.add_argument(
        "--speaker",
        choices=["a", "b"],
        default="a",
        help="Which speaker user ID to query as (stable across runs).",
    )
    p.add_argument(
        "--categories",
        default=os.getenv("LOCOMO_CATEGORIES", ""),
        help=(
            "Comma-separated LoCoMo QA categories to include (default: '1', "
            "backward-compat single-hop only). Use '1,2,3,4,5' for the full "
            "5-category 50-QA sample. Env: LOCOMO_CATEGORIES."
        ),
    )
    args = p.parse_args()

    try:
        categories = parse_categories(args.categories)
    except ValueError as exc:
        print(f"ERROR: --categories: {exc}", file=sys.stderr)
        return 2

    multi_cat = len(categories) > 1
    cat_label = ",".join(str(c) for c in categories)

    if args.sample:
        if multi_cat:
            # Build balanced sample on-the-fly from locomo10.json
            gold = build_sample_gold(FULL_DATA, categories)
            print(
                f"[query] 5-category mode: {len(gold)} QAs from conv-26 "
                f"(categories {cat_label}, {CATEGORY_SAMPLE_SIZE} each)",
                flush=True,
            )
        else:
            # Default backward-compat path: use committed sample_gold.json
            gold = load_gold(SAMPLE_GOLD)
            print(f"[query] sample mode: {len(gold)} QAs (category-1 only)", flush=True)
    elif args.full:
        gold = load_gold_from_locomo10(FULL_DATA, categories if multi_cat else None)
        print(f"[query] full mode: {len(gold)} QAs (categories {cat_label})", flush=True)
    else:
        gold = load_gold(args.gold)
        print(f"[query] custom gold: {len(gold)} QAs from {args.gold}", flush=True)

    gold.sort(key=lambda g: (g["conv_id"], g["question_idx"]))

    predictions: list[dict] = []
    errors: list[str] = []
    for qi, qa in enumerate(gold, 1):
        user_id = f"{qa['conv_id']}__speaker_{args.speaker}"
        question = qa["question"]
        print(f"[{qi}/{len(gold)}] {qa['conv_id']} q={question[:70]!r}", flush=True)
        rec = {
            "conv_id": qa["conv_id"],
            "question_idx": qa["question_idx"],
            "question": question,
            "gold_answer": qa["answer"],
            "evidence": qa.get("evidence", []),
            "category": qa.get("category"),
            "retrieved": [],
            "chat_answer": None,
            "search_ms": None,
            "chat_ms": None,
            "error": None,
        }
        try:
            items, ms = query_search(
                args.memdb_url,
                user_id,
                question,
                args.top_k,
                session_id=None,
            )
            rec["retrieved"] = items
            rec["search_ms"] = ms
        except requests.RequestException as exc:
            rec["error"] = f"search: {exc}"
            errors.append(rec["error"])
            print(f"  SEARCH ERROR: {exc}", file=sys.stderr, flush=True)

        if not args.skip_chat and rec["error"] is None:
            try:
                answer, ms = query_chat(args.memdb_url, user_id, question, args.top_k)
                rec["chat_answer"] = answer
                rec["chat_ms"] = ms
            except requests.RequestException as exc:
                rec["error"] = f"chat: {exc}"
                errors.append(rec["error"])
                print(f"  CHAT ERROR: {exc}", file=sys.stderr, flush=True)

        predictions.append(rec)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w") as f:
        json.dump(
            {
                "predictions": predictions,
                "meta": {
                    "memdb_url": args.memdb_url,
                    "top_k": args.top_k,
                    "speaker": args.speaker,
                    "total": len(gold),
                    "errors": len(errors),
                    "skip_chat": args.skip_chat,
                    "categories": categories,
                },
            },
            f,
            indent=2,
            ensure_ascii=False,
        )
    print(f"wrote {len(predictions)} predictions → {args.out}")
    return 0 if not errors else 1


if __name__ == "__main__":
    sys.exit(main())
