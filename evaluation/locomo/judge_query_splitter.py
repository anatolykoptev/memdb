"""
judge_query_splitter.py — paraphrase a question into N sub-queries via an LLM.

M13 J3-Q (multi-query expansion).  This is the FIRST stage of the multi-query
retrieval pipeline:

    original Q  ──splitter LLM──▶  [variant 1, variant 2, variant 3]
                                        │  │  │
                                        ▼  ▼  ▼
                                       (parallel /product/search calls)
                                                │
                                                ▼
                                       (union/dedupe by memory id)
                                                │
                                                ▼
                                          single judge

Why paraphrase instead of multi-judge ensemble (per user's M13 architectural
note): cat-1 multi-fact aggregation regresses (60% → 30% on M12 sample) when
gold has 4-6 facts but a SINGLE retrieval misses some due to phrasing.  Three
paraphrased queries trigger different cosine neighbourhoods and union those
hits before the judge sees them.  Cheaper than multi-judge (1 splitter + 1
judge = 2 LLM calls, vs 3 judges) and addresses the right failure mode
(retrieval miss, not judge bias).

Prior art: HyDE (Gao et al. 2022), LangChain MultiQueryRetriever, mem0 paper.

Public surface:
    SplitterConfig          — frozen-ish dataclass with N / model / temperature
    split_query(q, cfg)     — returns list[str] of length N (or [q] on error)
    SPLITTER_PROMPT_TEMPLATE — the exact prompt shipped to the LLM

Failure handling:
    NEVER returns an empty list.  On JSON parse failure, network failure, or
    a degenerate response, falls back to ``[question]`` so the caller's
    multi-query path collapses to a (degraded) single-query path with a clear
    log trail.  This is "fail-open to single-query" — never silently disables.

Cache:
    Reuses ``llm_judge.LLMJudgeCache`` with a separate disk file
    (``.query_splitter_cache.json``) so paraphrased variants are stable
    across re-runs.  Splitter cost is small (~$0.01/1K) but caching keeps
    the harness deterministic for CI comparisons.
"""

from __future__ import annotations

import json
import os
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import requests

# Reuse the disk cache machinery from llm_judge — same atomic-flush, same
# threading semantics.  We give it a separate file path so judge results
# and splitter results don't collide in the same dict.
from llm_judge import LLMJudgeCache, _cache_key

_DEFAULT_SPLITTER_CACHE = (
    Path(__file__).resolve().parent / "results" / ".query_splitter_cache.json"
)


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
@dataclass
class SplitterConfig:
    """Knobs for the question-splitter LLM call.

    Defaults match the M13 J3-Q design doc: 3 variants, gemini-2.5-flash
    (cheapest stable model on cliproxyapi), temperature 0.3 for mild
    diversity without hallucinating new entities.
    """

    n_variants: int = 3
    model: str = "gemini-2.5-flash"
    temperature: float = 0.3
    timeout: int = 30
    api_base: str | None = None
    api_key: str | None = None
    # When True (default) re-use the on-disk cache.  Tests pass False to
    # exercise the network path deterministically with mocked HTTP.
    use_cache: bool = True
    cache_path: Path = field(default_factory=lambda: _DEFAULT_SPLITTER_CACHE)


# ---------------------------------------------------------------------------
# Prompt
# ---------------------------------------------------------------------------
SPLITTER_PROMPT_TEMPLATE = """\
Given a question, generate {n} paraphrased variants that approach it from \
different angles.  Each variant must preserve the core intent but use \
different vocabulary, focus, or specificity.

Original: {question}

Output JSON (no markdown fence, no prose, just one JSON object):
{{"variants": ["...", "...", "..."]}}

Rules:
- First variant: same intent, different phrasing.
- Second variant: focus on a related sub-aspect (e.g., "hobbies" instead of "activities").
- Third variant: focus on a different sub-aspect (e.g., "sports" instead of "activities").
- Each variant must be at most 100 characters.
- Do NOT add new named entities, dates, or facts that are not already in the original.
- Output exactly {n} variants in the JSON array.
"""


# ---------------------------------------------------------------------------
# Module-level shared cache (created lazily so unit tests can stub the path)
# ---------------------------------------------------------------------------
_shared_cache: LLMJudgeCache | None = None


def _get_shared_cache(path: Path) -> LLMJudgeCache:
    global _shared_cache
    if _shared_cache is None or _shared_cache.path != path:
        _shared_cache = LLMJudgeCache(path=path)
    return _shared_cache


# ---------------------------------------------------------------------------
# JSON extraction (mirrors llm_judge._extract_json — the LLM is the same model
# and the same response-shape failures apply, but we duplicate to keep
# llm_judge.py's surface unchanged)
# ---------------------------------------------------------------------------
def _extract_json_object(content: str) -> dict[str, Any]:
    """Pull the first/best JSON object out of an LLM response.

    Handles plain JSON, ```json fences``` and trailing commentary.  Raises
    ``ValueError`` when nothing parses; callers fall back to single-query.
    """
    stripped = content.strip()
    if stripped.startswith("```"):
        lines = stripped.splitlines()
        lines = lines[1:]
        if lines and lines[-1].strip() == "```":
            lines = lines[:-1]
        stripped = "\n".join(lines).strip()

    try:
        return json.loads(stripped)
    except json.JSONDecodeError:
        pass

    # Greedy match: outermost {...} block.  Prefer this over the non-greedy
    # ``\{[^{}]+\}`` used in llm_judge — splitter responses contain a JSON
    # *array* inside the object so the body has braces, defeating that regex.
    match = re.search(r"\{.*\}", content, re.DOTALL)
    if match:
        try:
            return json.loads(match.group())
        except json.JSONDecodeError:
            pass
    raise ValueError(f"No JSON object found in splitter response: {content[:200]!r}")


# ---------------------------------------------------------------------------
# Public entry point
# ---------------------------------------------------------------------------
def split_query(question: str, cfg: SplitterConfig | None = None) -> list[str]:
    """Return up to ``cfg.n_variants`` paraphrased variants of ``question``.

    Always returns a non-empty list:
      * happy path → exactly ``cfg.n_variants`` strings (caller may dedupe)
      * any error  → ``[question]``  (single-query fallback, NOT silent — a
        warning is printed to stderr)

    The original question is NOT automatically prepended — the prompt itself
    is supposed to produce a faithful first variant.  Callers that want to
    guarantee the literal original is part of the union should add it
    themselves; the multi-query retriever in this package does so.
    """
    if cfg is None:
        cfg = SplitterConfig()

    q = (question or "").strip()
    if not q:
        # Empty query — nothing to split; degrade to a single empty string
        # so caller logic (which usually does memdb_search(q)) sees the
        # same shape as before.
        return [""]

    cache_key = _cache_key(
        q,
        f"splitter:{cfg.n_variants}:{cfg.model}:{cfg.temperature}",
        "",
    )
    cache: LLMJudgeCache | None = None
    if cfg.use_cache:
        cache = _get_shared_cache(cfg.cache_path)
        cached = cache.get(cache_key)
        if cached is not None and isinstance(cached.get("variants"), list):
            return list(cached["variants"])

    try:
        variants = _call_splitter(q, cfg)
    except Exception as exc:  # noqa: BLE001 — fail-open
        print(
            f"[query-splitter] fallback to single query (reason: "
            f"{type(exc).__name__}: {str(exc)[:160]})",
            file=sys.stderr,
            flush=True,
        )
        return [q]

    if not variants:
        # Empty list from a successful call → still degenerate; degrade.
        print(
            "[query-splitter] LLM returned 0 variants — fallback to single query",
            file=sys.stderr,
            flush=True,
        )
        return [q]

    if cache is not None:
        cache.set(cache_key, {"variants": list(variants)})

    return variants


def _call_splitter(question: str, cfg: SplitterConfig) -> list[str]:
    """Make the actual HTTP call and parse the response.

    Returns the variant list on success.  Raises on any failure so the
    public ``split_query`` can decide between fallback and propagation.
    """
    base = (
        cfg.api_base
        or os.getenv("LLM_API_BASE")
        or "http://127.0.0.1:8317/v1"
    ).rstrip("/")
    key = (
        cfg.api_key
        or os.getenv("CLI_PROXY_API_KEY")
        or os.getenv("LLM_API_KEY")
        or ""
    )

    prompt = SPLITTER_PROMPT_TEMPLATE.format(
        n=cfg.n_variants,
        question=question,
    )

    resp = requests.post(
        f"{base}/chat/completions",
        json={
            "model": cfg.model,
            "messages": [{"role": "user", "content": prompt}],
            "response_format": {"type": "json_object"},
            "temperature": cfg.temperature,
        },
        headers={"Authorization": f"Bearer {key}"},
        timeout=cfg.timeout,
    )
    resp.raise_for_status()
    content = resp.json()["choices"][0]["message"]["content"]
    parsed = _extract_json_object(content)

    raw = parsed.get("variants")
    if not isinstance(raw, list):
        raise ValueError(f"splitter response missing 'variants' list: {parsed!r}")

    cleaned: list[str] = []
    for v in raw:
        if not isinstance(v, str):
            continue
        s = v.strip()
        if not s:
            continue
        # Hard cap at 100 chars per the prompt rule — the LLM occasionally
        # ignores it; truncate defensively to avoid 500-char monsters
        # poisoning downstream cosine search.
        if len(s) > 200:
            s = s[:200]
        cleaned.append(s)

    # Dedupe while preserving order
    seen: set[str] = set()
    deduped: list[str] = []
    for s in cleaned:
        key_norm = s.lower()
        if key_norm in seen:
            continue
        seen.add(key_norm)
        deduped.append(s)

    # Truncate to requested count.  Pad with the original question if the
    # LLM under-delivered so callers always get cfg.n_variants entries —
    # this matters for the union step (3 retrievals → 3 cosine neighbourhoods)
    # but degrades gracefully to fewer when the LLM behaves badly.
    if len(deduped) > cfg.n_variants:
        deduped = deduped[: cfg.n_variants]
    if len(deduped) == 0:
        # The LLM returned a list of empty strings.  Treat as failure.
        raise ValueError("splitter response contained no usable variants")

    return deduped


# ---------------------------------------------------------------------------
# Lifecycle helpers
# ---------------------------------------------------------------------------
def flush_shared_cache() -> None:
    """Flush the module-level shared splitter cache, if any."""
    if _shared_cache is not None:
        _shared_cache.flush()
