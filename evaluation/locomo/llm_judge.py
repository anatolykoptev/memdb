"""
llm_judge.py — LLM-based correctness judge for LoCoMo QA evaluation (M13 J1).

Capabilities (M13 J1):
  * Multi-trial majority vote (default n=3, configurable via JudgeConfig.n_trials)
  * 5-point granular scale + threshold (default ≥3 → CORRECT)
  * Chain-of-thought reasoning (judge_cot.COT_5POINT_PROMPT) with self-consistency check
  * Reference normalization (Unicode NFC → lowercase → punctuation strip → contractions)
  * Per-run cache invalidation via JudgeConfig.cache_invalidation_key
  * Cache key includes config signature so different configs don't share entries

Backward compatibility:
  * cfg.scale="binary" preserves the legacy 1-trial CORRECT/WRONG behaviour using the
    Memobase-style ACCURACY_PROMPT (`_ACCURACY_PROMPT`). v1 cache files (where the
    cached value has shape {"score": 0|1, "reason": str}) remain readable; the
    pipeline accepts either {"score":..} or the new {"verdict":..} schema.
  * The legacy `judge(question, gold, prediction, ...)` function is preserved with
    its v1 return shape {"score": int, "reason": str} so existing callers
    (score.py without --judge-config) work unchanged.

API:
    @dataclass
    class JudgeConfig: ...
    @dataclass
    class JudgeVerdict: ...
    judge_prediction(question, gold, prediction, cfg) -> JudgeVerdict
    judge_predictions_batch(predictions, cfg, workers=10) -> list[JudgeVerdict]

Cache:
    SHA-256 of "question\\n---\\ngold\\n---\\nprediction\\n---\\n<cfg.cache_signature>"
    Atomic write via tmp+rename. Flush every 50 new entries or on program exit.
"""

from __future__ import annotations

import hashlib
import json
import os
from collections import Counter
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from typing import Any, Literal

import requests

from judge_cache import (
    LLMJudgeCache,
    _DEFAULT_CACHE_PATH,
    _FLUSH_EVERY,
    _shared_cache,
    flush_shared_cache,
    get_shared_cache,
)
from judge_cot import (
    COT_5POINT_PROMPT,
    is_self_consistent,
    parse_cot_response,
    parse_v1_binary_response,
)
from judge_normalize import normalize_reference

# Re-export names that pre-M13 callers (and existing tests) reference under
# `llm_judge.<name>` even though the implementation moved to judge_cache.py.
__all__ = [
    "JudgeConfig",
    "JudgeVerdict",
    "LLMJudgeCache",
    "_DEFAULT_CACHE_PATH",
    "_FLUSH_EVERY",
    "_cache_key",
    "_extract_json",
    "_shared_cache",
    "_v2_cache_key",
    "flush_shared_cache",
    "get_shared_cache",
    "judge",
    "judge_prediction",
    "judge_predictions_batch",
]

# ---------------------------------------------------------------------------
# Legacy v1 prompt — verbatim from Memobase locomo benchmark.
# Kept so cfg.scale="binary" continues to behave exactly like pre-M13 runs.
# ---------------------------------------------------------------------------
_ACCURACY_PROMPT = """
Your task is to label an answer to a question as 'CORRECT' or 'WRONG'. You will be given the following data:
    (1) a question (posed by one user to another user),
    (2) a 'gold' (ground truth) answer,
    (3) a generated answer
which you will score as CORRECT/WRONG.

The point of the question is to ask about something one user should know about the other user based on their prior conversations.
The gold answer will usually be a concise and short answer that includes the referenced topic, for example:
Question: Do you remember what I got the last time I went to Hawaii?
Gold answer: A shell necklace
The generated answer might be much longer, but you should be generous with your grading - as long as it touches on the same topic as the gold answer, it should be counted as CORRECT.

For time related questions, the gold answer will be a specific date, month, year, etc. The generated answer might be much longer or use relative time references (like "last Tuesday" or "next month"), but you should be generous with your grading - as long as it refers to the same date or time period as the gold answer, it should be counted as CORRECT. Even if the format differs (e.g., "May 7th" vs "7 May"), consider it CORRECT if it's the same date.

Now it's time for the real question:
Question: {question}
Gold answer: {gold_answer}
Generated answer: {generated_answer}

First, provide a short (one sentence) explanation of your reasoning, then finish with CORRECT or WRONG.
Do NOT include both CORRECT and WRONG in your response, or it will break the evaluation script.

Just return the label CORRECT or WRONG in a json format with the key as "label".
"""

# ---------------------------------------------------------------------------
# JudgeConfig + JudgeVerdict
# ---------------------------------------------------------------------------
@dataclass
class JudgeConfig:
    """Knobs for the LLM judge.

    All defaults are M13-J1 production values:
      * 3-trial majority vote
      * 5-point scale, threshold=3 → binary CORRECT
      * CoT reasoning required
      * Reference normalization on
      * No cache invalidation (use existing cache entries)
    """

    n_trials: int = 3
    scale: Literal["binary", "5point"] = "5point"
    threshold: int = 3
    require_cot: bool = True
    normalize: bool = True
    cache_invalidation_key: str | None = None

    # Network + model knobs (kept here so the cfg fully describes a judge run)
    model: str = "gemini-2.5-flash"
    api_base: str | None = None
    api_key: str | None = None
    timeout: int = 30

    @property
    def cache_signature(self) -> str:
        """SHA-256 of config fields that affect verdict, truncated to 16 hex chars.

        Notes:
            * `model` IS included — different models give different verdicts.
            * `n_trials` is NOT included — adding trials should be able to reuse
              previously-completed trials. Per-trial caching keys append the trial
              index (see `_trial_cache_key`).
            * `cache_invalidation_key` lets the caller force a re-judge on demand.
        """
        payload = (
            f"{self.scale}|t={self.threshold}|cot={self.require_cot}|"
            f"norm={self.normalize}|model={self.model}|"
            f"inv={self.cache_invalidation_key or ''}"
        )
        return hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16]


@dataclass
class JudgeVerdict:
    """Aggregate verdict across n_trials.

    Fields:
        verdict           — final binary CORRECT/WRONG (after threshold).
        score_5pt         — majority 5-point score; -1 in pure binary mode.
        trials            — per-trial dicts: {score_5pt, reasoning, raw_response,
                            self_consistent, parse_error}.
        reasoning_summary — reasoning text from the trial that won the majority.
        kappa_w_self      — agreement across trials in [0,1]. 1.0 = all trials
                            agree on the binary verdict AND the score AND no trial
                            failed the self-consistency check.
    """

    verdict: bool
    score_5pt: int
    trials: list[dict] = field(default_factory=list)
    reasoning_summary: str = ""
    kappa_w_self: float = 1.0


# ---------------------------------------------------------------------------
# Cache key derivation (the cache class itself lives in judge_cache.py)
# ---------------------------------------------------------------------------
def _cache_key(question: str, gold: str, prediction: str) -> str:
    """v1 (legacy) cache key — preserved for backward compat with old cache files."""
    payload = question + "\n---\n" + gold + "\n---\n" + prediction
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def _v2_cache_key(
    question: str, gold: str, prediction: str, cfg: JudgeConfig, trial: int = 0
) -> str:
    """v2 cache key includes the config signature and trial index."""
    payload = (
        f"{question}\n---\n{gold}\n---\n{prediction}\n---\n"
        f"{cfg.cache_signature}\n---\n{trial}"
    )
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# v1 legacy judge() — preserved verbatim API for back-compat callers
# ---------------------------------------------------------------------------
def judge(
    question: str,
    gold: str,
    prediction: str,
    *,
    model: str = "gemini-2.5-flash",
    api_base: str | None = None,
    api_key: str | None = None,
    timeout: int = 30,
    cache: LLMJudgeCache | None = None,
) -> dict[str, Any]:
    """Legacy 1-trial binary judge. Returns {"score": 0|1, "reason": str}.

    Calls path: same v1 SHA-256 cache key (no config signature). Existing pre-M13
    cache files keep working with this function unchanged.

    For new code, prefer `judge_prediction(..., JudgeConfig(...))`.
    """
    if cache is None:
        cache = _shared_cache

    key = _cache_key(question, gold, prediction)
    cached = cache.get(key)
    if cached is not None:
        return cached

    result = _call_v1_judge(
        question, gold, prediction,
        model=model, api_base=api_base, api_key=api_key, timeout=timeout,
    )
    cache.set(key, result)
    return result


def _extract_json(content: str) -> dict:
    """Tolerant JSON extractor used by the v1 legacy path.

    Handles three patterns: plain JSON, ```json ``` fence, text + inline {..}.
    Kept here for the v1 binary path; the v2 path uses judge_cot._extract_json_object
    which is stricter about preferring objects with `score_5pt` / `label`.
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

    import re
    matches = list(re.finditer(r"\{[^{}]+\}", content, re.DOTALL))
    for m in reversed(matches):
        try:
            return json.loads(m.group())
        except json.JSONDecodeError:
            continue
    raise ValueError(f"No JSON object found in response: {content[:200]!r}")


def _post_chat_completion(
    prompt: str,
    *,
    model: str,
    api_base: str | None,
    api_key: str | None,
    timeout: int,
) -> str:
    """POST a chat-completions request and return the assistant content string.

    Raises on HTTP / network error so callers can wrap the failure in the
    appropriate verdict shape (v1 returns dict with `judge_error:` reason; v2
    populates trial.parse_error).
    """
    base = (api_base or os.getenv("LLM_API_BASE") or "http://127.0.0.1:8317/v1").rstrip("/")
    key = api_key or os.getenv("CLI_PROXY_API_KEY") or os.getenv("LLM_API_KEY") or ""
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
    return resp.json()["choices"][0]["message"]["content"]


def _call_v1_judge(
    question: str,
    gold: str,
    prediction: str,
    *,
    model: str,
    api_base: str | None,
    api_key: str | None,
    timeout: int,
) -> dict[str, Any]:
    """Make the v1 binary HTTP call; return {"score": 0|1, "reason": str}."""
    prompt = _ACCURACY_PROMPT.format(
        question=question, gold_answer=gold, generated_answer=prediction
    )
    try:
        content = _post_chat_completion(
            prompt, model=model, api_base=api_base, api_key=api_key, timeout=timeout,
        )
        parsed = _extract_json(content)
        if not isinstance(parsed, dict) or "label" not in parsed:
            return {
                "score": 0,
                "reason": f"judge_error: missing label key in response: {content[:200]}",
            }
        label = str(parsed.get("label", "")).strip().upper()
        score = 1 if label == "CORRECT" else 0
        reason = str(parsed.get("reason", label))[:500]
        return {"score": score, "reason": reason}
    except Exception as exc:  # noqa: BLE001
        return {"score": 0, "reason": f"judge_error: {str(exc)[:480]}"}


# ---------------------------------------------------------------------------
# v2 multi-trial judge — judge_prediction / judge_predictions_batch
# ---------------------------------------------------------------------------
def judge_prediction(
    question: str,
    gold: str,
    prediction: str,
    cfg: JudgeConfig | None = None,
    *,
    cache: LLMJudgeCache | None = None,
) -> JudgeVerdict:
    """Run cfg.n_trials judge calls, take majority vote, return JudgeVerdict.

    Trials run sequentially within this function — for batch parallelism, use
    `judge_predictions_batch`. The expectation is that callers parallelize at
    the question level (one worker per question, each running its trials).
    """
    cfg = cfg or JudgeConfig()
    cache = cache if cache is not None else _shared_cache

    # 1. Normalization (purely on the strings handed to the LLM)
    q_in, g_in, p_in = question, gold, prediction
    if cfg.normalize:
        # Question is normalized lightly (just NFC + lowercase) — content matters
        # less than gold/prediction for verdict comparison, and we don't want to
        # nuke punctuation that conveys question structure ("?", "vs.")
        # Gold + prediction get the full pipeline.
        g_in = normalize_reference(gold)
        p_in = normalize_reference(prediction)
        # Keep `question` raw — judge needs to read it for context.

    n = max(1, int(cfg.n_trials))
    trials: list[dict] = []
    for trial_idx in range(n):
        trial = _run_one_trial(q_in, g_in, p_in, cfg, trial_idx, cache)
        trials.append(trial)

    return _aggregate_trials(trials, cfg)


def judge_predictions_batch(
    predictions: list[dict],
    cfg: JudgeConfig | None = None,
    *,
    workers: int = 10,
    cache: LLMJudgeCache | None = None,
) -> list[JudgeVerdict]:
    """Parallel judging across a list of prediction records.

    Each record must expose at least: question, gold_answer (or `gold`),
    prediction (or `chat_answer` / `pred`).

    Output index aligns with input.
    """
    cfg = cfg or JudgeConfig()
    cache = cache if cache is not None else _shared_cache

    def _one(rec: dict) -> JudgeVerdict:
        q = rec.get("question") or ""
        g = rec.get("gold_answer", rec.get("gold", ""))
        p = rec.get("prediction", rec.get("chat_answer", rec.get("pred", "")))
        return judge_prediction(str(q), str(g), str(p), cfg, cache=cache)

    out: list[JudgeVerdict | None] = [None] * len(predictions)
    with ThreadPoolExecutor(max_workers=max(1, workers)) as ex:
        futs = {ex.submit(_one, rec): i for i, rec in enumerate(predictions)}
        for fut in as_completed(futs):
            i = futs[fut]
            out[i] = fut.result()
    cache.flush()
    return [v for v in out if v is not None]


# ---------------------------------------------------------------------------
# Internals — single trial + aggregation
# ---------------------------------------------------------------------------
def _run_one_trial(
    question: str,
    gold: str,
    prediction: str,
    cfg: JudgeConfig,
    trial_idx: int,
    cache: LLMJudgeCache,
) -> dict[str, Any]:
    """Run (or fetch from cache) one trial.

    Returns dict {score_5pt, reasoning, raw_response, self_consistent, parse_error}.
    """
    cache_key = _v2_cache_key(question, gold, prediction, cfg, trial_idx)
    cached = cache.get(cache_key)
    if cached is not None:
        return cached

    result = _call_trial(question, gold, prediction, cfg)
    cache.set(cache_key, result)
    return result


def _call_trial(
    question: str, gold: str, prediction: str, cfg: JudgeConfig
) -> dict[str, Any]:
    """Run one trial — picks prompt + parser based on cfg.scale.

    Returns: {score_5pt, reasoning, raw_response, self_consistent, parse_error}.
    """
    if cfg.scale == "binary":
        prompt = _ACCURACY_PROMPT.format(
            question=question, gold_answer=gold, generated_answer=prediction
        )
        parser = parse_v1_binary_response
        check_self_consistency = False  # v1 binary has no nuanced reasoning to check
    else:
        prompt = COT_5POINT_PROMPT.format(
            question=question, gold_answer=gold, generated_answer=prediction
        )
        parser = parse_cot_response
        check_self_consistency = True

    try:
        content = _post_chat_completion(
            prompt,
            model=cfg.model,
            api_base=cfg.api_base,
            api_key=cfg.api_key,
            timeout=cfg.timeout,
        )
    except Exception as exc:  # noqa: BLE001
        return {
            "score_5pt": 0,
            "reasoning": "",
            "raw_response": "",
            "self_consistent": False,
            "parse_error": f"judge_error: {str(exc)[:480]}",
        }

    parsed = parser(content)
    if check_self_consistency and cfg.require_cot:
        consistent = parsed["parse_error"] is None and is_self_consistent(
            parsed["reasoning"], parsed["score_5pt"]
        )
    else:
        consistent = parsed["parse_error"] is None
    return {
        "score_5pt": parsed["score_5pt"],
        "reasoning": parsed["reasoning"],
        "raw_response": parsed["raw"][:1000],
        "self_consistent": bool(consistent),
        "parse_error": parsed["parse_error"],
    }


def _aggregate_trials(trials: list[dict], cfg: JudgeConfig) -> JudgeVerdict:
    """Majority vote across trials.

    Tie-break rule (when no strict majority on the binary verdict):
      Fall to the LOWER (more conservative) score.
      This mirrors evaluation-harness convention "when in doubt, mark wrong" —
      we'd rather under-report quality than inflate it.
    """
    if not trials:
        return JudgeVerdict(
            verdict=False, score_5pt=-1 if cfg.scale == "binary" else 0,
            trials=[], reasoning_summary="no trials", kappa_w_self=0.0,
        )

    scores = [int(t.get("score_5pt", 0)) for t in trials]
    binary_votes = [s >= cfg.threshold for s in scores]

    n = len(trials)
    correct_count = sum(binary_votes)
    if correct_count > n / 2:
        verdict_bin = True
    elif correct_count < n / 2:
        verdict_bin = False
    else:
        # Even split → conservative = WRONG (lower-score side)
        verdict_bin = False

    # Pick the modal 5-point score from trials whose binary vote matches the
    # winning side; on tie, take the lowest among them (conservative).
    matching_scores = [s for s, b in zip(scores, binary_votes) if b is verdict_bin]
    if matching_scores:
        score_counter = Counter(matching_scores)
        max_count = max(score_counter.values())
        modal_candidates = [s for s, c in score_counter.items() if c == max_count]
        majority_score = min(modal_candidates)  # conservative tie-break
    else:
        majority_score = min(scores)

    # Reasoning summary: pick the first trial whose verdict matches AND that
    # is self-consistent; fall back to the first matching trial.
    summary = ""
    matching_trials = [
        (t, s) for t, s in zip(trials, scores)
        if (s >= cfg.threshold) is verdict_bin
    ]
    for t, _ in matching_trials:
        if t.get("self_consistent", True) and t.get("reasoning"):
            summary = t["reasoning"]
            break
    if not summary and matching_trials:
        summary = matching_trials[0][0].get("reasoning") or ""
    if not summary:
        summary = trials[0].get("reasoning", "") or trials[0].get("parse_error", "") or ""

    # kappa_w_self: 1.0 if all trials agree on the binary verdict AND on the
    # 5-point score AND none failed self-consistency. We blend three signals:
    #   * binary agreement fraction
    #   * 5-point modal agreement fraction
    #   * self-consistency fraction
    # The agreement is the geometric mean; an inconsistent trial pulls it down.
    if n == 1:
        # Single-trial: kappa expresses self-consistency only.
        kappa = 1.0 if trials[0].get("self_consistent", True) else 0.5
    else:
        binary_agree = max(correct_count, n - correct_count) / n
        score_modal_count = max(Counter(scores).values())
        score_agree = score_modal_count / n
        consist_agree = sum(1 for t in trials if t.get("self_consistent", True)) / n
        # Geometric mean — any zero collapses to zero (conservative).
        kappa = (binary_agree * score_agree * consist_agree) ** (1 / 3)

    # Spec contract: score_5pt is the 0..4 majority when scale="5point",
    # else -1 in pure binary mode (the binary scale doesn't expose 5-point
    # granularity to the caller, even though we use 0/4 internally).
    score_5pt_out = majority_score if cfg.scale == "5point" else -1

    return JudgeVerdict(
        verdict=verdict_bin,
        score_5pt=score_5pt_out,
        trials=trials,
        reasoning_summary=summary[:500],
        kappa_w_self=round(kappa, 4),
    )
