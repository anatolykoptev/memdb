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


def load_gold(path: Path) -> list[dict]:
    with path.open() as f:
        return json.load(f)


def load_gold_from_locomo10(path: Path) -> list[dict]:
    with path.open() as f:
        data = json.load(f)
    out: list[dict] = []
    for conv in data:
        sample_id = conv.get("sample_id", "locomo_unknown")
        qas = [qa for qa in conv.get("qa", []) if qa.get("answer") not in (None, "")]
        qas.sort(key=lambda q: (q.get("category", 99), q.get("question", "")))
        for q_idx, qa in enumerate(qas):
            out.append(
                {
                    "conv_id": sample_id,
                    "question_idx": q_idx,
                    "question": qa["question"],
                    "answer": qa["answer"],
                    "evidence": qa.get("evidence", []),
                    "category": qa.get("category"),
                }
            )
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
    args = p.parse_args()

    if args.sample:
        gold = load_gold(SAMPLE_GOLD)
    elif args.full:
        gold = load_gold_from_locomo10(FULL_DATA)
    else:
        gold = load_gold(args.gold)

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
