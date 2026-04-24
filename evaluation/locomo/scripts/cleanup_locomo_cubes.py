#!/usr/bin/env python3
"""
cleanup_locomo_cubes.py — idempotent cleanup of LoCoMo test cubes before re-ingest.

Lists all mem_cubes whose owner_id matches locomo*__speaker_* pattern, then
hard-deletes them via POST /product/delete_cube. Running twice is safe: second
run finds no cubes and exits cleanly.

Usage:
    python3 cleanup_locomo_cubes.py --sample                  # 1 conv (default data)
    python3 cleanup_locomo_cubes.py --full                     # all 10 convs
    python3 cleanup_locomo_cubes.py --conversations PATH       # custom file
    python3 cleanup_locomo_cubes.py --sample --dry-run        # print without deleting
    python3 cleanup_locomo_cubes.py --sample --memdb-url URL  # custom URL
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import NamedTuple

import requests

# ---------------------------------------------------------------------------
# Paths — mirror ingest.py layout
# ---------------------------------------------------------------------------
SCRIPTS_DIR = Path(__file__).resolve().parent
EVAL_DIR = SCRIPTS_DIR.parent
REPO_ROOT = EVAL_DIR.parent.parent
FULL_DATA = REPO_ROOT / "evaluation" / "data" / "locomo" / "locomo10.json"
SAMPLE_DATA = EVAL_DIR / "sample_conversations.json"


# ---------------------------------------------------------------------------
# Auth — duplicated from ingest.py (small duplication preferred over
# refactoring ingest internals in this PR).
# ---------------------------------------------------------------------------
def build_headers() -> dict:
    """Auth headers from env: MEMDB_API_KEY (Bearer) or MEMDB_SERVICE_SECRET."""
    headers = {"Content-Type": "application/json"}
    if key := os.getenv("MEMDB_API_KEY"):
        headers["Authorization"] = f"Bearer {key}"
    if secret := os.getenv("MEMDB_SERVICE_SECRET"):
        headers["X-Service-Secret"] = secret
    return headers


# ---------------------------------------------------------------------------
# Cube enumeration via /product/list_cubes
# ---------------------------------------------------------------------------

class CubeInfo(NamedTuple):
    cube_id: str
    owner_id: str


def list_locomo_cubes_for_user(memdb_url: str, user_id: str) -> list[CubeInfo]:
    """Return cubes owned by user_id that look like LoCoMo test cubes."""
    url = f"{memdb_url.rstrip('/')}/product/list_cubes"
    try:
        resp = requests.post(
            url,
            json={"owner_id": user_id},
            headers=build_headers(),
            timeout=30,
        )
        resp.raise_for_status()
    except requests.RequestException as exc:
        print(f"  WARN: list_cubes for {user_id!r} failed: {exc}", file=sys.stderr)
        return []

    body = resp.json()
    cubes_raw = body.get("data", {}).get("cubes", [])
    result: list[CubeInfo] = []
    for c in cubes_raw:
        cid = c.get("cube_id") or c.get("id", "")
        oid = c.get("owner_id", "")
        if cid:
            result.append(CubeInfo(cube_id=cid, owner_id=oid or user_id))
    return result


# ---------------------------------------------------------------------------
# Speaker ID helpers — mirror ingest.py user_ids_for
# ---------------------------------------------------------------------------

def user_ids_for(sample_id: str) -> tuple[str, str]:
    """Stable per-conv user IDs for both speakers."""
    return f"{sample_id}__speaker_a", f"{sample_id}__speaker_b"


def collect_user_ids(conversations: list[dict]) -> list[str]:
    """Return all (user_a, user_b) pairs across conversations."""
    ids: list[str] = []
    for conv in conversations:
        sample_id = conv.get("sample_id", "locomo_unknown")
        ua, ub = user_ids_for(sample_id)
        ids.extend([ua, ub])
    return ids


# ---------------------------------------------------------------------------
# Delete logic
# ---------------------------------------------------------------------------

def delete_cube(memdb_url: str, cube_id: str, owner_id: str, dry_run: bool) -> str:
    """Delete one cube. Returns 'deleted', 'not_found', 'dry_run', or 'error:<msg>'."""
    if dry_run:
        return "dry_run"
    url = f"{memdb_url.rstrip('/')}/product/delete_cube"
    try:
        resp = requests.post(
            url,
            json={"cube_id": cube_id, "user_id": owner_id, "hard_delete": True},
            headers=build_headers(),
            timeout=30,
        )
    except requests.RequestException as exc:
        return f"error:{exc}"

    if resp.status_code in (404, 410):
        return "not_found"
    if resp.status_code == 200:
        return "deleted"
    return f"error:http_{resp.status_code}"


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    g = p.add_mutually_exclusive_group(required=True)
    g.add_argument("--sample", action="store_true", help="Use sample_conversations.json (1 conv).")
    g.add_argument("--full", action="store_true", help="Use full locomo10.json (10 convs).")
    g.add_argument("--conversations", type=Path, help="Path to a conversations JSON.")
    p.add_argument("--memdb-url", default="http://localhost:8080", help="memdb-go base URL.")
    p.add_argument("--dry-run", action="store_true", help="List cubes but do not delete.")
    args = p.parse_args()

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
    conversations = list(data.values()) if isinstance(data, dict) else data

    user_ids = collect_user_ids(conversations)
    print(f"[cleanup] memdb_url={args.memdb_url!r} dry_run={args.dry_run}", flush=True)
    print(f"[cleanup] scanning {len(user_ids)} user IDs across {len(conversations)} conv(s)", flush=True)

    scanned = 0
    deleted = 0
    errors: list[str] = []

    for uid in user_ids:
        cubes = list_locomo_cubes_for_user(args.memdb_url, uid)
        scanned += len(cubes)
        for cube in cubes:
            outcome = delete_cube(args.memdb_url, cube.cube_id, cube.owner_id, args.dry_run)
            tag = "[DRY-RUN]" if args.dry_run else ""
            print(f"  {tag} cube_id={cube.cube_id!r} owner={cube.owner_id!r} → {outcome}", flush=True)
            if outcome in ("deleted", "dry_run", "not_found"):
                if outcome == "deleted":
                    deleted += 1
            else:
                errors.append(f"{cube.cube_id}/{cube.owner_id}: {outcome}")

    summary = {"scanned": scanned, "deleted": deleted, "errors": errors}
    print(json.dumps(summary, indent=2))
    return 0 if not errors else 1


if __name__ == "__main__":
    sys.exit(main())
