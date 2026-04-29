"""
judge_multi_query_retrieval.py — fan-out retrieval over paraphrased sub-queries.

M13 J3-Q stage 2: take the variants produced by ``judge_query_splitter`` and
push each through the existing ``/product/search`` pipeline (single- or
dual-speaker), then union & dedupe the result lists by memory id before
handing them to the reassembling judge.

Why this is the cat-1 multi-fact aggregation fix:

    Q: "What activities does Melanie partake in?"
    Gold (LoCoMo cat-1): pottery, camping, painting, swimming
    Single-query top-K@20: pottery, painting, hiking, running       ← misses camping, swimming
    Three-query union:
      - hobbies/crafts → pottery, painting, sketching
      - outdoor       → camping, hiking, swimming
      - sports        → swimming, running, charity-race
      union ∪ dedupe → pottery, painting, sketching, camping, hiking, swimming, running, charity-race
    All 4 gold facts covered by union.

Public surface:
    MultiQueryConfig          — knobs (top_k, max_union_size, parallel)
    multi_query_retrieve(...) — returns dict with sub_queries / union /
                                per-query lists / size diagnostics

This module is search-only: it does NOT call /chat/complete.  The system
prompt + final verdict assembly live in ``judge_multi_query_reassembler``.

Coordination notes (M13 J1/J2/J5):
    * The existing JudgeVerdict type from llm_judge.py keeps the binary
      score=0|1 shape.  This module returns retrieval data only.  The
      reassembler emits a (5pt → binary) score that score.py treats the
      same way as the existing ``llm_score`` column.
    * Reuses ``query.query_search`` and ``query.query_search_dual`` so the
      single-source-of-truth retrieval path (relativity threshold, retry,
      M9 dual-speaker fan-out, M12.1 ts handling) never forks.
"""

from __future__ import annotations

import concurrent.futures
import sys
from dataclasses import dataclass, field
from typing import Callable

# These imports MUST stay relative to the locomo dir on sys.path; query.py
# already enforces that via its own bootstrap.
from judge_query_splitter import SplitterConfig, split_query

import query as locomo_query  # for query_search / query_search_dual


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
@dataclass
class MultiQueryConfig:
    """Bundles per-question multi-query retrieval knobs.

    ``user_id`` is used in single-speaker mode (legacy harness path).
    ``conv_id`` is used in dual-speaker mode (M9+).  Exactly one of the two
    matters per call; the unused one is ignored.

    ``parallel=True`` issues N×retrieval calls concurrently via
    ``ThreadPoolExecutor``.  CLIProxyAPI :8317 plus the embed-server
    sidecar handle 60+ concurrent QPS so this stays cheap; the ceiling is
    actually memdb-go itself.  Worker count = min(n_variants, 8).
    """

    splitter: SplitterConfig = field(default_factory=SplitterConfig)
    memdb_url: str = "http://localhost:8080"
    # Dual-speaker mode (default, mirrors LOCOMO_DUAL_SPEAKER=true):
    conv_id: str | None = None
    top_k_per_speaker: int = 30
    # Single-speaker mode (legacy / LOCOMO_DUAL_SPEAKER=false):
    user_id: str | None = None
    # Common knobs:
    top_k: int = 20
    max_union_size: int = 60
    parallel: bool = True
    timeout: int = 60
    # When True, prepend the original question itself as variant[0] so the
    # union always includes the legacy single-query hits as a floor —
    # multi-query then strictly dominates single-query on recall.  See
    # PR body for the empirical justification.
    include_original: bool = True


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _dedupe_by_id(lists: list[list[dict]], max_size: int) -> tuple[list[dict], int]:
    """Union & dedupe retrieved memories across N sub-query result lists.

    Dedup key precedence:
      1. ``id`` field (memdb-go memory id) — primary, stable across cubes.
      2. ``content`` text — fallback when id is empty/missing.

    When the same memory appears multiple times, we keep the entry with the
    highest ``score``.  Returned list is sorted by score descending and
    truncated to ``max_size`` entries.

    Returns ``(union, size_before_cap)`` so callers can log the truncation.
    """
    by_key: dict[str, dict] = {}
    for items in lists:
        for it in items:
            if not isinstance(it, dict):
                continue
            key = (it.get("id") or "").strip()
            if not key:
                # Fall back to content hash — best we can do.  Skip when
                # content is also empty (corrupt row, very rare).
                content = (it.get("content") or "").strip()
                if not content:
                    continue
                key = f"content::{hash(content)}"
            existing = by_key.get(key)
            if existing is None:
                by_key[key] = dict(it)
                continue
            # Higher score wins.  None → 0.0 for comparison.
            new_score = it.get("score") or 0.0
            old_score = existing.get("score") or 0.0
            if new_score > old_score:
                by_key[key] = dict(it)

    merged = list(by_key.values())
    merged.sort(key=lambda m: (m.get("score") or 0.0), reverse=True)
    size_before = len(merged)
    if size_before > max_size:
        merged = merged[:max_size]
    return merged, size_before


def _retrieve_one(
    cfg: MultiQueryConfig,
    sub_q: str,
) -> list[dict]:
    """Run a single sub-query through the existing retrieval pipeline.

    Picks dual-speaker vs single-speaker based on which id is set on the
    config.  Errors are NOT silently swallowed — they propagate to
    ``multi_query_retrieve`` which logs the variant and continues with the
    rest of the union (one bad sub-query shouldn't sink the whole pass).
    """
    if cfg.conv_id:
        _, _, merged, _ = locomo_query.query_search_dual(
            cfg.memdb_url,
            cfg.conv_id,
            sub_q,
            cfg.top_k,
            session_id=None,
            timeout=cfg.timeout,
            top_k_per_speaker=cfg.top_k_per_speaker,
        )
        return merged

    if cfg.user_id:
        items, _ = locomo_query.query_search(
            cfg.memdb_url,
            cfg.user_id,
            sub_q,
            cfg.top_k,
            session_id=None,
            timeout=cfg.timeout,
        )
        return items

    raise ValueError(
        "MultiQueryConfig: exactly one of conv_id (dual) or user_id (single) "
        "must be set"
    )


# ---------------------------------------------------------------------------
# Public entry point
# ---------------------------------------------------------------------------
def multi_query_retrieve(
    question: str,
    cfg: MultiQueryConfig | None = None,
    *,
    splitter_fn: Callable[[str, SplitterConfig], list[str]] | None = None,
    retriever_fn: Callable[[MultiQueryConfig, str], list[dict]] | None = None,
) -> dict:
    """Run the full multi-query retrieval pipeline for one question.

    Returns the diagnostic dict (see module docstring) — callers can
    serialise this verbatim into the predictions JSON for offline review.

    ``splitter_fn`` / ``retriever_fn`` are exposed for unit tests so the
    full pipeline runs against in-memory fakes; production callers leave
    them unset.
    """
    if cfg is None:
        cfg = MultiQueryConfig()

    splitter = splitter_fn or split_query
    retriever = retriever_fn or _retrieve_one

    # Stage 1: paraphrase the question.
    raw_variants = splitter(question, cfg.splitter)

    # Compose the working set of sub-queries.  Always include the original
    # when configured (cfg.include_original) — this is the safety net that
    # makes multi-query a strict superset of single-query in retrieval.
    sub_queries: list[str] = []
    if cfg.include_original:
        sub_queries.append(question)
    for v in raw_variants:
        if v and v not in sub_queries:
            sub_queries.append(v)

    # Stage 2: parallel retrieval per sub-query.
    per_query_retrieved: list[list[dict]] = []
    errors: list[str] = []

    def _run_one(sq: str) -> list[dict]:
        try:
            return retriever(cfg, sq)
        except Exception as exc:  # noqa: BLE001 — partial-failure tolerant
            errors.append(f"sub-q {sq[:50]!r}: {type(exc).__name__}: {str(exc)[:120]}")
            return []

    if cfg.parallel and len(sub_queries) > 1:
        n_workers = min(len(sub_queries), 8)
        with concurrent.futures.ThreadPoolExecutor(max_workers=n_workers) as pool:
            # preserve sub_queries order in the result list
            futures = [pool.submit(_run_one, sq) for sq in sub_queries]
            for fut in futures:
                per_query_retrieved.append(fut.result())
    else:
        for sq in sub_queries:
            per_query_retrieved.append(_run_one(sq))

    # Stage 3: union & dedupe.
    union, size_before_cap = _dedupe_by_id(per_query_retrieved, cfg.max_union_size)

    if errors:
        # Surface partial failures to stderr — don't fail the whole question
        # because one variant tripped a transient 500.
        print(
            f"[multi-query] {len(errors)}/{len(sub_queries)} sub-queries errored: "
            f"{'; '.join(errors[:3])}",
            file=sys.stderr,
            flush=True,
        )

    return {
        "original_question": question,
        "sub_queries": sub_queries,
        "per_query_retrieved": per_query_retrieved,
        "union": union,
        "union_size_before_cap": size_before_cap,
        "union_size_after_cap": len(union),
        "errors": errors,
    }
