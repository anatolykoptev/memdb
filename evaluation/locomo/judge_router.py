"""
judge_router.py — Hybrid programmatic+LLM verdict dispatcher for LoCoMo.

Flow:
    judge_dispatch(question, gold, pred, category, llm_judge_fn)
        ├─ run_programmatic(category, gold, pred)
        │       │
        │       ├─ confidence ≥ PROGRAMMATIC_THRESHOLD (0.85)
        │       │       └→ return ProgrammaticVerdict (no LLM call)
        │       │
        │       └─ otherwise (low conf / N/A)
        │               └→ fall through to llm_judge_fn(question, gold, pred)
        │
        └─ metrics: per-category counters incremented on every dispatch
                   so we can audit programmatic coverage in production.

The router is the ONLY place that decides "skip LLM".  Extractors set
confidence; the router enforces the cut-off.  This keeps the policy in one
place — easy to retune from a single constant without touching extractors.

🚨 NEVER silently disable: if all extractors abstain, llm_judge_fn MUST be
called.  We do not return a free pass.
"""

from __future__ import annotations

import threading
from collections import Counter
from dataclasses import dataclass, field
from typing import Any, Callable

from judge_programmatic import (  # local import — same dir
    ProgrammaticVerdict,
    run_programmatic,
)

# ---------------------------------------------------------------------------
# Decision threshold — keep in sync with judge_programmatic.ROUTER_THRESHOLD.
# ---------------------------------------------------------------------------
PROGRAMMATIC_THRESHOLD = 0.85


# ---------------------------------------------------------------------------
# Result type — superset of LLM judge's {"score","reason"} payload so the
# rest of the harness can stay unchanged.
# ---------------------------------------------------------------------------
@dataclass
class JudgeVerdict:
    score: int                     # 0 / 1
    reason: str                    # human-readable
    source: str                    # "programmatic" | "llm"
    method: str                    # extractor method or LLM model name
    confidence: float              # programmatic 0..1, LLM = 1.0 by convention
    extracted: dict[str, Any] = field(default_factory=dict)

    def as_score_reason(self) -> dict[str, Any]:
        """Backward-compatible {"score","reason"} dict for score.py."""
        return {"score": self.score, "reason": self.reason}

    @classmethod
    def from_programmatic(cls, v: ProgrammaticVerdict) -> "JudgeVerdict":
        return cls(
            score=1 if v.verdict else 0,
            reason=f"programmatic[{v.method}] conf={v.confidence:.2f}",
            source="programmatic",
            method=v.method,
            confidence=v.confidence,
            extracted=dict(v.extracted),
        )

    @classmethod
    def from_llm(cls, llm_result: dict[str, Any], model: str = "llm") -> "JudgeVerdict":
        return cls(
            score=int(llm_result.get("score", 0)),
            reason=str(llm_result.get("reason", "")),
            source="llm",
            method=model,
            confidence=1.0,
            extracted={},
        )


# ---------------------------------------------------------------------------
# Metrics — process-wide counters; thread-safe.
# ---------------------------------------------------------------------------
class _RouterMetrics:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._programmatic: Counter[str] = Counter()  # method → count
        self._programmatic_correct: Counter[str] = Counter()
        self._fallthrough: Counter[int] = Counter()   # category → count
        self._llm_calls: int = 0

    def record_programmatic(self, category: int, method: str, verdict: bool) -> None:
        with self._lock:
            key = f"cat{category}:{method}"
            self._programmatic[key] += 1
            if verdict:
                self._programmatic_correct[key] += 1

    def record_fallthrough(self, category: int) -> None:
        with self._lock:
            self._fallthrough[int(category) if category is not None else -1] += 1
            self._llm_calls += 1

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            return {
                "programmatic_by_method": dict(self._programmatic),
                "programmatic_correct_by_method": dict(self._programmatic_correct),
                "fallthrough_by_category": dict(self._fallthrough),
                "llm_calls": self._llm_calls,
                "programmatic_total": sum(self._programmatic.values()),
            }

    def reset(self) -> None:
        with self._lock:
            self._programmatic.clear()
            self._programmatic_correct.clear()
            self._fallthrough.clear()
            self._llm_calls = 0


_METRICS = _RouterMetrics()


def get_metrics() -> dict[str, Any]:
    """Snapshot of router metrics for reporting / score.py."""
    return _METRICS.snapshot()


def reset_metrics() -> None:
    """Test helper — zeroes the global counters."""
    _METRICS.reset()


def record_programmatic_metric(category: int, method: str, verdict: bool) -> None:
    """Public entry-point for tests that want to bump counters directly."""
    _METRICS.record_programmatic(category, method, verdict)


# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------
LLMJudgeFn = Callable[[str, str, str], dict[str, Any]]


def judge_dispatch(
    question: str,
    gold: str,
    pred: str,
    category: int | None,
    llm_judge_fn: LLMJudgeFn,
    *,
    llm_model: str = "llm",
    threshold: float = PROGRAMMATIC_THRESHOLD,
) -> JudgeVerdict:
    """Try programmatic extractors first; fall through to LLM on low conf / N/A.

    Args:
        question:     User question (passed to LLM if invoked).
        gold:         Gold answer.
        pred:         Predicted answer (chat output).
        category:     LoCoMo category int (1..5).  ``None`` → straight to LLM.
        llm_judge_fn: Callable (q, g, p) → {"score": 0|1, "reason": str}.
                      Typically ``llm_judge.judge`` bound with model/cache.
        llm_model:    Identifier recorded in JudgeVerdict.method when LLM used.
        threshold:    Programmatic confidence cut-off; default 0.85.

    Returns:
        JudgeVerdict — either programmatic or LLM-sourced.
    """
    cat_int = int(category) if category is not None else -1

    if cat_int in (1, 3):
        prog = run_programmatic(cat_int, gold, pred)
        if prog is not None and prog.confidence >= threshold:
            _METRICS.record_programmatic(cat_int, prog.method, prog.verdict)
            return JudgeVerdict.from_programmatic(prog)

    # Fall through (programmatic abstained / unsupported category / low conf)
    _METRICS.record_fallthrough(cat_int)
    llm_result = llm_judge_fn(question, gold, pred)
    return JudgeVerdict.from_llm(llm_result, model=llm_model)


# ---------------------------------------------------------------------------
# Convenience wrapper for score.py — keeps the existing {"score","reason"}
# return shape so callers don't need to change.
# ---------------------------------------------------------------------------
def judge_dispatch_score_reason(
    question: str,
    gold: str,
    pred: str,
    category: int | None,
    llm_judge_fn: LLMJudgeFn,
    *,
    llm_model: str = "llm",
) -> dict[str, Any]:
    """Same as judge_dispatch but returns the legacy {score,reason} dict.

    Adds two extra keys ("source" and "method") that score.py can record but
    that won't break callers expecting only score/reason.
    """
    v = judge_dispatch(
        question=question,
        gold=gold,
        pred=pred,
        category=category,
        llm_judge_fn=llm_judge_fn,
        llm_model=llm_model,
    )
    return {
        "score": v.score,
        "reason": v.reason,
        "source": v.source,
        "method": v.method,
        "confidence": v.confidence,
    }
