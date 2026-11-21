#!/usr/bin/env python3
"""eval_guard.py - target guard shared by every MemDB benchmark/eval/smoke harness.

A harness writes benchmark users and memories, so it must never run against a
real store. Three rules, enforced here once instead of in every harness:

1. No default target. The URL comes from --memdb-url or MEMDB_URL; with neither,
   the harness exits 2.
2. The target must say it is an eval instance. ``GET /health`` must report
   ``data.env == "eval"`` (the server sets it from MEMDB_ENV; unset means
   "prod"). Anything else - prod, a typo, an old build with no env field, an
   unreachable host - exits 3 before the first write.
3. Every request carries ``X-MemDB-Eval-Harness``, which a non-eval server
   rejects with 403, so a harness that skipped rule 2 still cannot write.

Bring up a throwaway eval instance with docker/docker-compose.eval.yml.

CLI (for shell harnesses):  python3 eval_guard.py check <url>
"""
from __future__ import annotations

import json
import sys
import urllib.error
import urllib.request

HARNESS_HEADER = "X-MemDB-Eval-Harness"
EXIT_NO_TARGET = 2
EXIT_NOT_EVAL = 3


class EvalGuardError(Exception):
    """The target is missing or is not an eval instance."""


def harness_headers() -> dict:
    """Marker header for every request a harness sends to MemDB."""
    return {HARNESS_HEADER: "1"}


def resolve_target(cli_value: str | None, env_value: str | None) -> str:
    """Return the explicit target (flag wins over env) or raise."""
    url = (cli_value or env_value or "").strip()
    if not url:
        raise EvalGuardError(
            "no target: pass --memdb-url or set MEMDB_URL. There is no default; "
            "start an isolated instance with docker/docker-compose.eval.yml."
        )
    return url


def reported_env(url: str, timeout: float = 5.0) -> str:
    """Return the ``data.env`` that ``GET <url>/health`` reports."""
    health = url.rstrip("/") + "/health"
    try:
        with urllib.request.urlopen(health, timeout=timeout) as resp:  # noqa: S310
            body = json.load(resp)
    except (urllib.error.URLError, OSError, ValueError) as exc:
        raise EvalGuardError(f"cannot read {health}: {exc}") from exc
    data = body.get("data") if isinstance(body, dict) else None
    env = data.get("env") if isinstance(data, dict) else None
    return env if isinstance(env, str) else ""


def check_eval_instance(url: str, timeout: float = 5.0) -> None:
    """Raise unless ``url`` reports ``env=eval``."""
    env = reported_env(url, timeout)
    if env != "eval":
        raise EvalGuardError(
            f"{url} reports env={env or '<none>'!r}, not 'eval'. Refusing to write "
            "benchmark data to it. Run the harness against an instance started "
            "with MEMDB_ENV=eval (docker/docker-compose.eval.yml)."
        )


def guard(cli_value: str | None, env_value: str | None = None) -> str:
    """Resolve the target and verify it; exit non-zero instead of returning on failure."""
    try:
        url = resolve_target(cli_value, env_value)
    except EvalGuardError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(EXIT_NO_TARGET)
    try:
        check_eval_instance(url)
    except EvalGuardError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(EXIT_NOT_EVAL)
    return url


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "check":
        guard(sys.argv[2])
        sys.exit(0)
    print("usage: eval_guard.py check <memdb-url>", file=sys.stderr)
    sys.exit(EXIT_NO_TARGET)
