#!/usr/bin/env python3
"""
cleanup_locomo_cubes.py — idempotent cleanup of LoCoMo test cubes before re-ingest.

Uses the conversation source file as the source of truth instead of
/product/list_cubes registry, since the registry is not populated by the
LoCoMo ingest flow (list_cubes returns empty → silent no-op if used for enum).

Cube IDs are derived deterministically from the conversation file the same way
ingest.py writes them: f"{conv_id}__speaker_{speaker_a/b}". Each cube_id also
doubles as user_id in the LoCoMo context.

Running twice is safe: second run issues delete_cube calls that return 404/410
(not_found) and exit cleanly with deleted=0, errors=[].

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
# Cube ID helpers — mirror ingest.py user_ids_for / first_two_speakers
# ---------------------------------------------------------------------------

class CubeInfo(NamedTuple):
    cube_id: str
    user_id: str


def user_ids_for(sample_id: str) -> tuple[str, str]:
    """Stable per-conv user IDs for both speakers (mirrors ingest.py).

    LOCOMO_CUBE_NAMESPACE env scopes cleanup to a per-session namespace.
    Cleanup MUST run with the same env value the ingest used, otherwise
    it either deletes nothing or — far worse — wipes another session's
    cubes. This is the wedge that prevents the conv-26 cross-session
    delete that bit us on 2026-04-29.
    """
    ns = os.getenv("LOCOMO_CUBE_NAMESPACE", "").strip()
    prefix = f"{ns}__" if ns else ""
    return f"{prefix}{sample_id}__speaker_a", f"{prefix}{sample_id}__speaker_b"


def cubes_from_conversations(conversations: list[dict]) -> list[CubeInfo]:
    """Build the full list of cube IDs from the conversation file.

    Mirrors the exact IDs that ingest.py writes: f"{sample_id}__speaker_a" /
    f"{sample_id}__speaker_b". cube_id == user_id in the LoCoMo context.
    """
    result: list[CubeInfo] = []
    for conv in conversations:
        sample_id = conv.get("sample_id", "locomo_unknown")
        ua, ub = user_ids_for(sample_id)
        result.append(CubeInfo(cube_id=ua, user_id=ua))
        result.append(CubeInfo(cube_id=ub, user_id=ub))
    return result


# ---------------------------------------------------------------------------
# Kept for ops debugging (not called by default cleanup flow).
# Not used in the main path — list_cubes registry is not populated by
# the LoCoMo ingest flow and returns {} for all LoCoMo user IDs.
# ---------------------------------------------------------------------------

def list_locomo_cubes_for_user(memdb_url: str, user_id: str) -> list[CubeInfo]:
    """Query /product/list_cubes for user_id (unused by default, kept for debugging)."""
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
            result.append(CubeInfo(cube_id=cid, user_id=oid or user_id))
    return result


# ---------------------------------------------------------------------------
# Delete logic
# ---------------------------------------------------------------------------

def delete_cube(memdb_url: str, cube_id: str, user_id: str, dry_run: bool) -> str:
    """Delete one cube. Returns 'deleted', 'not_found', 'dry_run', or 'error:<msg>'."""
    if dry_run:
        return "dry_run"
    url = f"{memdb_url.rstrip('/')}/product/delete_cube"
    try:
        resp = requests.post(
            url,
            json={"cube_id": cube_id, "user_id": user_id, "hard_delete": True},
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

    cubes = cubes_from_conversations(conversations)
    print(f"[cleanup] memdb_url={args.memdb_url!r} dry_run={args.dry_run}", flush=True)
    print(f"[cleanup] {len(cubes)} cubes to delete across {len(conversations)} conv(s)", flush=True)

    scanned = 0
    deleted = 0
    errors: list[str] = []

    for cube in cubes:
        scanned += 1
        outcome = delete_cube(args.memdb_url, cube.cube_id, cube.user_id, args.dry_run)
        tag = "[DRY-RUN]" if args.dry_run else ""
        print(f"  {tag} cube_id={cube.cube_id!r} user_id={cube.user_id!r} → {outcome}", flush=True)
        if outcome == "deleted":
            deleted += 1
        elif outcome.startswith("error:"):
            errors.append(f"{cube.cube_id}: {outcome}")

    summary = {"scanned": scanned, "deleted": deleted, "errors": errors}
    print(json.dumps(summary, indent=2))
    return 0 if not errors else 1


if __name__ == "__main__":
    sys.exit(main())
