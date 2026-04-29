"""
judge_cot.py — Chain-of-thought judge: prompt template, response parser,
self-consistency check (M13 J1).

Output contract: judge returns JSON
    {"reasoning": "<brief justification>", "score_5pt": <int 0..4>}
where:
    0 = wrong            — answer is incorrect or completely off-topic
    1 = mostly_wrong     — touches the topic but factual content is wrong
    2 = partial          — partially correct (some elements right, others wrong/missing)
    3 = mostly_correct   — captures the gold answer, possibly with extra noise
    4 = correct          — fully correct, semantically equivalent to gold

`threshold ≥ 3` (default) → final binary verdict CORRECT.

Self-consistency: if the parsed `reasoning` text strongly contradicts the parsed
`score_5pt` (e.g. score=4 but reasoning starts "the answer is wrong"), we flag
the trial as inconsistent. The flag is surfaced via `kappa_w_self < 1.0` in the
aggregate verdict so callers can de-weight it.

Backward-compat: a parser for the v1 binary `{"label": "CORRECT|WRONG"}` is kept
under `parse_v1_binary_response` so cfg.scale="binary" mode continues to work
verbatim against existing cache files.
"""

from __future__ import annotations

import json
import re
from typing import Any


# ---------------------------------------------------------------------------
# 5-point CoT prompt
# ---------------------------------------------------------------------------
COT_5POINT_PROMPT = """\
Your task is to grade a generated answer against a gold (ground-truth) answer for a question.

Use this 5-point scale:
  0 = wrong            (incorrect or off-topic)
  1 = mostly_wrong     (touches the topic but the factual content is wrong)
  2 = partial          (partially correct — some elements right, others wrong or missing)
  3 = mostly_correct   (captures the gold answer; may include extra correct or harmless detail)
  4 = correct          (fully correct, semantically equivalent to gold)

Be GENEROUS with paraphrase, format, and verbosity:
  - "May 7th" === "7 May" → 4 (correct)
  - Gold "A shell necklace" + Generated "You bought a shell necklace from Hawaii" → 4 (correct)
  - Gold "3 children" + Generated "two" → 0 (wrong) — the number is different
  - Gold "counselor" + Generated "Caroline works in healthcare as a counselor" → 4
  - Gold "counselor" + Generated "Caroline is a chef" → 0
  - Gold "Italy" + Generated "France or Italy, I think Italy" → 3 (mostly_correct, hedged)
  - Gold "Charlotte's Web" + Generated "the book about a pig and spider" → 2 (partial — topic without title)

For TIME questions: same date in different formats = 4. Adjacent dates (off-by-day or
"about a week later") = 3 (mostly_correct). Wrong week/month = 0.

For NUMERIC questions: exact-match the number, allowing word-form ("two" === "2"). A
different number = 0 even if everything else lines up.

Now grade this:
  Question: {question}
  Gold answer: {gold_answer}
  Generated answer: {generated_answer}

Return ONLY this JSON shape (no prose outside the JSON):
  {{"reasoning": "<one or two sentences explaining the grade>", "score_5pt": <0|1|2|3|4>}}
"""


# Words/phrases that suggest the trial verdict is WRONG/PARTIAL.
# Matched on the lowercased reasoning. Whitespace boundaries (\b) used to avoid
# false positives like "incorporated" matching "incorrect".
_NEGATIVE_REASONING = re.compile(
    r"\b(?:wrong|incorrect|does\s+not\s+match|doesn'?t\s+match|"
    r"different\s+(?:number|date|fact)|off[\- ]topic|not\s+correct|"
    r"opposite|contradicts?|fails?\s+to)\b",
    re.IGNORECASE,
)
# Phrases that suggest the trial verdict is CORRECT.
_POSITIVE_REASONING = re.compile(
    r"\b(?:fully\s+correct|matches\s+(?:the\s+)?gold|semantically\s+equivalent|"
    r"(?:is\s+|are\s+)correct|same\s+(?:answer|date|number)|equivalent\s+to)\b",
    re.IGNORECASE,
)


def parse_cot_response(content: str) -> dict[str, Any]:
    """Parse 5-point CoT JSON from raw LLM `content`.

    Returns:
        {"reasoning": str, "score_5pt": int (0..4), "raw": str, "parse_error": str|None}

    On any failure: score_5pt = 0, parse_error populated. Caller decides how to
    treat (typically: count toward kappa as a trial that disagrees with the rest).
    """
    raw = content
    parsed_obj = _extract_json_object(content)
    if parsed_obj is None:
        return {
            "reasoning": "",
            "score_5pt": 0,
            "raw": raw,
            "parse_error": f"could not extract JSON from response: {content[:200]!r}",
        }

    score_raw = parsed_obj.get("score_5pt")
    reasoning = parsed_obj.get("reasoning") or parsed_obj.get("reason") or ""

    try:
        score = int(score_raw)
    except (TypeError, ValueError):
        return {
            "reasoning": str(reasoning)[:500],
            "score_5pt": 0,
            "raw": raw,
            "parse_error": f"score_5pt missing or not an int: {score_raw!r}",
        }

    if not 0 <= score <= 4:
        return {
            "reasoning": str(reasoning)[:500],
            "score_5pt": 0,
            "raw": raw,
            "parse_error": f"score_5pt out of range [0,4]: {score}",
        }

    return {
        "reasoning": str(reasoning)[:500],
        "score_5pt": score,
        "raw": raw,
        "parse_error": None,
    }


def parse_v1_binary_response(content: str) -> dict[str, Any]:
    """Parse the legacy v1 binary judge response: {"label": "CORRECT"|"WRONG"}.

    Mapped to the v2 verdict shape:
        CORRECT → score_5pt=4
        WRONG   → score_5pt=0

    Used when JudgeConfig.scale == "binary" so cache files written before M13
    remain readable by the new pipeline.
    """
    parsed_obj = _extract_json_object(content)
    if parsed_obj is None or "label" not in parsed_obj:
        return {
            "reasoning": "",
            "score_5pt": 0,
            "raw": content,
            "parse_error": f"v1 binary: missing label key in {content[:200]!r}",
        }
    label = str(parsed_obj.get("label", "")).strip().upper()
    score = 4 if label == "CORRECT" else 0
    reason = str(parsed_obj.get("reason", label))[:500]
    return {
        "reasoning": reason,
        "score_5pt": score,
        "raw": content,
        "parse_error": None,
    }


def is_self_consistent(reasoning: str, score_5pt: int) -> bool:
    """Cheap heuristic: does the prose contradict the numeric score?

    Rule:
      - If score_5pt >= 3 (CORRECT-leaning) but reasoning matches a NEGATIVE phrase
        without a positive override, flag as inconsistent.
      - If score_5pt <= 1 (WRONG-leaning) but reasoning matches a POSITIVE phrase
        without a negative override, flag as inconsistent.
      - Score in {2} (partial) is ambiguous — never flagged on prose alone.

    Self-consistent reasoning is the *common* case; we only flag clear contradictions.
    """
    if not reasoning:
        return True  # nothing to contradict — accept the numeric score
    if score_5pt >= 3:
        if _NEGATIVE_REASONING.search(reasoning) and not _POSITIVE_REASONING.search(reasoning):
            return False
    elif score_5pt <= 1:
        if _POSITIVE_REASONING.search(reasoning) and not _NEGATIVE_REASONING.search(reasoning):
            return False
    return True


# ---------------------------------------------------------------------------
# Internal: tolerant JSON extraction (mirrors llm_judge._extract_json patterns)
# ---------------------------------------------------------------------------
_FENCE_RE = re.compile(r"^```(?:json)?\s*\n", re.IGNORECASE)


def _extract_json_object(content: str) -> dict[str, Any] | None:
    """Try several strategies to pull a JSON object out of a model response."""
    if not content:
        return None
    stripped = content.strip()

    # Strategy 1: markdown code fence
    if stripped.startswith("```"):
        # drop leading fence + optional language tag
        body = _FENCE_RE.sub("", stripped, count=1)
        # drop trailing fence
        if body.endswith("```"):
            body = body[: -len("```")]
        stripped = body.strip()

    # Strategy 2: direct parse
    try:
        obj = json.loads(stripped)
        if isinstance(obj, dict):
            return obj
    except json.JSONDecodeError:
        pass

    # Strategy 3: scan for outermost balanced {...} that contains target keys.
    # Prefer the FIRST top-level object that looks like our schema (has score_5pt
    # OR label) over inner nested {...} blocks.
    candidates = list(_iter_balanced_objects(content))
    # Prefer ones containing our schema keys
    for body in candidates:
        try:
            obj = json.loads(body)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict) and ("score_5pt" in obj or "label" in obj):
            return obj
    # Fallback: any parseable dict
    for body in candidates:
        try:
            obj = json.loads(body)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            return obj
    return None


def _iter_balanced_objects(text: str):
    """Yield substrings that look like balanced {...} objects, outer-first."""
    depth = 0
    start = -1
    for i, ch in enumerate(text):
        if ch == "{":
            if depth == 0:
                start = i
            depth += 1
        elif ch == "}":
            if depth > 0:
                depth -= 1
                if depth == 0 and start >= 0:
                    yield text[start : i + 1]
                    start = -1
