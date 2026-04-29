"""
judge_multi_query_reassembler.py — single LLM judge over a multi-query union.

M13 J3-Q stage 3: with the original question, gold answer, and union of
retrieved memories from the paraphrased sub-queries, emit a fact-aware 5pt
verdict that score.py can collapse into the existing CORRECT/WRONG axis.

Why a 5pt scale (not the binary CORRECT/WRONG used by llm_judge.py):
    LoCoMo cat-1 multi-fact aggregation has gold answers like
    "pottery, camping, painting, swimming" (4 facts).  A 0/1 judge has to
    pick a side on partial recall, hiding diagnostic signal.  The 5pt
    matched/missing breakdown lets us:
      * compute a finer-grained per-question score (graceful 2/4 → 0.5)
      * extract per-fact recall for a follow-up sprint
      * keep CORRECT/WRONG comparable to the existing harness via the
        same threshold convention (≥3/4 ⇒ binary 1) used in M9 Judge work

Public surface:
    JudgeVerdict5pt              — dataclass mirror of llm_judge return shape
    REASSEMBLER_PROMPT_TEMPLATE  — the exact prompt
    reassemble_verdict(...)      — single LLM call, returns JudgeVerdict5pt

Cache:
    Reuses ``LLMJudgeCache`` (separate file ``.multi_query_judge_cache.json``)
    so the union → verdict mapping is stable across re-runs.  Cache key
    incorporates the question, gold, sub-queries, and union memory IDs so
    a different union (e.g. retrieval drift from server changes) busts the
    cache cleanly.
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

from llm_judge import LLMJudgeCache, _cache_key

_DEFAULT_REASSEMBLER_CACHE = (
    Path(__file__).resolve().parent
    / "results"
    / ".multi_query_judge_cache.json"
)


# ---------------------------------------------------------------------------
# Prompt
# ---------------------------------------------------------------------------
REASSEMBLER_PROMPT_TEMPLATE = """\
You are a strict factual-accuracy judge for a long-conversation memory benchmark.

Given an ORIGINAL question, a GOLD answer (the canonical short answer), and a \
UNION of memories retrieved from {n_subq} paraphrased sub-queries, decide how \
many of the gold answer's facts are supported by the union.

Original question: {question}
Gold answer: {gold}
Sub-queries used (for context only):
{sub_qs_block}

Retrieved memories (deduped union, top {n_mem}):
{memories_block}

Rules:
- Treat the gold answer as a comma- or "and"-separated list of facts when \
applicable.  A short single-fact gold ("3 children", "Hawaii") counts as 1 fact.
- A fact is "matched" if any retrieved memory line clearly supports it, even \
with different wording (synonyms, paraphrases, gender/number variants).
- A retrieved memory CAN come from any sub-query — they share evidence.
- Do NOT credit facts that are only present in the question itself.
- For time-related gold answers, accept any consistent reference to the same \
date or period (different formats are fine).
- Be GENEROUS: a memory line that mentions the topic of a gold fact counts \
as match (mirrors the Memobase locomo prompt convention).

Output JSON (no markdown fence, no prose, just one JSON object):
{{
  "reasoning": "1-2 sentences naming each gold fact and whether the union \
supports it",
  "score_5pt": <integer 0..4>,
  "matched_facts": ["fact A", "fact B"],
  "missing_facts": ["fact C"]
}}

Score scale:
  0 = none of the gold facts are present in the union
  1 = ~25% of gold facts present (1 of 4, etc.)
  2 = ~50% present (partial)
  3 = ~75% present (most facts but at least one gap)
  4 = all gold facts present in the union

Threshold: score_5pt >= 3 ⇒ CORRECT.
"""


# ---------------------------------------------------------------------------
# Verdict dataclass
# ---------------------------------------------------------------------------
@dataclass
class JudgeVerdict5pt:
    """Reassembler output, mapped to the binary llm_judge contract.

    ``score_5pt`` is the raw 0..4 LLM output.  ``score`` is the binary
    projection (1 if score_5pt ≥ ``binary_threshold``, else 0) used by
    score.py's existing aggregate columns.

    On any error the dataclass carries score=0 and reason="judge_error: …"
    — same convention as llm_judge.py so callers handle both code paths
    uniformly.
    """

    score: int  # 0 or 1 — binary, comparable to existing llm_score column
    score_5pt: int  # 0..4
    matched_facts: list[str] = field(default_factory=list)
    missing_facts: list[str] = field(default_factory=list)
    reason: str = ""
    raw_response: str = ""  # debug: the LLM's full text response

    def to_dict(self) -> dict[str, Any]:
        return {
            "score": self.score,
            "score_5pt": self.score_5pt,
            "matched_facts": list(self.matched_facts),
            "missing_facts": list(self.missing_facts),
            "reason": self.reason[:500],
        }


# Threshold for binary projection.  3/4 ⇒ CORRECT mirrors the LoCoMo
# "generous grading" convention (most facts present is enough).
_BINARY_THRESHOLD = 3


# ---------------------------------------------------------------------------
# Module-level shared cache
# ---------------------------------------------------------------------------
_shared_cache: LLMJudgeCache | None = None


def _get_shared_cache(path: Path) -> LLMJudgeCache:
    global _shared_cache
    if _shared_cache is None or _shared_cache.path != path:
        _shared_cache = LLMJudgeCache(path=path)
    return _shared_cache


# ---------------------------------------------------------------------------
# JSON extraction (greedy — see judge_query_splitter for the why)
# ---------------------------------------------------------------------------
def _extract_json_object(content: str) -> dict[str, Any]:
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
    match = re.search(r"\{.*\}", content, re.DOTALL)
    if match:
        try:
            return json.loads(match.group())
        except json.JSONDecodeError:
            pass
    raise ValueError(f"No JSON object found in reassembler response: {content[:200]!r}")


# ---------------------------------------------------------------------------
# Memory-block formatter
# ---------------------------------------------------------------------------
def _format_memories_block(memories: list[dict], max_lines: int = 60) -> str:
    """Render the union into a numbered, dated, bounded text block.

    Format mirrors ``query._build_dual_speaker_system_prompt`` so the judge
    sees the same "ts: content" / "[speaker:X]" cues that the chat model
    sees during retrieval.  Lines past ``max_lines`` are dropped (the
    union is already capped at MultiQueryConfig.max_union_size, but this
    is a defensive ceiling).
    """
    if not memories:
        return "(no memories retrieved)"
    lines: list[str] = []
    for i, m in enumerate(memories[:max_lines], 1):
        if not isinstance(m, dict):
            continue
        content = (m.get("content") or "").strip()
        if not content:
            continue
        ts = m.get("ts")
        label = m.get("speaker_label")
        prefix_parts: list[str] = []
        if label:
            prefix_parts.append(f"[speaker:{label}]")
        if ts:
            prefix_parts.append(str(ts))
        prefix = " ".join(prefix_parts) + ": " if prefix_parts else ""
        lines.append(f"{i}. {prefix}{content}")
    if not lines:
        return "(no memories retrieved)"
    return "\n".join(lines)


def _format_subqs_block(sub_qs: list[str]) -> str:
    if not sub_qs:
        return "(none)"
    return "\n".join(f"  - {q}" for q in sub_qs)


# ---------------------------------------------------------------------------
# Public entry point
# ---------------------------------------------------------------------------
def reassemble_verdict(
    question: str,
    gold: str,
    sub_queries: list[str],
    union_memories: list[dict],
    *,
    model: str = "gemini-2.5-flash",
    api_base: str | None = None,
    api_key: str | None = None,
    timeout: int = 30,
    use_cache: bool = True,
    cache_path: Path = _DEFAULT_REASSEMBLER_CACHE,
    binary_threshold: int = _BINARY_THRESHOLD,
) -> JudgeVerdict5pt:
    """One LLM call → 5pt verdict + binary projection.

    Failure mode: returns ``JudgeVerdict5pt(score=0, score_5pt=0,
    reason="judge_error: ...")`` so score.py treats the row as WRONG with
    a diagnostic.  Same shape as ``llm_judge.judge`` for drop-in compat.
    """
    # Cache key: question + gold + sub-queries + memory ids (a content
    # change in any of those should bust the cache, but two different
    # orderings of the same union should NOT — sort the ids first).
    union_ids = sorted(
        (m.get("id") or "") for m in union_memories if isinstance(m, dict)
    )
    key_payload = (
        question
        + "\n---SUBQ---\n"
        + "|".join(sub_queries)
        + "\n---IDS---\n"
        + "|".join(union_ids)
    )
    cache_key = _cache_key(question, gold, key_payload)

    cache: LLMJudgeCache | None = None
    if use_cache:
        cache = _get_shared_cache(cache_path)
        cached = cache.get(cache_key)
        if cached is not None:
            return JudgeVerdict5pt(
                score=int(cached.get("score", 0)),
                score_5pt=int(cached.get("score_5pt", 0)),
                matched_facts=list(cached.get("matched_facts", []) or []),
                missing_facts=list(cached.get("missing_facts", []) or []),
                reason=str(cached.get("reason", "")),
            )

    try:
        verdict = _call_reassembler(
            question=question,
            gold=gold,
            sub_queries=sub_queries,
            union_memories=union_memories,
            model=model,
            api_base=api_base,
            api_key=api_key,
            timeout=timeout,
            binary_threshold=binary_threshold,
        )
    except Exception as exc:  # noqa: BLE001 — surface as judge_error
        verdict = JudgeVerdict5pt(
            score=0,
            score_5pt=0,
            reason=f"judge_error: {str(exc)[:480]}",
        )

    if cache is not None:
        cache.set(cache_key, verdict.to_dict())

    return verdict


def _call_reassembler(
    *,
    question: str,
    gold: str,
    sub_queries: list[str],
    union_memories: list[dict],
    model: str,
    api_base: str | None,
    api_key: str | None,
    timeout: int,
    binary_threshold: int,
) -> JudgeVerdict5pt:
    base = (
        api_base
        or os.getenv("LLM_API_BASE")
        or "http://127.0.0.1:8317/v1"
    ).rstrip("/")
    key = (
        api_key
        or os.getenv("CLI_PROXY_API_KEY")
        or os.getenv("LLM_API_KEY")
        or ""
    )

    memories_block = _format_memories_block(union_memories)
    subqs_block = _format_subqs_block(sub_queries)
    n_mem = min(len(union_memories), 60)

    prompt = REASSEMBLER_PROMPT_TEMPLATE.format(
        n_subq=len(sub_queries),
        n_mem=n_mem,
        question=question,
        gold=gold,
        sub_qs_block=subqs_block,
        memories_block=memories_block,
    )

    resp = requests.post(
        f"{base}/chat/completions",
        json={
            "model": model,
            "messages": [{"role": "user", "content": prompt}],
            "response_format": {"type": "json_object"},
            "temperature": 0.0,
        },
        headers={"Authorization": f"Bearer {key}"},
        timeout=timeout,
    )
    resp.raise_for_status()
    content = resp.json()["choices"][0]["message"]["content"]
    parsed = _extract_json_object(content)

    raw_5pt = parsed.get("score_5pt")
    if not isinstance(raw_5pt, int):
        # Tolerate stringy ints from the model.
        try:
            raw_5pt = int(str(raw_5pt).strip())
        except (TypeError, ValueError):
            raise ValueError(  # noqa: B904
                f"reassembler missing/invalid score_5pt: {parsed!r}"
            )
    score_5pt = max(0, min(4, raw_5pt))

    matched_raw = parsed.get("matched_facts") or []
    missing_raw = parsed.get("missing_facts") or []
    matched: list[str] = [str(x) for x in matched_raw if isinstance(x, (str, int, float))]
    missing: list[str] = [str(x) for x in missing_raw if isinstance(x, (str, int, float))]

    reason = str(parsed.get("reasoning") or parsed.get("reason") or "")[:500]

    binary = 1 if score_5pt >= binary_threshold else 0
    return JudgeVerdict5pt(
        score=binary,
        score_5pt=score_5pt,
        matched_facts=matched,
        missing_facts=missing,
        reason=reason,
        raw_response=content,
    )


def flush_shared_cache() -> None:
    if _shared_cache is not None:
        _shared_cache.flush()
