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

F9 changes (mem0 arxiv pattern):
    --reverse-role     Ingest each conversation TWICE: once with the normal
                       speaker assignment, and once with speaker_a ↔ speaker_b
                       roles swapped. Each speaker becomes "user" from their own
                       POV in one pass, doubling fact coverage per the mem0
                       eval harness pattern. Default: true for new runs.
                       Use --no-reverse-role to reproduce pre-F9 baselines.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time

from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime, timezone
from pathlib import Path

import requests

# LOCOMO_INGEST_PARALLELISM caps client-side parallel /add HTTP calls.
# Default 1 = sequential (legacy behavior). Set to N>1 to fan out N session
# ingests concurrently. Effective ceiling is min(N, MEMDB_ADD_WORKERS server
# env). Server queue (MEMDB_ADD_QUEUE_SIZE=50) absorbs bursts; LLM proxy free
# tier sustains ~3 calls/s across all 17 keys via round-robin.
INGEST_PARALLELISM = int(os.getenv("LOCOMO_INGEST_PARALLELISM", "1"))

# LoCoMo ingest mode. Three modes are supported (per CLAUDE.md add-mode contract):
#   - "raw":  per-message granularity, no LLM. Best per-message cosine, but no
#             graph / atomic-fact enrichment. Use for pre-W2 baselines only.
#   - "fast": batched session-windowed chunking, no LLM. Was default 2026-04-30
#             → 2026-04-30 (replaced raw which starved wiki_pages clustering).
#   - "fine": full graph extraction — entities, atomic facts, edges, event_dates,
#             attributed_to, linked_memory_ids. Sync LLM call per session.
#             Default since 2026-04-30 paired with MEMDB_ATOMIC_FACTS=true:
#             cat-1 (single-hop) needs kind=atomic_fact; cat-2 (multi-hop)
#             needs linked_memory_ids; cat-3 (temporal) needs event_dates;
#             cat-4 (open-domain) needs attributed_to. LLM cost on Gemini Flash
#             Lite ~$0.66/1k QA — negligible vs retrieval cost (audited
#             2026-04-30). Resilience via fine→fast fallback (PR #246).
# Env LOCOMO_INGEST_MODE overrides; set "raw"/"fast" explicitly to reproduce older baselines.
INGEST_MODE = os.getenv("LOCOMO_INGEST_MODE", "fine")


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
    """Stable per-conv user IDs for both speakers (conversation is 2-speaker).

    LOCOMO_CUBE_NAMESPACE env optionally prepends a per-session prefix so
    parallel Claude / CI runs don't trample each other's cube data. Empty
    (default) preserves the legacy "<sample_id>__speaker_X" IDs that the
    test suite asserts against.
    """
    ns = os.getenv("LOCOMO_CUBE_NAMESPACE", "").strip()
    prefix = f"{ns}__" if ns else ""
    return f"{prefix}{sample_id}__speaker_a", f"{prefix}{sample_id}__speaker_b"


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
        "mode": INGEST_MODE,
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


def ingest(
    conversations: list[dict],
    memdb_url: str,
    dry_run: bool = False,
    reverse_role: bool = True,
) -> dict:
    """Ingest conversations into memdb-go.

    When ``reverse_role=True`` (default, F9 mem0 arxiv pattern), each
    conversation is ingested twice: once with normal speaker assignments and
    once with speaker_a ↔ speaker_b roles swapped.  This doubles the fact
    coverage per speaker's user store — each speaker becomes "user" from their
    own POV in one pass AND from the other speaker's POV in the swapped pass.
    The session_id for the swapped pass is suffixed with ``__rev`` to avoid
    collision with normal ingest sessions.

    Set ``reverse_role=False`` to reproduce pre-F9 baselines.
    """
    stats = {"conversations": 0, "sessions": 0, "messages": 0, "errors": [],
             "reverse_role": reverse_role, "reverse_role_sessions": 0,
             "parallelism": INGEST_PARALLELISM}
    conversations = sorted(conversations, key=lambda c: c.get("sample_id", ""))

    # Collect every (sample_id, session_key, perspective, kwargs, is_reverse)
    # job up-front so the parallel pool can fan them out without re-walking
    # the conversation tree per worker. Sequential mode runs the same list
    # through max_workers=1, so behavior is identical at parallelism=1.
    jobs = []
    for conv in conversations:
        sample_id = conv.get("sample_id", "locomo_unknown")
        conversation = conv.get("conversation", {})
        speaker_a, speaker_b = first_two_speakers(conversation)
        user_a, user_b = user_ids_for(sample_id)
        print(
            f"[ingest] conv={sample_id} speakers=({speaker_a}, {speaker_b}) "
            f"reverse_role={reverse_role} parallelism={INGEST_PARALLELISM}",
            flush=True,
        )
        for session_key, iso_date, messages in iter_sessions(conversation):
            session_id = f"{sample_id}__{session_key}"
            stats["sessions"] += 1
            stats["messages"] += len(messages)
            print(f"  session={session_key} msgs={len(messages)} date={iso_date}", flush=True)
            if dry_run:
                continue

            # Normal pass jobs (per-speaker POV).
            for perspective, uid in (("a", user_a), ("b", user_b)):
                jobs.append({
                    "sample_id": sample_id, "session_key": session_key,
                    "perspective": perspective, "is_reverse": False,
                    "kwargs": dict(memdb_url=memdb_url, user_id=uid,
                                   session_id=session_id,
                                   speaker_a=speaker_a, speaker_b=speaker_b,
                                   messages=messages, iso_date=iso_date,
                                   perspective=perspective),
                })

            # Reverse-role pass jobs (swap a↔b, __rev session suffix).
            if reverse_role:
                rev_session_id = f"{session_id}__rev"
                for perspective, uid in (("a", user_a), ("b", user_b)):
                    jobs.append({
                        "sample_id": sample_id, "session_key": session_key,
                        "perspective": perspective, "is_reverse": True,
                        "kwargs": dict(memdb_url=memdb_url, user_id=uid,
                                       session_id=rev_session_id,
                                       speaker_a=speaker_b, speaker_b=speaker_a,
                                       messages=messages, iso_date=iso_date,
                                       perspective=perspective),
                    })
        stats["conversations"] += 1

    if dry_run or not jobs:
        return stats

    # Group jobs by cube (user_id). content_hash dedup is per-cube, so two
    # concurrent adds against the SAME cube race: both LLM-extract the same
    # chunk, both try to insert, the UNIQUE constraint drops one silently and
    # the loser's facts are lost. Confirmed empirically on 2026-05-01: naive
    # parallel=10 dropped TOTAL rows from 431 (serial Flash 3.1) to 121 and
    # entire pet conversation (Oliver/Luna/Bailey) vanished.
    #
    # Solution: serial per cube, parallel between cubes. ThreadPool worker
    # count = number of distinct cubes (capped by INGEST_PARALLELISM). A LoCoMo
    # conversation has 2 cubes (speaker_a + speaker_b), so 1 conv → 2 workers
    # max; full corpus (10 conv) → 20 workers max.
    cubes: dict[str, list] = {}
    for job in jobs:
        cube = job["kwargs"]["user_id"]
        cubes.setdefault(cube, []).append(job)

    def _run_cube(cube_jobs):
        """Process all jobs for one cube serially. Returns (cube_id, [(job, err)])."""
        results = []
        for job in cube_jobs:
            try:
                ingest_one_session(**job["kwargs"])
                results.append((job, None))
            except requests.RequestException as exc:
                results.append((job, exc))
        return results

    workers = min(len(cubes), max(1, INGEST_PARALLELISM))
    print(f"[ingest] dispatching {len(jobs)} jobs across {len(cubes)} cubes "
          f"with {workers} parallel cube workers (serial within each cube to "
          f"avoid hash_dedup race)", flush=True)
    with ThreadPoolExecutor(max_workers=workers) as ex:
        for results in (f.result() for f in as_completed(ex.submit(_run_cube, cj) for cj in cubes.values())):
            for job, exc in results:
                if exc is not None:
                    tag = f"{job['sample_id']}/{job['session_key']}/{job['perspective']}"
                    if job["is_reverse"]:
                        tag += "/rev"
                    stats["errors"].append(f"{tag}: {exc}")
                    print(f"    ERROR{' (rev)' if job['is_reverse'] else ''}: {exc}",
                          file=sys.stderr, flush=True)
                elif job["is_reverse"]:
                    stats["reverse_role_sessions"] += 1

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
    p.add_argument(
        "--reverse-role",
        action="store_true",
        default=True,
        help=(
            "Ingest each conversation TWICE: once normal, once with speaker_a ↔ "
            "speaker_b roles swapped (mem0 pattern, doubles fact coverage). "
            "Default: enabled. Use --no-reverse-role for pre-F9 baselines."
        ),
    )
    p.add_argument(
        "--no-reverse-role",
        dest="reverse_role",
        action="store_false",
        help="Disable reverse-role ingest (pre-F9 baseline mode).",
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
    print(f"[ingest] mode={INGEST_MODE!r}", flush=True)
    print(f"[ingest] categories={cat_label!r} (ingest always pushes full conversation)", flush=True)
    print(f"[ingest] reverse_role={args.reverse_role}", flush=True)

    start = time.time()
    stats = ingest(
        conversations,
        args.memdb_url,
        dry_run=args.dry_run,
        reverse_role=args.reverse_role,
    )
    stats["duration_sec"] = round(time.time() - start, 2)
    stats["memdb_url"] = args.memdb_url
    stats["data_path"] = str(path)
    stats["categories"] = categories
    print(json.dumps(stats, indent=2))
    return 0 if not stats["errors"] else 1


if __name__ == "__main__":
    sys.exit(main())
