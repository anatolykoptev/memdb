"""
judge_cache.py — Persistent disk cache for LLM-judge results.

Concern split out of llm_judge.py at M13 J1: the cache is fully independent of
the prompt + parser pipeline (it just stores arbitrary dicts under SHA-256 keys).
Keeping it isolated makes the unit-test surface for `LLMJudgeCache` clean and
lets future judge implementations (programmatic / ensemble) reuse it.

Behaviour:
  * Lazy-load: cache file is read on first `get` / `set`, not at construction.
  * Atomic write: tmp + rename (POSIX atomic on the same filesystem).
  * Batch flush: auto-persists every `_FLUSH_EVERY` writes; explicit `flush()` or
    context-manager exit also persist.
  * Thread-safe: a single `threading.Lock` guards reads + writes + flush.
"""

from __future__ import annotations

import json
import threading
from pathlib import Path
from typing import Any


# Default cache location — colocated with the evaluation results directory.
_DEFAULT_CACHE_PATH = (
    Path(__file__).resolve().parent / "results" / ".llm_judge_cache.json"
)

# Flush threshold: write to disk after this many new (un-flushed) entries.
_FLUSH_EVERY = 50


class LLMJudgeCache:
    """Persistent disk cache for judge results.

    Usage:
        cache = LLMJudgeCache()           # shared instance (default path)
        with cache:                       # entering context flushes on exit
            result = cache.get(k)         # returns None on miss
            cache.set(k, result)          # stores; auto-flushes every 50 entries
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


# Module-level shared cache (used by default by llm_judge.judge / judge_prediction).
_shared_cache = LLMJudgeCache()


def get_shared_cache() -> LLMJudgeCache:
    """Return the module-level shared cache (for use as context manager)."""
    return _shared_cache


def flush_shared_cache() -> None:
    """Flush the module-level shared cache to disk."""
    _shared_cache.flush()
