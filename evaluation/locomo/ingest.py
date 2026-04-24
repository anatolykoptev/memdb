#!/usr/bin/env python3
"""
ingest.py — push LoCoMo conversations into memdb-go via /product/add.

Deterministic: conversations sorted by sample_id, sessions sorted by
key, messages in original order. One /product/add call per session.

Usage:
    python3 ingest.py --sample                         # 1 conv (category-1 mode, 10 QAs)
    python3 ingest.py --sample --categories=1,2,3,4,5  # 1 conv (5-category mode, 50 QAs)
    python3 ingest.py --full                            # all 10 convs
    python3 ingest.py --conversations <path.json>       # custom

Env:
    LOCOMO_CATEGORIES  comma-separated category list (default: "1").
                       Overridden by --categories.  Affects only which
                       gold QAs are sampled — ingest always pushes the
                       full conversation so every QA category has
                       evidence to retrieve.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time

from datetime import datetime, timezone
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
SAMPLE_DATA = EVAL_DIR / "sample_conversations.json"

# Category constants (LoCoMo categories 1-5)
ALL_CATEGORIES = {1, 2, 3, 4, 5}


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


def parse_session_date(raw: str | None) -> str | None:
    """Parse LoCoMo date strings like '1:56 pm on 8 May, 2023' → ISO8601 UTC."""
    if not raw:
        return None
    fmts = [
        "%I:%M %p on %d %B, %Y",
        "%I:%M %p on %d %B %Y",
        "%I:%M%p on %d %B, %Y",
    ]
    for fmt in fmts:
        try:
            dt = datetime.strptime(raw.strip(), fmt).replace(tzinfo=timezone.utc)
            return dt.isoformat()
        except ValueError:
            continue
    return None


def iter_sessions(conversation: dict):
    """Yield (session_key, iso_date, [messages]) sorted by session index."""
    session_keys = sorted(
        [k for k in conversation if k.startswith("session_") and not k.endswith("_date_time")],
        key=lambda k: int(re.search(r"(\d+)", k).group(1)) if re.search(r"(\d+)", k) else 0,
    )
    for key in session_keys:
        messages = conversation.get(key)
        if not isinstance(messages, list):
            continue
        iso_date = parse_session_date(conversation.get(f"{key}_date_time"))
        yield key, iso_date, messages


def user_ids_for(sample_id: str) -> tuple[str, str]:
    """Stable per-conv user IDs for both speakers (conversation is 2-speaker)."""
    return f"{sample_id}__speaker_a", f"{sample_id}__speaker_b"


def ingest_one_session(
    memdb_url: str,
    user_id: str,
    session_id: str,
    speaker_a: str,
    speaker_b: str,
    messages: list[dict],
    iso_date: str | None,
    perspective: str,
    timeout: int = 120,
) -> dict:
    """Post one session to /product/add."""
    chat_messages = []
    for m in messages:
        speaker = m.get("speaker", "")
        text = m.get("text", "")
        if not text:
            continue
        content = f"{speaker}: {text}"
        if perspective == "a":
            role = "user" if speaker == speaker_a else "assistant"
        else:
            role = "user" if speaker == speaker_b else "assistant"
        msg = {"role": role, "content": content}
        if iso_date:
            msg["chat_time"] = iso_date
        chat_messages.append(msg)

    if not chat_messages:
        return {"skipped": "empty"}

    payload = {
        "user_id": user_id,
        "session_id": session_id,
        "messages": chat_messages,
        "async_mode": "sync",  # deterministic: block until stored
        "mode": "fast",
    }
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/add",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    resp.raise_for_status()
    return resp.json()


def first_two_speakers(conversation: dict) -> tuple[str, str]:
    """Extract the two speakers appearing in the conversation."""
    seen: list[str] = []
    for _key, _date, messages in iter_sessions(conversation):
        for m in messages:
            sp = m.get("speaker")
            if sp and sp not in seen:
                seen.append(sp)
            if len(seen) >= 2:
                return seen[0], seen[1]
    if len(seen) == 1:
        return seen[0], seen[0] + "_other"
    return "speaker_a", "speaker_b"


def ingest(conversations: list[dict], memdb_url: str, dry_run: bool = False) -> dict:
    stats = {"conversations": 0, "sessions": 0, "messages": 0, "errors": []}
    conversations = sorted(conversations, key=lambda c: c.get("sample_id", ""))
    for conv in conversations:
        sample_id = conv.get("sample_id", "locomo_unknown")
        conversation = conv.get("conversation", {})
        speaker_a, speaker_b = first_two_speakers(conversation)
        user_a, user_b = user_ids_for(sample_id)
        print(f"[ingest] conv={sample_id} speakers=({speaker_a}, {speaker_b})", flush=True)
        for session_key, iso_date, messages in iter_sessions(conversation):
            session_id = f"{sample_id}__{session_key}"
            stats["sessions"] += 1
            stats["messages"] += len(messages)
            print(f"  session={session_key} msgs={len(messages)} date={iso_date}", flush=True)
            if dry_run:
                continue
            for perspective, uid in (("a", user_a), ("b", user_b)):
                try:
                    ingest_one_session(
                        memdb_url=memdb_url,
                        user_id=uid,
                        session_id=session_id,
                        speaker_a=speaker_a,
                        speaker_b=speaker_b,
                        messages=messages,
                        iso_date=iso_date,
                        perspective=perspective,
                    )
                except requests.RequestException as exc:
                    stats["errors"].append(f"{sample_id}/{session_key}/{perspective}: {exc}")
                    print(f"    ERROR: {exc}", file=sys.stderr, flush=True)
        stats["conversations"] += 1
    return stats


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    g = p.add_mutually_exclusive_group(required=True)
    g.add_argument("--sample", action="store_true", help="Use sample_conversations.json (1 conv).")
    g.add_argument("--full", action="store_true", help="Use full locomo10.json (10 convs).")
    g.add_argument("--conversations", type=Path, help="Path to a conversations JSON.")
    p.add_argument("--memdb-url", default="http://localhost:8080", help="memdb-go base URL.")
    p.add_argument("--dry-run", action="store_true", help="Parse only, do not POST.")
    p.add_argument(
        "--categories",
        default=os.getenv("LOCOMO_CATEGORIES", ""),
        help=(
            "Comma-separated QA categories to include in the gold sample "
            "(default: '1' = backward-compat single-hop only). "
            "Use '1,2,3,4,5' to enable the 5-category 50-QA mode. "
            "Ingest always pushes the full conversation regardless of this flag."
        ),
    )
    args = p.parse_args()

    try:
        categories = parse_categories(args.categories)
    except ValueError as exc:
        print(f"ERROR: --categories: {exc}", file=sys.stderr)
        return 2

    if args.sample:
        path = SAMPLE_DATA
    elif args.full:
        path = FULL_DATA
    else:
        path = args.conversations

    if not path.exists():
        print(f"ERROR: data file not found: {path}", file=sys.stderr)
        return 2

    with path.open() as f:
        data = json.load(f)
    # locomo10.json is a list; locomo10_rag.json is a dict
    conversations = list(data.values()) if isinstance(data, dict) else data

    cat_label = ",".join(str(c) for c in categories)
    print(f"[ingest] categories={cat_label!r} (ingest always pushes full conversation)", flush=True)

    start = time.time()
    stats = ingest(conversations, args.memdb_url, dry_run=args.dry_run)
    stats["duration_sec"] = round(time.time() - start, 2)
    stats["memdb_url"] = args.memdb_url
    stats["data_path"] = str(path)
    stats["categories"] = categories
    print(json.dumps(stats, indent=2))
    return 0 if not stats["errors"] else 1


if __name__ == "__main__":
    sys.exit(main())
