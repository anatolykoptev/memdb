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
import sys
import time
import threading

from pathlib import Path

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
    resp = exc.response
    if resp is None:
        return False
    code = resp.status_code
    if code in (502, 503, 504):
        return True
    if code == 500:
        body = ""
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


def _call_with_retry(
    fn,
    *args,
    max_retries: int = 2,
    backoff_base: float = 5.0,
    cat: str = "",
    qid: str = "",
    **kwargs,
):
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
    cat, qid:
        Metadata logged to stderr on each retry attempt.

    Returns
    -------
    The return value of ``fn``.

    Raises
    ------
    Re-raises the last exception when all retries are exhausted.
    """
    last_exc: Exception | None = None
    delay = backoff_base
    for attempt in range(max_retries + 1):
        try:
            result = fn(*args, **kwargs)
            if attempt > 0:
                # Successful recovery after at least one retry
                _RETRY_STATS.record_recovered()
            return result
        except Exception as exc:
            if attempt < max_retries and _is_retryable(exc):
                _RETRY_STATS.record_attempt()
                reason = _short_reason(exc)
                print(
                    f"[retry attempt={attempt + 1} cat={cat} qid={qid} reason={reason}]",
                    file=sys.stderr,
                    flush=True,
                )
                time.sleep(delay)
                delay *= 2
                last_exc = exc
                continue
            # Not retryable or exhausted — propagate
            if attempt > 0:
                _RETRY_STATS.record_final_error()
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
    """
    for key in ("chat_time", "created_at", "created_time", "time", "date"):
        val = metadata.get(key)
        if val and isinstance(val, str):
            # Trim to date-only part (first 10 chars of ISO8601) for brevity.
            # "2023-05-08T13:56:00Z" → "2023-05-08"
            return val[:10] if len(val) >= 10 else val
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
        items.append(
            {
                "content": content,
                "score": score,
                "id": m.get("id") or m.get("memory_id") or metadata.get("id") or "",
                "type": memory_type,
                "ts": ts,
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
    """Return `(speaker_a_uid, speaker_b_uid)` for a LoCoMo conv id."""
    return f"{conv_id}__speaker_a", f"{conv_id}__speaker_b"


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
    """Dual-speaker fan-out of `query_search`.

    Issues two parallel `/product/search` calls — one for `<conv>__speaker_a`
    and one for `<conv>__speaker_b` — using a 2-worker thread pool, then
    returns `(speaker_a_items, speaker_b_items, merged_items, elapsed_ms)`.
    The merged list is bounded to `top_k` by `_merge_dual_results`.

    F9: each speaker is queried with `top_k_per_speaker` (default 30, matching
    the mem0 eval harness: 30 per speaker × 2 = 60 candidates).  Merged list
    is still capped at `top_k` so downstream callers see the same budget.

    Network errors propagate (caller handles `requests.RequestException`).
    """
    uid_a, uid_b = _speaker_user_ids(conv_id)
    start = time.time()
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


def _build_dual_speaker_system_prompt(
    speaker_a_items: list[dict],
    speaker_b_items: list[dict],
) -> str:
    """Assemble a system prompt that exposes both speakers' memories.

    Mirrors Memobase's `answer_question` template (memobase_search.py:81-88):
    each speaker's memories are presented as a labelled block so the model
    cannot "miss" cross-speaker evidence.  The current date placeholder
    matches the server-side `factualQAPromptEN` style.

    F9: each memory line is prefixed with its timestamp when available
    (`f"{ts}: {content}"`) so the LLM gets a time anchor per fact — mem0
    arxiv pattern for temporal multi-hop (cat-2) queries.
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

    now = time.strftime("%Y-%m-%d %H:%M (%A)")
    block_a = _fmt(speaker_a_items, "A")
    block_b = _fmt(speaker_b_items, "B")
    return (
        "You are a factual QA assistant answering a question about a recorded "
        "two-speaker conversation.  Below are memories retrieved separately "
        "from each speaker's personal memory store; treat both speakers' "
        "evidence as equally authoritative and combine across speakers when "
        "the question requires it.\n\n"
        f"Current time: {now}\n\n"
        "## Memories\n"
        f"{block_a}\n\n"
        f"{block_b}\n\n"
        "Answer the user's question with a short, direct factual response. "
        "If the memories do not contain the answer, say so plainly."
    )


def query_chat_dual(
    memdb_url: str,
    conv_id: str,
    query: str,
    top_k: int,
    speaker_a_items: list[dict] | None = None,
    speaker_b_items: list[dict] | None = None,
    timeout: int = 120,
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
        speaker_a_items, speaker_b_items
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
        default=int(os.getenv("LOCOMO_WORKERS", "1")),
        help=(
            "Outer parallelism: process N QAs concurrently via ThreadPoolExecutor. "
            "Default 1 (sequential). Use 4-8 for benchmark speedup. "
            "CLIProxyAPI :8317 handles 60+ concurrent. Env: LOCOMO_WORKERS."
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
    print(
        f"[query] retry_on_timeout={retry_n} backoff_base={retry_backoff}s",
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
                    cat=cat_str,
                    qid=qid_str,
                )
                rec["retrieved"] = merged
                rec["search_ms"] = ms
                rec["speaker_a_memories"] = speaker_a_items
                rec["speaker_b_memories"] = speaker_b_items
                rec["merged_top_k"] = len(merged)
                rec["top_k_per_speaker"] = args.top_k_per_speaker
            except requests.RequestException as exc:
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
                        cat=cat_str,
                        qid=qid_str,
                    )
                    rec["chat_answer"] = answer
                    rec["chat_ms"] = ms
                except requests.RequestException as exc:
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
                    cat=cat_str,
                    qid=qid_str,
                )
                rec["retrieved"] = items
                rec["search_ms"] = ms
            except requests.RequestException as exc:
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
                        cat=cat_str,
                        qid=qid_str,
                    )
                    rec["chat_answer"] = answer
                    rec["chat_ms"] = ms
                except requests.RequestException as exc:
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
