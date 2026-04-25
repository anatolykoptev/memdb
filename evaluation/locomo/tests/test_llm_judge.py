"""
Unit tests for evaluation/locomo/llm_judge.py

Run: python -m pytest evaluation/locomo/tests/test_llm_judge.py -v
  or: python -m unittest evaluation/locomo/tests/test_llm_judge -v
"""

from __future__ import annotations

import hashlib
import json
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

# Make locomo directory importable when running from repo root
_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import llm_judge as lj  # noqa: E402


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _make_cache(tmp_path: Path) -> lj.LLMJudgeCache:
    return lj.LLMJudgeCache(path=tmp_path / ".llm_judge_cache.json")


def _expected_key(q: str, g: str, p: str) -> str:
    payload = q + "\n---\n" + g + "\n---\n" + p
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# Cache key stability
# ---------------------------------------------------------------------------
class TestCacheKeyStability(unittest.TestCase):
    def test_same_inputs_same_key(self) -> None:
        k1 = lj._cache_key("q", "g", "p")
        k2 = lj._cache_key("q", "g", "p")
        self.assertEqual(k1, k2)

    def test_different_inputs_different_keys(self) -> None:
        k1 = lj._cache_key("q1", "g", "p")
        k2 = lj._cache_key("q2", "g", "p")
        self.assertNotEqual(k1, k2)

    def test_order_sensitivity(self) -> None:
        """Gold and prediction in reversed order must produce different key."""
        k1 = lj._cache_key("q", "gold_text", "pred_text")
        k2 = lj._cache_key("q", "pred_text", "gold_text")
        self.assertNotEqual(k1, k2)

    def test_matches_expected_sha256(self) -> None:
        q, g, p = "question", "gold", "pred"
        expected = _expected_key(q, g, p)
        self.assertEqual(lj._cache_key(q, g, p), expected)


# ---------------------------------------------------------------------------
# Cache hit/miss
# ---------------------------------------------------------------------------
class TestLLMJudgeCache(unittest.TestCase):
    def test_miss_returns_none(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            self.assertIsNone(cache.get("nonexistent_key"))

    def test_set_then_get_returns_value(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            cache.set("k1", {"score": 1, "reason": "correct"})
            result = cache.get("k1")
            self.assertIsNotNone(result)
            self.assertEqual(result["score"], 1)

    def test_flush_persists_to_disk(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / ".llm_judge_cache.json"
            cache = lj.LLMJudgeCache(path=path)
            cache.set("k1", {"score": 0, "reason": "wrong"})
            cache.flush()
            self.assertTrue(path.exists())
            with path.open() as f:
                data = json.load(f)
            self.assertIn("k1", data)

    def test_reload_from_disk(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / ".llm_judge_cache.json"
            # Write cache via first instance
            c1 = lj.LLMJudgeCache(path=path)
            c1.set("reload_key", {"score": 1, "reason": "yes"})
            c1.flush()
            # Read via second instance (fresh load)
            c2 = lj.LLMJudgeCache(path=path)
            result = c2.get("reload_key")
            self.assertIsNotNone(result)
            self.assertEqual(result["score"], 1)

    def test_auto_flush_at_threshold(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / ".llm_judge_cache.json"
            cache = lj.LLMJudgeCache(path=path)
            # Write FLUSH_EVERY entries — should trigger auto-flush
            for i in range(lj._FLUSH_EVERY):
                cache.set(f"key_{i}", {"score": 0, "reason": f"r{i}"})
            # After exactly FLUSH_EVERY writes, dirty should be reset
            self.assertEqual(cache._dirty, 0)
            self.assertTrue(path.exists())

    def test_context_manager_flushes_on_exit(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / ".llm_judge_cache.json"
            with lj.LLMJudgeCache(path=path) as cache:
                cache.set("ctx_key", {"score": 1, "reason": "ok"})
            # After __exit__ the file should exist
            self.assertTrue(path.exists())

    def test_atomic_write_via_tmp_rename(self) -> None:
        """Verify that .tmp file is gone after flush."""
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / ".llm_judge_cache.json"
            cache = lj.LLMJudgeCache(path=path)
            cache.set("a", {"score": 0, "reason": "x"})
            cache.flush()
            tmp_file = Path(str(path) + ".tmp")
            self.assertFalse(tmp_file.exists(), ".tmp file should be renamed away")

    def test_thread_safety(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            errors: list[Exception] = []

            def _write(n: int) -> None:
                try:
                    for i in range(20):
                        cache.set(f"t{n}_{i}", {"score": n % 2, "reason": f"r{i}"})
                except Exception as exc:  # noqa: BLE001
                    errors.append(exc)

            threads = [threading.Thread(target=_write, args=(i,)) for i in range(5)]
            for t in threads:
                t.start()
            for t in threads:
                t.join()

            self.assertEqual(errors, [], f"Thread errors: {errors}")


# ---------------------------------------------------------------------------
# judge() function — mock HTTP
# ---------------------------------------------------------------------------
class TestJudgeFunction(unittest.TestCase):
    def _mock_response(self, label: str, reason: str = "looks right") -> MagicMock:
        resp = MagicMock()
        resp.raise_for_status = MagicMock()
        resp.json.return_value = {
            "choices": [{"message": {"content": json.dumps({"label": label, "reason": reason})}}]
        }
        return resp

    def test_correct_answer_returns_score_1(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=self._mock_response("CORRECT")) as mock_post:
                result = lj.judge("q", "gold", "pred", cache=cache)
            mock_post.assert_called_once()
            self.assertEqual(result["score"], 1)
            self.assertNotIn("judge_error", result.get("reason", ""))

    def test_wrong_answer_returns_score_0(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=self._mock_response("WRONG", "nope")):
                result = lj.judge("q", "gold", "bad_pred", cache=cache)
            self.assertEqual(result["score"], 0)

    def test_cache_hit_skips_http(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            key = lj._cache_key("q", "g", "p")
            cache.set(key, {"score": 1, "reason": "cached"})
            with patch("requests.post") as mock_post:
                result = lj.judge("q", "g", "p", cache=cache)
            mock_post.assert_not_called()
            self.assertEqual(result["score"], 1)

    def test_network_error_returns_judge_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", side_effect=ConnectionError("unreachable")):
                result = lj.judge("q", "g", "p", cache=cache)
            self.assertEqual(result["score"], 0)
            self.assertTrue(result["reason"].startswith("judge_error:"))

    def test_timeout_error_returns_judge_error(self) -> None:
        import requests as req_module

        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", side_effect=req_module.exceptions.Timeout("timeout")):
                result = lj.judge("q", "g", "p", cache=cache)
            self.assertEqual(result["score"], 0)
            self.assertTrue(result["reason"].startswith("judge_error:"))

    def test_malformed_json_response_returns_judge_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            bad_resp = MagicMock()
            bad_resp.raise_for_status = MagicMock()
            bad_resp.json.return_value = {
                "choices": [{"message": {"content": "not json {"}}]
            }
            with patch("requests.post", return_value=bad_resp):
                result = lj.judge("q", "g", "p", cache=cache)
            self.assertEqual(result["score"], 0)
            self.assertTrue(result["reason"].startswith("judge_error:"))

    def test_reason_truncated_to_500_chars(self) -> None:
        long_reason = "x" * 1000
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch(
                "requests.post",
                return_value=self._mock_response("CORRECT", long_reason),
            ):
                result = lj.judge("q", "g", "p", cache=cache)
            self.assertLessEqual(len(result["reason"]), 500)

    def test_error_result_stored_in_cache(self) -> None:
        """judge_error results are still cached to avoid re-hitting a bad endpoint."""
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", side_effect=ConnectionError("down")):
                lj.judge("q", "g", "p", cache=cache)
            # Second call should hit cache, not make another request
            with patch("requests.post") as mock_post:
                result = lj.judge("q", "g", "p", cache=cache)
            mock_post.assert_not_called()
            self.assertEqual(result["score"], 0)

    def test_nested_json_response_without_label_returns_judge_error(self) -> None:
        """When LLM returns nested JSON and _extract_json grabs the inner dict (no 'label'),
        _call_judge must escalate to judge_error instead of silently scoring 0."""
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            # Simulate response where inner {} is matched first by \{[^{}]+\} regex
            nested_content = json.dumps({
                "explanation": "The answer matches perfectly",
                "details": {"confidence": 0.9},
            })
            nested_resp = MagicMock()
            nested_resp.raise_for_status = MagicMock()
            nested_resp.json.return_value = {
                "choices": [{"message": {"content": nested_content}}]
            }
            with patch("requests.post", return_value=nested_resp):
                result = lj.judge("q", "gold", "pred", cache=cache)
        self.assertEqual(result["score"], 0)
        self.assertTrue(
            result["reason"].startswith("judge_error:"),
            f"Expected reason to start with 'judge_error:', got: {result['reason']!r}",
        )

    def test_extract_json_handles_nested_object(self) -> None:
        """_extract_json with text + nested JSON: direct json.loads of the full string
        succeeds and returns the outer dict containing 'label'."""
        content = json.dumps({"label": "CORRECT", "meta": {"x": 1}})
        result = lj._extract_json(content)
        # The outer dict (with 'label') must be returned when direct parse succeeds
        self.assertIn("label", result)
        self.assertEqual(result["label"], "CORRECT")


# ---------------------------------------------------------------------------
# Live smoke test (skipped if proxy unreachable)
# ---------------------------------------------------------------------------
class TestLiveSmoke(unittest.TestCase):
    PROXY_BASE = "http://127.0.0.1:8317/v1"

    @classmethod
    def _proxy_reachable(cls) -> bool:
        import requests as r

        try:
            # Any response (even 4xx) means the proxy is up; ConnectionRefused/timeout = down
            r.get(cls.PROXY_BASE.replace("/v1", ""), timeout=2)
            return True
        except (r.exceptions.ConnectionError, r.exceptions.Timeout):
            return False
        except Exception:  # noqa: BLE001
            return True  # got some response — proxy is up

    @classmethod
    def _get_api_key(cls) -> str:
        import os

        key = os.getenv("CLI_PROXY_API_KEY") or os.getenv("LLM_API_KEY") or ""
        if not key:
            # Try loading from ~/.openclaw/.env
            env_file = Path.home() / ".openclaw" / ".env"
            if env_file.exists():
                for line in env_file.read_text().splitlines():
                    line = line.strip()
                    if line.startswith("CLI_PROXY_API_KEY="):
                        key = line.split("=", 1)[1].strip()
                        break
        return key

    def test_live_correct_answer(self) -> None:
        if not self._proxy_reachable():
            self.skipTest("CLIProxyAPI at :8317 unreachable — live smoke deferred")
        api_key = self._get_api_key()
        if not api_key:
            self.skipTest("CLI_PROXY_API_KEY not found — live smoke deferred")
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            result = lj.judge(
                question="Do you remember what I got the last time I went to Hawaii?",
                gold="A shell necklace",
                prediction="You got a shell necklace from Hawaii.",
                api_base=self.PROXY_BASE,
                api_key=api_key,
                cache=cache,
            )
        self.assertEqual(result["score"], 1, f"Expected CORRECT, got: {result}")

    def test_live_wrong_answer(self) -> None:
        if not self._proxy_reachable():
            self.skipTest("CLIProxyAPI at :8317 unreachable — live smoke deferred")
        api_key = self._get_api_key()
        if not api_key:
            self.skipTest("CLI_PROXY_API_KEY not found — live smoke deferred")
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            result = lj.judge(
                question="What is Caroline's job?",
                gold="counselor",
                prediction="Caroline is a professional chef who runs a restaurant.",
                api_base=self.PROXY_BASE,
                api_key=api_key,
                cache=cache,
            )
        self.assertEqual(result["score"], 0, f"Expected WRONG, got: {result}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
