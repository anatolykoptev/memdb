#!/usr/bin/env python3
"""
query.py — run every LoCoMo QA against memdb-go, capture predictions.

For each question:
  1. GET /product/search  → retrieved memory texts (top-k).
  2. (optional) POST /product/chat/complete → full generated answer.

Predictions JSON layout (dual-speaker mode, M9+):
  [
    {
      "conv_id": "...",
      "question_idx": 0,
      "question": "...",
      "gold_answer": "...",
      "evidence": ["D1:3"],
      "category": 2,
      "retrieved": [{"content": "...", "score": 0.87, "id": "...",
                     "speaker_label": "A"}, ...],
      "chat_answer": "...",
      "search_ms": 123,
      "chat_ms": 456,
      "dual_speaker": true,
      "speaker_a_memories": [...],
      "speaker_b_memories": [...],
      "merged_top_k": 20,
      "top_k_per_speaker": 30
    }, ...
  ]

When LOCOMO_DUAL_SPEAKER=false the dual-speaker fields (`speaker_*_memories`,
`merged_top_k`, `speaker_label` on retrieved items) are omitted; the rest of
the schema matches the M7/M8 single-speaker baseline.

Deterministic: QAs sorted by (conv_id, question_idx).

Category modes:
  --sample (default)         : 10 category-1 QAs from conv-26 (backward compat)
  --sample --categories=1,2,3,4,5 : 50 QAs from conv-26, 10 per category
  --full                     : all QAs from all 10 convs

Env:
  LOCOMO_CATEGORIES   comma-separated list (e.g. "1,2,3,4,5"). Overridden
                      by --categories.  Default: "1" (backward compat).
  LOCOMO_DUAL_SPEAKER true|false (default: true). Fan-out per question to
                      BOTH `<conv>__speaker_a` and `<conv>__speaker_b` user
                      stores in parallel and merge results. Set to `false`
                      (or `0`) to reproduce M7/M8 single-speaker baselines.
                      M9 Stream 1 (HARNESS-DUAL).

F9 changes (mem0 arxiv pattern):
  --top-k-per-speaker N  Per-speaker top_k for dual-speaker search (default 30,
                         matching mem0 eval harness). Each speaker is queried
                         independently with this budget; results are merged and
                         presented to chat with timestamp prefixes so the LLM
                         gets a time anchor per fact.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import re
import sys
import time
import threading

from pathlib import Path
from typing import Any, Callable, TypeVar

import requests

# LoCoMo's raw-mode memories are short verbatim dialogue turns ("Caroline: Hey Mel!").
# Question-form queries match these at cosine ~0.15-0.30, well below the
# DefaultRelativity=0.5 threshold in memdb-go/internal/search/config.go.
# Override to 0.0 so retrieval surfaces raw turns and the harness can score
# the actual hit@k. Production clients keep the server-side defaults.
#
# Field-name inconsistency in memdb-go server:
#   /product/search         → searchRequest.Relativity  → field "relativity"
#   /product/chat/complete  → nativeChatRequest.Threshold *float64 → field "threshold"
# Both represent the same concept (cosine floor before returning memories) but
# the chat endpoint does NOT parse "relativity" — it silently drops it and falls
# back to a hardcoded 0.30 post-filter in chatSearchMemories. We must send the
# right field to each endpoint, controlled by a single env var here.
#
# Env priority: LOCOMO_RETRIEVAL_THRESHOLD → LOCOMO_SEARCH_RELATIVITY (legacy) → 0.0
_raw_threshold = os.getenv("LOCOMO_RETRIEVAL_THRESHOLD") or os.getenv("LOCOMO_SEARCH_RELATIVITY", "0.0")
LOCOMO_RETRIEVAL_THRESHOLD = float(_raw_threshold)


# M9 Stream 1 (HARNESS-DUAL): dual-speaker retrieval per question.
# When enabled (default), each question fans out to BOTH `<conv>__speaker_a`
# and `<conv>__speaker_b` user stores in parallel, merges the result lists
# (dedup by memory id, keep higher score), and presents the combined context
# to the chat endpoint with explicit `[speaker:A]` / `[speaker:B]` provenance
# markers in the system prompt.  Mirrors Memobase's reference benchmark
# implementation (see compete-research/memobase/.../memobase_search.py:71-105).
#
# Disable with `LOCOMO_DUAL_SPEAKER=false` (or `0`) to reproduce M7/M8
# single-speaker baselines.  Case-insensitive parse.
def _parse_dual_speaker_env(raw: str | None) -> bool:
    if raw is None:
        return True
    return raw.strip().lower() not in {"false", "0", "no", "off", ""}


LOCOMO_DUAL_SPEAKER = _parse_dual_speaker_env(os.getenv("LOCOMO_DUAL_SPEAKER"))

# Suppresses server-side retrieval in chat: above this cosine score, server
# returns nothing extra so we rely on our pre-fetched dual-speaker context.
_CHAT_RETRIEVAL_SUPPRESS_THRESHOLD = 0.99

# Error substrings that indicate a transient 500 (safe to retry).
# Permanent 500s (e.g. "invalid user", "not found") must NOT be retried.
_TRANSIENT_500_PATTERNS = (
    "Internal Server Error",
    "request canceled",
    "context deadline exceeded",
    "connection reset by peer",
    "EOF",
    "upstream connect error",
)


def _is_transient_http_error(exc: requests.HTTPError) -> bool:
    """Return True if the HTTPError is safe to retry.

    Retries on 502, 503, 504 unconditionally.
    Retries on 500 only when the response body / exception message matches a
    known-transient pattern (see _TRANSIENT_500_PATTERNS).
    """
    resp: requests.Response | None = exc.response
    if resp is None:
        return False
    code: int = resp.status_code
    if code in (502, 503, 504):
        return True
    if code == 500:
        body: str = ""
        try:
            body = resp.text or ""
        except Exception:
            pass
        msg = str(exc)
        combined = (body + " " + msg).lower()
        return any(p.lower() in combined for p in _TRANSIENT_500_PATTERNS)
    return False


def _is_retryable(exc: Exception) -> bool:
    """Return True if the exception is worth retrying."""
    if isinstance(exc, (requests.exceptions.ConnectionError, requests.exceptions.Timeout)):
        return True
    if isinstance(exc, requests.exceptions.HTTPError):
        return _is_transient_http_error(exc)
    return False


def _short_reason(exc: Exception) -> str:
    """One-line human-readable reason for a retry."""
    if isinstance(exc, requests.exceptions.Timeout):
        return "timeout"
    if isinstance(exc, requests.exceptions.ConnectionError):
        return "connection_error"
    if isinstance(exc, requests.exceptions.HTTPError):
        resp = exc.response
        if resp is not None:
            return f"http_{resp.status_code}"
    return type(exc).__name__


# ---------------------------------------------------------------------------
# Per-run retry counters (thread-safe)
# ---------------------------------------------------------------------------
# Note: a module-level singleton (`_RETRY_STATS`) is used for backwards-compat
# with `print_summary()` at end of `main()`.  Test isolation: tests that need
# a clean slate either instantiate a fresh `_RetryStats()` (preferred — see
# tests/test_query_retry.py::TestRetryStats) or call `_RETRY_STATS.reset()`.
# Do not assume zeroed counters between unit-test cases.
class _RetryStats:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.attempts: int = 0
        self.recovered: int = 0
        self.final_errors: int = 0

    def record_attempt(self) -> None:
        with self._lock:
            self.attempts += 1

    def record_recovered(self) -> None:
        with self._lock:
            self.recovered += 1

    def record_final_error(self) -> None:
        with self._lock:
            self.final_errors += 1

    def reset(self) -> None:
        """Zero all counters under the lock. Test helper."""
        with self._lock:
            self.attempts = 0
            self.recovered = 0
            self.final_errors = 0

    def print_summary(self) -> None:
        pct = (
            f"{100 * self.recovered / self.attempts:.0f}%"
            if self.attempts
            else "n/a"
        )
        print(
            "[locomo retry summary]\n"
            f"  retries_attempted={self.attempts}\n"
            f"  retries_recovered={self.recovered} ({pct})\n"
            f"  final_errors={self.final_errors}",
            file=sys.stderr,
            flush=True,
        )


_RETRY_STATS = _RetryStats()

# DeadlineExceeded — raised by `_call_with_retry` when the cumulative wall-clock
# spent on a single call (across attempts + backoff sleeps) crosses the
# `deadline` budget passed by the caller.  Distinct from
# `requests.exceptions.Timeout` so callers can differentiate per-attempt
# socket timeouts from per-call overall budget exhaustion.
class DeadlineExceeded(Exception):
    """Raised when `_call_with_retry` exceeds its overall wall-clock deadline."""


T = TypeVar("T")


def _call_with_retry(
    fn: Callable[..., T],
    *args: Any,
    max_retries: int = 2,
    backoff_base: float = 5.0,
    deadline: float | None = None,
    cat: str = "",
    qid: str = "",
    stats: _RetryStats | None = None,
    **kwargs: Any,
) -> T:
    """Call ``fn(*args, **kwargs)`` with automatic retry on transient errors.

    Parameters
    ----------
    fn:
        Callable to invoke.
    max_retries:
        Maximum number of retry attempts (0 = no retries, matches legacy behaviour).
    backoff_base:
        Sleep duration in seconds before the first retry; doubles on each
        subsequent attempt (5 → 10 → 20 …).
    deadline:
        Optional overall wall-clock budget in seconds.  When set, the helper
        refuses to start a new attempt or sleep past `start + deadline` and
        raises :class:`DeadlineExceeded` (chained to the last seen exception
        when there is one).  ``None`` (default) preserves legacy behaviour
        where only the per-attempt socket timeout applies.
    cat, qid:
        Metadata logged to stderr on each retry attempt.
    stats:
        Optional :class:`_RetryStats` instance to record into.  Defaults to
        the module-level singleton ``_RETRY_STATS`` so existing call-sites
        keep working unchanged.

    Returns
    -------
    The return value of ``fn``.

    Raises
    ------
    Re-raises the last exception when all retries are exhausted, or
    :class:`DeadlineExceeded` if the per-call deadline is hit first.
    """
    counters = stats if stats is not None else _RETRY_STATS
    last_exc: Exception | None = None
    delay = backoff_base
    start_ts = time.monotonic()
    for attempt in range(max_retries + 1):
        if deadline is not None and (time.monotonic() - start_ts) >= deadline:
            err = DeadlineExceeded(
                f"per-call deadline {deadline:.1f}s exceeded before attempt={attempt + 1} "
                f"cat={cat} qid={qid}"
            )
            if last_exc is not None:
                raise err from last_exc
            raise err
        try:
            result = fn(*args, **kwargs)
            if attempt > 0:
                # Successful recovery after at least one retry
                counters.record_recovered()
            return result
        except Exception as exc:
            if attempt < max_retries and _is_retryable(exc):
                counters.record_attempt()
                reason = _short_reason(exc)
                print(
                    f"[retry attempt={attempt + 1} cat={cat} qid={qid} reason={reason}]",
                    file=sys.stderr,
                    flush=True,
                )
                # Honour the deadline BEFORE we sleep — sleeping past the
                # budget would leak wall-clock to no benefit.
                if deadline is not None:
                    remaining = deadline - (time.monotonic() - start_ts)
                    if remaining <= 0:
                        if attempt > 0:
                            counters.record_final_error()
                        raise DeadlineExceeded(
                            f"per-call deadline {deadline:.1f}s exceeded after attempt={attempt + 1} "
                            f"cat={cat} qid={qid}"
                        ) from exc
                    sleep_for = min(delay, remaining)
                else:
                    sleep_for = delay
                time.sleep(sleep_for)
                delay *= 2
                last_exc = exc
                continue
            # Not retryable or exhausted — propagate
            if attempt > 0:
                counters.record_final_error()
            raise
    # Should be unreachable, but satisfy type checker
    raise last_exc  # type: ignore[misc]


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


def _extract_ts(metadata: dict) -> str | None:
    """Extract a timestamp string from memory item metadata (best-effort).

    Tries common field names in priority order.  Returns None when no usable
    timestamp is found so callers can skip the prefix rather than emit an
    empty/uninformative one.

    Accepts either an ISO8601 string ("2023-05-08T13:56:00Z") or a numeric
    epoch (seconds or milliseconds — autodetected by magnitude).  Numeric
    epochs are normalised to "YYYY-MM-DD" UTC.  Out-of-range or unparseable
    values are skipped, matching the historic str-only behaviour.

    M12.1 priority change: ``observation_date`` is checked FIRST. memdb-go's
    add pipeline (M12.1) stamps it on memory props as the latest source
    message's chat_time (YYYY-MM-DD), surviving the ``sources`` strip done by
    search/response.go::FormatMemoryItem. ``chat_time`` is kept second for
    forward-compat with future server changes that surface it directly. Then
    ``created_at`` (ingest wall-clock) and the legacy fallbacks remain.
    """
    for key in ("observation_date", "chat_time", "created_at", "created_time", "time", "date"):
        val = metadata.get(key)
        if val is None or val == "":
            continue
        if isinstance(val, str):
            # Trim to date-only part (first 10 chars of ISO8601) for brevity.
            # "2023-05-08T13:56:00Z" → "2023-05-08"
            return val[:10] if len(val) >= 10 else val
        if isinstance(val, (int, float)) and not isinstance(val, bool):
            # Heuristic: ≥ 1e12 → epoch milliseconds; ≥ 1e9 → epoch seconds.
            # Reject anything outside [1e9, 1e13] BEFORE normalising — that
            # window covers 2001-09-09 through 2286-11-20, which is comfortably
            # past LoCoMo's range without admitting Y10K-style overflow.
            num = float(val)
            if num < 1e9 or num > 1e13:
                continue
            if num >= 1e12:  # ms → s
                num /= 1000.0
            try:
                return time.strftime("%Y-%m-%d", time.gmtime(num))
            except (OverflowError, ValueError, OSError):
                continue
    return None


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
        ts = _extract_ts(metadata)
        # M9 server-side dual-speaker stamps metadata.speaker_label so the
        # harness can split the merged result back into per-speaker buckets
        # for prompt assembly. Falls through to "" when single-speaker.
        speaker_label = metadata.get("speaker_label", "") if isinstance(metadata, dict) else ""
        items.append(
            {
                "content": content,
                "score": score,
                "id": m.get("id") or m.get("memory_id") or metadata.get("id") or "",
                "type": memory_type,
                "ts": ts,
                "speaker_label": speaker_label,
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
        "relativity": LOCOMO_RETRIEVAL_THRESHOLD,  # searchRequest.Relativity field
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
        "threshold": LOCOMO_RETRIEVAL_THRESHOLD,  # nativeChatRequest.Threshold field (NOT "relativity")
        # Use server-side factual QA prompt (Stream A: answer_style=factual in buildSystemPrompt).
        # This exercises the production code path instead of the harness-side QA_SYSTEM_PROMPT
        # override that was used during M6 ablation experiments.
        "answer_style": "factual",
        # Token-budget memories block. Default raised 2000→8000 (2026-05-01
        # forensic): old 2000 cap was double-trimming dual-speaker payload that
        # harness already pre-trimmed via top_k=20 — bridging memories at rank
        # 4-8 silently dropped (cat-2 multi-hop F1 hit).  Set
        # LOCOMO_MAX_CONTEXT_TOKENS=0 for no cap, or override to any int.
        "max_context_tokens": int(os.getenv("LOCOMO_MAX_CONTEXT_TOKENS", "8000")),
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


# --- M9 Stream 1: dual-speaker retrieval helpers -------------------------------

def _speaker_user_ids(conv_id: str) -> tuple[str, str]:
    """Return `(speaker_a_uid, speaker_b_uid)` for a LoCoMo conv id.

    LOCOMO_CUBE_NAMESPACE env optionally prepends a per-session prefix —
    must match the value used by ingest.py / cleanup_locomo_cubes.py for
    the run, otherwise query targets the wrong cubes.
    """
    ns = os.getenv("LOCOMO_CUBE_NAMESPACE", "").strip()
    prefix = f"{ns}__" if ns else ""
    return f"{prefix}{conv_id}__speaker_a", f"{prefix}{conv_id}__speaker_b"


def _merge_dual_results(
    speaker_a_items: list[dict],
    speaker_b_items: list[dict],
    top_k: int,
) -> list[dict]:
    """Merge dual-speaker search results.

    Rules:
      * Dedup by memory `id`.  When the same id appears in both speakers'
        lists, keep the higher `score` and remember which speaker contributed
        the winning copy via `speaker_label` ("A" or "B").
      * Items with empty/None id are kept as distinct entries (best-effort
        — no reasonable dedup key available); they still get `speaker_label`.
      * Each merged item carries `speaker_label` provenance.
      * Result is sorted by score (descending; missing/None score → 0.0)
        and truncated to `top_k`.
    """
    by_id: dict[str, dict] = {}
    no_id: list[dict] = []

    def _ingest(items: list[dict], label: str) -> None:
        for it in items:
            tagged = dict(it)
            tagged["speaker_label"] = label
            mid = tagged.get("id") or ""
            if not mid:
                no_id.append(tagged)
                continue
            existing = by_id.get(mid)
            if existing is None:
                by_id[mid] = tagged
                continue
            # Higher score wins; treat None as 0.0 for comparison.
            existing_score = existing.get("score") or 0.0
            new_score = tagged.get("score") or 0.0
            if new_score > existing_score:
                by_id[mid] = tagged

    _ingest(speaker_a_items, "A")
    _ingest(speaker_b_items, "B")

    merged = list(by_id.values()) + no_id
    merged.sort(key=lambda m: (m.get("score") or 0.0), reverse=True)
    return merged[:top_k]


def query_search_dual(
    memdb_url: str,
    conv_id: str,
    query: str,
    top_k: int,
    session_id: str | None = None,
    timeout: int = 60,
    top_k_per_speaker: int = 30,
) -> tuple[list[dict], list[dict], list[dict], int]:
    """Dual-speaker fan-out via server-side M9 path (single POST).

    Sets `speakers: [<a>, <b>]` + `top_k_per_speaker` so memdb-go runs the
    fan-out / per-speaker tagging / merge pipeline server-side
    (internal/handlers/chat_dual_speaker.go::handleDualSpeakerSearch).
    Each returned memory carries `metadata.speaker_label` which we use to
    split the merged list back into per-speaker buckets for downstream
    prompt assembly.

    LOCOMO_DUAL_CLIENT=1 forces the legacy two-thread client-side fan-out
    (kept as a safety toggle while we observe the server path under load).

    Returns `(speaker_a_items, speaker_b_items, merged_items, elapsed_ms)`.
    """
    uid_a, uid_b = _speaker_user_ids(conv_id)
    start = time.time()

    if os.getenv("LOCOMO_DUAL_CLIENT", "").lower() in ("1", "true", "yes"):
        # Legacy client-side fan-out path.
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            fut_a = pool.submit(
                query_search, memdb_url, uid_a, query, top_k_per_speaker, session_id, timeout
            )
            fut_b = pool.submit(
                query_search, memdb_url, uid_b, query, top_k_per_speaker, session_id, timeout
            )
            items_a, _ = fut_a.result()
            items_b, _ = fut_b.result()
        elapsed_ms = int((time.time() - start) * 1000)
        merged = _merge_dual_results(items_a, items_b, top_k)
        return items_a, items_b, merged, elapsed_ms

    # Server-side M9: single POST with speakers fan-out.
    payload = {
        "query": query,
        "top_k": top_k,
        "mode": "fast",
        "include_preference": False,
        "search_tool_memory": False,
        "include_skill_memory": False,
        "relativity": LOCOMO_RETRIEVAL_THRESHOLD,
        "speakers": [uid_a, uid_b],
        "top_k_per_speaker": top_k_per_speaker,
        "merge_strategy": "interleave",
    }
    if session_id:
        payload["session_id"] = session_id
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/search",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    elapsed_ms = int((time.time() - start) * 1000)
    resp.raise_for_status()
    merged = extract_memory_items(resp.json())
    items_a: list[dict] = []
    items_b: list[dict] = []
    for it in merged:
        label = it.get("speaker_label", "")
        if label == uid_a:
            items_a.append(it)
        elif label == uid_b:
            items_b.append(it)
    return items_a, items_b, merged, elapsed_ms


def _conversation_now(items: list[dict]) -> str | None:
    """Return the latest in-conversation date from a flat list of retrieved items.

    Used by `_build_dual_speaker_system_prompt` as the "Current time:" anchor.
    Picks the max non-None `ts` (already YYYY-MM-DD via _extract_ts).
    Returns None when no item carries a usable ts — caller falls back to env
    override or omits the line entirely.

    M12.1: replaces the previous `time.strftime("%Y-%m-%d %H:%M (%A)")` which
    poisoned LoCoMo cat-2 by anchoring "last week" / "yesterday" against
    server NOW (2026-04-26..28) instead of the conversation's own timeline
    (2022/2023). 42.7% of cat-2 misses traced to this single line — see
    `docs/superpowers/plans/2026-04-29-m12-recovery-analysis.md` (S1).
    """
    latest: str | None = None
    for it in items:
        ts = it.get("ts")
        if not ts or not isinstance(ts, str):
            continue
        if latest is None or ts > latest:
            latest = ts
    return latest


def _category_brevity_instruction(category: int | None) -> str:
    """Return the length-target line for the given LoCoMo question category.

    Core rules (no echo, no filler, always attempt) are constant; only the
    permitted answer form varies.

    cat=1  single-hop   → noun / number / name, 1-3 words
    cat=2  multi-hop    → date or relative phrase ("3 months ago")
    cat=3  temporal     → exact date or relative phrase from the memory text
    cat=4  open-domain  → 1 brief clause max (feelings, opinions, descriptions)
    cat=5  adversarial  → "Yes" or "No" + 1-clause justification if context
                          supports it; "uncertain" only when context is absent
    default             → current uniform brevity rule (back-compat)
    """
    # 2026-05-01 — Run #10 evidence: cat-aware specialization HURT cat2/3/4
    # (-10 to -17pp) — too restrictive on narrative flexibility. Only cat1
    # specialization showed clean +9.4pp win (proper-noun lookups truly want
    # 1-3 word answers). Reverted cat2-5 back to uniform default brevity.
    if category == 1:
        return (
            "Answer with a noun phrase, number, or name — 1-3 words. "
            "No full sentences."
        )
    # cat2/3/4/5 + None → uniform brevity (Run #8 baseline, F1=0.284 best).
    #
    # Karpathy r3 forensic (2026-05-01) fix #4: dropped the second sentence
    # ("If the memories do not contain the answer, say so plainly in 3 words
    # or fewer."). The server-side "## Answer Rules" block (injected by
    # buildFactualRulesBlock when answer_style=factual + custom system_prompt)
    # already owns the refusal contract. Carrying a second refusal instruction
    # in the harness tail caused the LLM to pick the strictest contract and
    # refuse despite the gold being in context — measured on Run #18 as 50%
    # of F1 regressions vs the Run #17 brevity-pattB peak. The brevity tail
    # now ONLY enforces answer length; refusal-bias is server-side only.
    return (
        "Answer in as few words as possible — prefer a noun phrase, number, "
        "or single date over a full sentence unless the question explicitly asks for explanation."
    )


def _build_dual_speaker_system_prompt(
    speaker_a_items: list[dict],
    speaker_b_items: list[dict],
    category: int | None = None,
) -> str:
    """Assemble a system prompt that exposes both speakers' memories.

    Mirrors Memobase's `answer_question` template (memobase_search.py:81-88):
    each speaker's memories are presented as a labelled block so the model
    cannot "miss" cross-speaker evidence.

    M12.1 — temporal anchor fix:
      1. `Current time:` line is derived from the LATEST `ts` across retrieved
         memories (the in-conversation date), not server wall-clock.
      2. Per-memory `ts:<date>` prefix is kept (mem0 arxiv pattern for cat-2
         multi-hop), but `ts` now resolves to `observation_date` /
         `chat_time` from server-side metadata (M12.1 add-pipeline change),
         so the value is the real conversation date — not `time.Now()` at
         ingest as in PR #155 / M11.
      3. Override priority: `LOCOMO_CONV_NOW` env (operator pin) >
         max(retrieved.ts) > omit the header. NEVER falls back to `time.Now()`
         — that was the M11 regression vector.
    """
    def _fmt(items: list[dict], label: str) -> str:
        if not items:
            return f"[speaker:{label}] (no memories retrieved)"
        lines = [f"[speaker:{label}]"]
        for i, it in enumerate(items, 1):
            content = (it.get("content") or "").strip()
            if not content:
                continue
            ts = it.get("ts")
            entry = f"{ts}: {content}" if ts else content
            lines.append(f"{i}. {entry}")
        return "\n".join(lines)

    block_a = _fmt(speaker_a_items, "A")
    block_b = _fmt(speaker_b_items, "B")

    # Resolve the conversation reference time. NEVER use server NOW —
    # LoCoMo's gold answers reference 2022/2023; server NOW is 2026.
    override = os.getenv("LOCOMO_CONV_NOW", "").strip()
    if override:
        conv_now: str | None = override
    else:
        conv_now = _conversation_now(speaker_a_items + speaker_b_items)

    if conv_now:
        now_line = f"Current time: {conv_now}\n\n"
    else:
        # No retrieved memory carries a ts AND no operator pin —
        # safer to omit than to leak today's date.
        now_line = ""

    brevity = _category_brevity_instruction(category)
    return (
        "You are a factual QA assistant answering a question about a recorded "
        "two-speaker conversation.  Below are memories retrieved separately "
        "from each speaker's personal memory store; treat both speakers' "
        "evidence as equally authoritative and combine across speakers when "
        "the question requires it.\n\n"
        f"{now_line}"
        "## Memories\n"
        f"{block_a}\n\n"
        f"{block_b}\n\n"
        "Each memory line is prefixed with its in-conversation date (YYYY-MM-DD). "
        "Resolve relative phrases like 'last week', 'yesterday', 'next month' "
        "against the dated memory, NOT against the 'Current time' header. "
        "When asked WHEN an event happened, answer with the most specific "
        "date or relative phrase present in the memories themselves.\n\n"
        f"{brevity} "
        "Do not echo the question in your answer. Do not add filler like "
        "'According to the memories' or 'Based on what is provided'."
    )


def query_chat_dual(
    memdb_url: str,
    conv_id: str,
    query: str,
    top_k: int,
    speaker_a_items: list[dict] | None = None,
    speaker_b_items: list[dict] | None = None,
    timeout: int = 120,
    category: int | None = None,
) -> tuple[str, list[dict], list[dict], int]:
    """Dual-speaker chat: pass both speakers' memories to `/product/chat/complete`.

    If `speaker_a_items` / `speaker_b_items` are provided (typical caller
    path — they come from a prior `query_search_dual` call), they are reused
    verbatim to avoid duplicate retrieval.  Otherwise, a fresh dual fan-out
    runs first.

    Builds an explicit `[speaker:A]`/`[speaker:B]` system prompt that
    overrides the server's default `answer_style=factual` template and
    minimises server-side retrieval noise by setting `top_k=1` for the
    chat call (the model relies on our pre-fetched dual-speaker context).

    Returns `(answer, speaker_a_items, speaker_b_items, elapsed_ms)`.
    """
    if speaker_a_items is None or speaker_b_items is None:
        speaker_a_items, speaker_b_items, _, _ = query_search_dual(
            memdb_url, conv_id, query, top_k, session_id=None, timeout=timeout
        )

    system_prompt = _build_dual_speaker_system_prompt(
        speaker_a_items, speaker_b_items, category=category
    )

    # The chat endpoint requires a user_id; pick speaker_a deterministically.
    # Server-side retrieval is suppressed with top_k=1 + a high threshold —
    # the model's primary context comes from `system_prompt`.
    uid_a, _ = _speaker_user_ids(conv_id)
    payload = {
        "user_id": uid_a,
        "query": query,
        "top_k": 1,
        "mode": "fast",
        "include_preference": False,
        "threshold": _CHAT_RETRIEVAL_SUPPRESS_THRESHOLD,
        "system_prompt": system_prompt,
        # M12.4 routing trigger: server's buildSystemPromptWithDecision
        # injects variant-conditional anti-refusal rules ONLY when answer_style
        # is "factual" — the dual-speaker harness path was silently bypassing
        # M12.4 because this flag was missing, leaving 48% chat_refused_with_evidence.
        "answer_style": "factual",
        # Token-budget — same rationale as query_chat. system_prompt path
        # already pre-trims memories per-speaker via the prompt builder, but
        # the server still injects {memories} placeholder if templated; the
        # cap is a no-op there and an active cap on legacy non-system_prompt
        # callers, so it's safe to set unconditionally.
        "max_context_tokens": int(os.getenv("LOCOMO_MAX_CONTEXT_TOKENS", "8000")),
        # Karpathy r3 Fix #3 — signal to server that harness pre-supplied N
        # memories in basePrompt. Server uses this to upgrade variant from
        # Zero (which would inject strict "no answer" refusal rules) to High
        # (commit-to-best-evidence). Without this field, server's empty pool
        # (top_k=1 + threshold=0.99) → variant=Zero → 11 false-negative
        # refusals в Run #19 где hit@k=1.0 но LLM рефузит.
        "external_memory_count": len(speaker_a_items) + len(speaker_b_items),
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
    answer = ""
    if isinstance(body, dict):
        data = body.get("data", body)
        if isinstance(data, str):
            answer = data
        elif isinstance(data, dict):
            for key in ("response", "answer", "content", "message", "text"):
                val = data.get(key)
                if isinstance(val, str) and val:
                    answer = val
                    break
    if not answer:
        answer = json.dumps(body)[:2000]
    return answer, speaker_a_items, speaker_b_items, elapsed_ms


def _load_qids_from_file(path: Path) -> set[tuple[str, int]]:
    """Load a set of (conv_id, question_idx) pairs from a text file.

    Each non-empty, non-comment line must be: ``<conv_id> <question_idx>``
    (space-separated).  Lines starting with ``#`` are ignored.
    """
    pairs: set[tuple[str, int]] = set()
    with path.open() as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split()
            if len(parts) < 2:
                print(
                    f"[qids-from-file] WARNING: line {lineno} skipped (expected "
                    f"'<conv_id> <question_idx>', got {line!r})",
                    file=sys.stderr,
                )
                continue
            try:
                pairs.add((parts[0], int(parts[1])))
            except ValueError:
                print(
                    f"[qids-from-file] WARNING: line {lineno} skipped "
                    f"(question_idx not an int: {parts[1]!r})",
                    file=sys.stderr,
                )
    return pairs


# ---------------------------------------------------------------------------
# M12.5 — observability summary block (Category D)
# ---------------------------------------------------------------------------
# These metrics surface harness-path signals that M11 LoCoMo regression
# analysis was missing.  Computed once at the end of the run and printed in a
# stable text block so CI / postmortems can grep them deterministically.
# ---------------------------------------------------------------------------


# Refusal regex — kept in sync with internal/observability/m12_metrics.go
# (Go-side IsRefusalAnswer).  False-positive guard: substantive answers like
# "she does not own a dog" must NOT match — anchor "do not (contain|state|
# mention|...)" patterns.
_LOCOMO_REFUSAL_RE = re.compile(
    r"(?i)\b(do not (contain|state|mention|describe|specify|detail|provide|"
    r"include|indicate|reference)|memories.{0,40}(do not|no information|"
    r"don't (contain|mention|state)))\b"
)
_LOCOMO_BARE_NO_ANSWER_RE = re.compile(
    r"(?i)^\s*(no answer|i don't know|i do not know|unknown|n/?a)\s*\.?\s*$"
)
# Today-leak regex — flags predictions that reference 2025/2026 dates when
# the LoCoMo gold dataset is pre-2024.  Surfaces "system prompt date echo"
# bugs (where the LLM treats the current_time placeholder as a memory fact).
_LOCOMO_TODAY_LEAK_RE = re.compile(r"\b(202[5-9]|203\d)\b")


def _is_locomo_refusal(answer: str) -> bool:
    """Mirror of Go-side observability.IsRefusalAnswer — see m12_metrics.go."""
    a = (answer or "").strip()
    if not a:
        return False
    if _LOCOMO_BARE_NO_ANSWER_RE.match(a):
        return True
    return bool(_LOCOMO_REFUSAL_RE.search(a))


def _print_m12_observability(predictions: list[dict], gold: list[dict]) -> None:
    """Print a stable "## Observability deltas vs M11 baseline" block.

    Caller passes the full predictions list and the gold list (for category
    cross-reference).  All counts are derived in a single pass — no I/O.

    Output goes to stderr so it doesn't pollute the JSON stdout caller may
    capture.  Format is intentionally plain "key=value" lines so CI tooling
    can parse them with grep.
    """
    if not predictions:
        print("[m12 observability] no predictions — skipping summary", file=sys.stderr)
        return

    n = len(predictions)
    today_leak = 0
    refused = 0
    refused_with_hit = 0
    ts_emit = 0  # memory-line emit count where ts != ""
    ts_total = 0  # total memory lines considered
    pred_lengths: list[int] = []
    per_conv_pred_lengths: dict[str, list[int]] = {}

    for rec in predictions:
        ans = rec.get("chat_answer") or ""
        if isinstance(ans, str):
            pred_lengths.append(len(ans))
            conv_id = rec.get("conv_id") or ""
            if conv_id:
                per_conv_pred_lengths.setdefault(conv_id, []).append(len(ans))
            if _LOCOMO_TODAY_LEAK_RE.search(ans):
                today_leak += 1
            if _is_locomo_refusal(ans):
                refused += 1
                # "with evidence" = the harness retrieved at least one memory.
                # Catches the Go-side over-refusal pattern from a different
                # vantage point: if memdb returned candidates but the chat
                # response is "no answer", something is wrong with prompt/
                # rerank.
                retrieved = rec.get("retrieved") or []
                if isinstance(retrieved, list) and len(retrieved) > 0:
                    refused_with_hit += 1

        # Per-memory ts-prefix emit rate. _build_dual_speaker_system_prompt
        # adds a "{ts}: {content}" prefix only when ts is non-empty; if the
        # ts-extraction path silently regressed (M12.1 fix territory), this
        # rate craters and we see it here without re-running the harness.
        for key in ("speaker_a_memories", "speaker_b_memories", "retrieved"):
            mems = rec.get(key) or []
            if not isinstance(mems, list):
                continue
            for m in mems:
                if not isinstance(m, dict):
                    continue
                ts_total += 1
                if (m.get("ts") or "").strip():
                    ts_emit += 1

    # Per-cube (per-conv_id) judge mean — placeholder; the harness doesn't
    # currently record per-question judge scores in process_one_qa, so we
    # emit a length-based proxy until the score path is wired.  Detects cube
    # outliers (e.g. "conv-26 averages 5 chars while conv-01 averages 80 →
    # something cube-specific broke").
    per_cube: dict[str, dict[str, float]] = {}
    for conv_id, lens in per_conv_pred_lengths.items():
        if not lens:
            continue
        slens = sorted(lens)
        per_cube[conv_id] = {
            "n": float(len(slens)),
            "p50_chars": float(slens[len(slens) // 2]),
            "p95_chars": float(slens[int(len(slens) * 0.95)]),
            "mean_chars": sum(slens) / len(slens),
        }

    # Length distribution.
    p50 = p95 = 0
    if pred_lengths:
        slens = sorted(pred_lengths)
        p50 = slens[len(slens) // 2]
        p95 = slens[min(len(slens) - 1, int(len(slens) * 0.95))]

    ts_rate = (ts_emit / ts_total) if ts_total else 0.0
    refused_pct = 100.0 * refused / n if n else 0.0
    refused_hit_pct = 100.0 * refused_with_hit / n if n else 0.0
    today_leak_pct = 100.0 * today_leak / n if n else 0.0

    lines = [
        "",
        "## Observability deltas vs M11 baseline",
        f"locomo_chat_today_leak_count={today_leak} ({today_leak_pct:.1f}%)",
        f"locomo_chat_refused_count={refused} ({refused_pct:.1f}%)",
        f"locomo_chat_refused_with_hit_count={refused_with_hit} ({refused_hit_pct:.1f}%)",
        f"locomo_ts_prefix_emit_rate={ts_rate:.3f} ({ts_emit}/{ts_total})",
        f"locomo_pred_length_p50={p50}",
        f"locomo_pred_length_p95={p95}",
        f"locomo_pred_length_total_predictions={len(pred_lengths)}",
    ]

    if per_cube:
        lines.append("locomo_per_cube_pred_length:")
        for conv_id in sorted(per_cube.keys()):
            stats = per_cube[conv_id]
            lines.append(
                f"  {conv_id}: n={int(stats['n'])} p50={int(stats['p50_chars'])} "
                f"p95={int(stats['p95_chars'])} mean={stats['mean_chars']:.1f}"
            )

    print("\n".join(lines), file=sys.stderr, flush=True)


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    g = p.add_mutually_exclusive_group(required=True)
    g.add_argument("--sample", action="store_true")
    g.add_argument("--full", action="store_true")
    g.add_argument("--gold", type=Path, help="Custom gold JSON.")
    p.add_argument("--memdb-url", default="http://localhost:8080")
    p.add_argument("--top-k", type=int, default=20)
    p.add_argument(
        "--top-k-per-speaker",
        type=int,
        default=30,
        metavar="N",
        help=(
            "Per-speaker top_k for dual-speaker search (default: 30, matching "
            "mem0 eval harness: 30 per speaker × 2 = 60 candidates). "
            "Ignored in single-speaker mode. F9 recall budget tuning."
        ),
    )
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
    p.add_argument(
        "--workers",
        type=int,
        default=int(os.getenv("LOCOMO_WORKERS", "5")),
        help=(
            "Outer parallelism: process N QAs concurrently via ThreadPoolExecutor. "
            "Default 5 (was 1; bumped 2026-05-01 — query is the cycle bottleneck "
            "and CLIProxyAPI :8317 handles 60+ concurrent comfortably). Use 1 to "
            "reproduce pre-2026-05-01 serial baselines. Env: LOCOMO_WORKERS."
        ),
    )
    p.add_argument(
        "--retry-on-timeout",
        type=int,
        default=2,
        metavar="N",
        help=(
            "Number of retry attempts on connection timeout or 5xx errors. "
            "0 disables retries (parity with legacy behaviour). Default: 2."
        ),
    )
    p.add_argument(
        "--retry-backoff-base",
        type=float,
        default=5.0,
        metavar="N",
        help=(
            "First backoff in seconds before the initial retry; "
            "each subsequent retry doubles the delay (5 → 10 → 20 …). "
            "Default: 5."
        ),
    )
    p.add_argument(
        "--per-question-deadline",
        type=float,
        default=float(os.getenv("LOCOMO_PER_Q_DEADLINE", "0")),
        metavar="SECONDS",
        help=(
            "Overall wall-clock budget per /search and /chat call (across "
            "retries + backoff sleeps).  0 (default) disables — only the "
            "per-attempt socket timeout applies.  Recommended: 60-120 to "
            "cap pathological backoff chains.  Env: LOCOMO_PER_Q_DEADLINE."
        ),
    )
    p.add_argument(
        "--qids-from-file",
        type=Path,
        default=None,
        metavar="FILE",
        help=(
            "Path to a text file containing '<conv_id> <question_idx>' pairs "
            "(one per line).  When supplied, only those QAs are processed "
            "(useful for targeted re-runs of previously failed questions)."
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

    # --qids-from-file: filter gold to only the specified (conv_id, question_idx) pairs
    if args.qids_from_file is not None:
        qid_filter = _load_qids_from_file(args.qids_from_file)
        before = len(gold)
        gold = [qa for qa in gold if (qa["conv_id"], qa["question_idx"]) in qid_filter]
        print(
            f"[query] --qids-from-file: kept {len(gold)}/{before} QAs "
            f"({len(qid_filter)} pairs requested, {len(qid_filter) - len(gold)} not found in gold)",
            flush=True,
        )

    dual_speaker = LOCOMO_DUAL_SPEAKER
    print(
        f"[query] dual_speaker={dual_speaker} "
        f"(env LOCOMO_DUAL_SPEAKER={os.getenv('LOCOMO_DUAL_SPEAKER', '<unset>')!r})",
        flush=True,
    )
    if dual_speaker:
        print(
            f"[query] top_k_per_speaker={args.top_k_per_speaker} "
            f"(total candidates = {args.top_k_per_speaker * 2} before merge to top_k={args.top_k})",
            flush=True,
        )

    retry_n = args.retry_on_timeout
    retry_backoff = args.retry_backoff_base
    # 0 → disabled; pass None into _call_with_retry to keep legacy behaviour.
    per_q_deadline: float | None = (
        args.per_question_deadline if args.per_question_deadline > 0 else None
    )
    print(
        f"[query] retry_on_timeout={retry_n} backoff_base={retry_backoff}s "
        f"per_q_deadline={per_q_deadline if per_q_deadline is not None else 'disabled'}",
        flush=True,
    )

    predictions: list[dict] = []
    errors: list[str] = []

    def process_one_qa(qi: int, qa: dict) -> dict:
        question = qa["question"]
        cat_str = str(qa.get("category", "?"))
        qid_str = str(qa["question_idx"])
        print(f"[{qi}/{len(gold)}] {qa['conv_id']} q={question[:70]!r}", flush=True)
        rec: dict = {
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
            "dual_speaker": dual_speaker,
        }

        if dual_speaker:
            speaker_a_items: list[dict] = []
            speaker_b_items: list[dict] = []
            try:
                speaker_a_items, speaker_b_items, merged, ms = _call_with_retry(
                    query_search_dual,
                    args.memdb_url,
                    qa["conv_id"],
                    question,
                    args.top_k,
                    None,  # session_id
                    60,    # timeout
                    args.top_k_per_speaker,
                    max_retries=retry_n,
                    backoff_base=retry_backoff,
                    deadline=per_q_deadline,
                    cat=cat_str,
                    qid=qid_str,
                )
                rec["retrieved"] = merged
                rec["search_ms"] = ms
                rec["speaker_a_memories"] = speaker_a_items
                rec["speaker_b_memories"] = speaker_b_items
                rec["merged_top_k"] = len(merged)
                rec["top_k_per_speaker"] = args.top_k_per_speaker
            except (requests.RequestException, DeadlineExceeded) as exc:
                rec["error"] = f"search: {exc}"
                rec.setdefault("_errors", []).append(rec["error"])
                print(f"  SEARCH ERROR: {exc}", file=sys.stderr, flush=True)

            if not args.skip_chat and rec["error"] is None:
                try:
                    answer, _, _, ms = _call_with_retry(
                        query_chat_dual,
                        args.memdb_url,
                        qa["conv_id"],
                        question,
                        args.top_k,
                        speaker_a_items,
                        speaker_b_items,
                        max_retries=retry_n,
                        backoff_base=retry_backoff,
                        deadline=per_q_deadline,
                        cat=cat_str,
                        qid=qid_str,
                        category=qa.get("category"),
                    )
                    rec["chat_answer"] = answer
                    rec["chat_ms"] = ms
                except (requests.RequestException, DeadlineExceeded) as exc:
                    rec["error"] = f"chat: {exc}"
                    rec.setdefault("_errors", []).append(rec["error"])
                    print(f"  CHAT ERROR: {exc}", file=sys.stderr, flush=True)
        else:
            user_id = f"{qa['conv_id']}__speaker_{args.speaker}"
            try:
                items, ms = _call_with_retry(
                    query_search,
                    args.memdb_url,
                    user_id,
                    question,
                    args.top_k,
                    None,  # session_id
                    max_retries=retry_n,
                    backoff_base=retry_backoff,
                    deadline=per_q_deadline,
                    cat=cat_str,
                    qid=qid_str,
                )
                rec["retrieved"] = items
                rec["search_ms"] = ms
            except (requests.RequestException, DeadlineExceeded) as exc:
                rec["error"] = f"search: {exc}"
                rec.setdefault("_errors", []).append(rec["error"])
                print(f"  SEARCH ERROR: {exc}", file=sys.stderr, flush=True)

            if not args.skip_chat and rec["error"] is None:
                try:
                    answer, ms = _call_with_retry(
                        query_chat,
                        args.memdb_url,
                        user_id,
                        question,
                        args.top_k,
                        max_retries=retry_n,
                        backoff_base=retry_backoff,
                        deadline=per_q_deadline,
                        cat=cat_str,
                        qid=qid_str,
                    )
                    rec["chat_answer"] = answer
                    rec["chat_ms"] = ms
                except (requests.RequestException, DeadlineExceeded) as exc:
                    rec["error"] = f"chat: {exc}"
                    rec.setdefault("_errors", []).append(rec["error"])
                    print(f"  CHAT ERROR: {exc}", file=sys.stderr, flush=True)

        return rec

    if args.workers > 1:
        print(f"[query] outer parallelism: {args.workers} workers", flush=True)
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
            futures = {pool.submit(process_one_qa, qi, qa): qi for qi, qa in enumerate(gold, 1)}
            done_count = 0
            for fut in concurrent.futures.as_completed(futures):
                done_count += 1
                try:
                    rec = fut.result()
                    if rec.get("_errors"):
                        errors.extend(rec.pop("_errors"))
                    predictions.append(rec)
                    if done_count % 50 == 0 or done_count == len(gold):
                        print(f"[parallel] progress {done_count}/{len(gold)}", flush=True)
                except Exception as exc:
                    print(f"  PARALLEL ERROR: {exc}", file=sys.stderr, flush=True)
                    errors.append(str(exc))
        # Sort predictions by original question_idx for deterministic output
        predictions.sort(key=lambda r: (r["conv_id"], r["question_idx"]))
    else:
        # Sequential path (default, backward-compat)
        for qi, qa in enumerate(gold, 1):
            rec = process_one_qa(qi, qa)
            if rec.get("_errors"):
                errors.extend(rec.pop("_errors"))
            predictions.append(rec)

    # Print retry summary to stderr (even when retries=0; shows zero counts)
    _RETRY_STATS.print_summary()

    # M12.5: harness-path observability deltas vs M11 baseline.  Computed once
    # at the end of the run from the in-memory predictions list, no extra I/O.
    # See docs/superpowers/plans/2026-04-29-m12-recovery-analysis.md §4 for
    # what each metric flags.
    _print_m12_observability(predictions, gold)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w") as f:
        json.dump(
            {
                "predictions": predictions,
                "meta": {
                    "memdb_url": args.memdb_url,
                    "top_k": args.top_k,
                    "top_k_per_speaker": args.top_k_per_speaker,
                    "speaker": args.speaker,
                    "total": len(gold),
                    "errors": len(errors),
                    "skip_chat": args.skip_chat,
                    "categories": categories,
                    "dual_speaker": dual_speaker,
                    "retry_on_timeout": retry_n,
                    "retry_backoff_base": retry_backoff,
                    "per_question_deadline": per_q_deadline,
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
