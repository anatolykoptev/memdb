"""
llm_judge.py — Binary LLM-based correctness judge for LoCoMo QA evaluation.

Calls Gemini Flash via CLIProxyAPI (http://127.0.0.1:8317/v1) to judge each
prediction as CORRECT (1) or WRONG (0) against the gold answer.

Prompt taken verbatim from Memobase evaluation:
  memobase/docs/experiments/locomo-benchmark/metrics/llm_judge.py
  (ACCURACY_PROMPT, evaluate_llm_judge)

Cache:
  SHA-256 of "question\\n---\\ngold\\n---\\nprediction" → persisted JSON dict.
  Atomic write via tmp+rename. Flush every 50 new entries or on program exit.
"""

from __future__ import annotations

import hashlib
import json
import os
import threading
from pathlib import Path
from typing import Any

import requests

# ---------------------------------------------------------------------------
# Prompt — verbatim from Memobase locomo benchmark
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
# Cache path
# ---------------------------------------------------------------------------
_DEFAULT_CACHE_PATH = (
    Path(__file__).resolve().parent / "results" / ".llm_judge_cache.json"
)

# Flush threshold: write to disk after this many new (un-flushed) entries
_FLUSH_EVERY = 50


def _cache_key(question: str, gold: str, prediction: str) -> str:
    """Stable SHA-256 hash for a (question, gold, prediction) triple."""
    payload = question + "\n---\n" + gold + "\n---\n" + prediction
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# LLMJudgeCache — thread-safe, lazy-load, batch-flush
# ---------------------------------------------------------------------------
class LLMJudgeCache:
    """Persistent disk cache for judge results.

    Usage:
        cache = LLMJudgeCache()          # shared instance
        with cache:                      # enters context: sets up flush-on-exit
            result = cache.get(k)        # returns None on miss
            cache.set(k, result)         # stores; auto-flushes every 50 entries
    """

    def __init__(self, path: Path = _DEFAULT_CACHE_PATH) -> None:
        self.path = path
        self._lock = threading.Lock()
        self._data: dict[str, Any] | None = None  # lazy-loaded
        self._dirty: int = 0  # unflushed writes since last flush

    # --- context manager for "flush on exit" guarantee ---
    def __enter__(self) -> "LLMJudgeCache":
        return self

    def __exit__(self, *_: object) -> None:
        self.flush()

    # --- lazy load ---
    def _load(self) -> dict[str, Any]:
        if self._data is None:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            if self.path.exists():
                try:
                    with self.path.open("r", encoding="utf-8") as f:
                        self._data = json.load(f)
                except (json.JSONDecodeError, OSError):
                    self._data = {}
            else:
                self._data = {}
        return self._data

    # --- public get / set ---
    def get(self, key: str) -> dict[str, Any] | None:
        with self._lock:
            return self._load().get(key)

    def set(self, key: str, value: dict[str, Any]) -> None:
        with self._lock:
            self._load()[key] = value
            self._dirty += 1
            if self._dirty >= _FLUSH_EVERY:
                self._flush_locked()

    def flush(self) -> None:
        with self._lock:
            if self._dirty > 0:
                self._flush_locked()

    def _flush_locked(self) -> None:
        """Atomic write via tmp+rename (POSIX atomic on same filesystem)."""
        if self._data is None:
            return
        tmp = Path(str(self.path) + ".tmp")
        tmp.parent.mkdir(parents=True, exist_ok=True)
        with tmp.open("w", encoding="utf-8") as f:
            json.dump(self._data, f, ensure_ascii=False)
        tmp.rename(self.path)
        self._dirty = 0


# Module-level shared cache (used by default)
_shared_cache = LLMJudgeCache()


# ---------------------------------------------------------------------------
# Judge call
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
    """Return {"score": 0|1, "reason": str}.

    Falls back to {"score": 0, "reason": "judge_error: <msg>"} on any failure.
    Uses shared module cache unless an explicit cache instance is provided.
    """
    if cache is None:
        cache = _shared_cache

    key = _cache_key(question, gold, prediction)
    cached = cache.get(key)
    if cached is not None:
        return cached

    result = _call_judge(
        question,
        gold,
        prediction,
        model=model,
        api_base=api_base,
        api_key=api_key,
        timeout=timeout,
    )
    cache.set(key, result)
    return result


def _extract_json(content: str) -> dict:
    """Extract a JSON object from LLM response content.

    Handles three common patterns:
    1. Plain JSON:  {"label": "CORRECT"}
    2. Markdown code fence:  ```json\\n{...}\\n```
    3. Text + inline JSON:  "The answer is correct.\\n{"label": "CORRECT"}"
    """
    stripped = content.strip()
    # Pattern 1: markdown code fence
    if stripped.startswith("```"):
        lines = stripped.splitlines()
        lines = lines[1:]  # drop opening fence
        if lines and lines[-1].strip() == "```":
            lines = lines[:-1]  # drop closing fence
        stripped = "\n".join(lines).strip()

    # Pattern 2: try direct parse first (clean JSON or after fence strip)
    try:
        return json.loads(stripped)
    except json.JSONDecodeError:
        pass

    # Pattern 3: find the last {...} block in the content
    import re

    matches = list(re.finditer(r"\{[^{}]+\}", content, re.DOTALL))
    for m in reversed(matches):
        try:
            return json.loads(m.group())
        except json.JSONDecodeError:
            continue

    raise ValueError(f"No JSON object found in response: {content[:200]!r}")


def _call_judge(
    question: str,
    gold: str,
    prediction: str,
    *,
    model: str,
    api_base: str | None,
    api_key: str | None,
    timeout: int,
) -> dict[str, Any]:
    """Make the actual HTTP call to the LLM judge; parse and return result."""
    base = (api_base or os.getenv("LLM_API_BASE") or "http://127.0.0.1:8317/v1").rstrip("/")
    key = api_key or os.getenv("CLI_PROXY_API_KEY") or os.getenv("LLM_API_KEY") or ""

    prompt = _ACCURACY_PROMPT.format(
        question=question,
        gold_answer=gold,
        generated_answer=prediction,
    )

    try:
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
        parsed = _extract_json(content)
        if not isinstance(parsed, dict) or "label" not in parsed:
            return {"score": 0, "reason": f"judge_error: missing label key in response: {content[:200]}"}
        label: str = parsed.get("label", "").strip().upper()
        score = 1 if label == "CORRECT" else 0
        # Use reason key if present, else the reasoning text before the JSON
        reason = str(parsed.get("reason", label))[:500]
        return {"score": score, "reason": reason}
    except Exception as exc:  # noqa: BLE001
        return {"score": 0, "reason": f"judge_error: {str(exc)[:480]}"}


# ---------------------------------------------------------------------------
# Flush helpers for score.py lifecycle management
# ---------------------------------------------------------------------------
def flush_shared_cache() -> None:
    """Flush the module-level shared cache to disk."""
    _shared_cache.flush()


def get_shared_cache() -> LLMJudgeCache:
    """Return the module-level shared cache (for use as context manager)."""
    return _shared_cache
