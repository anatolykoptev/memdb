"""
Unit tests for the timeout/5xx retry logic in evaluation/locomo/query.py.

Run: python -m pytest evaluation/locomo/tests/test_query_retry.py -v
"""

from __future__ import annotations

import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch, call

import requests

# Make locomo directory importable when running from repo root
_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import query as q  # noqa: E402


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _http_error(status_code: int, body: str = "") -> requests.HTTPError:
    """Build a requests.HTTPError with a mock response."""
    resp = MagicMock(spec=requests.Response)
    resp.status_code = status_code
    resp.text = body
    exc = requests.HTTPError(f"{status_code} Server Error", response=resp)
    return exc


def _timeout_exc() -> requests.exceptions.Timeout:
    return requests.exceptions.Timeout("Read timed out")


def _conn_error_exc() -> requests.exceptions.ConnectionError:
    return requests.exceptions.ConnectionError("Connection refused")


# ---------------------------------------------------------------------------
# _is_transient_http_error
# ---------------------------------------------------------------------------

class TestIsTransientHttpError(unittest.TestCase):
    def test_502_is_transient(self) -> None:
        self.assertTrue(q._is_transient_http_error(_http_error(502)))

    def test_503_is_transient(self) -> None:
        self.assertTrue(q._is_transient_http_error(_http_error(503)))

    def test_504_is_transient(self) -> None:
        self.assertTrue(q._is_transient_http_error(_http_error(504)))

    def test_500_internal_server_error_is_transient(self) -> None:
        exc = _http_error(500, "Internal Server Error")
        self.assertTrue(q._is_transient_http_error(exc))

    def test_500_context_deadline_is_transient(self) -> None:
        exc = _http_error(500, "context deadline exceeded")
        self.assertTrue(q._is_transient_http_error(exc))

    def test_500_not_found_is_not_transient(self) -> None:
        exc = _http_error(500, "user not found")
        self.assertFalse(q._is_transient_http_error(exc))

    def test_400_is_not_transient(self) -> None:
        self.assertFalse(q._is_transient_http_error(_http_error(400)))

    def test_404_is_not_transient(self) -> None:
        self.assertFalse(q._is_transient_http_error(_http_error(404)))

    def test_no_response_is_not_transient(self) -> None:
        exc = requests.HTTPError("bad")
        exc.response = None
        self.assertFalse(q._is_transient_http_error(exc))


# ---------------------------------------------------------------------------
# _is_retryable
# ---------------------------------------------------------------------------

class TestIsRetryable(unittest.TestCase):
    def test_timeout_is_retryable(self) -> None:
        self.assertTrue(q._is_retryable(_timeout_exc()))

    def test_connection_error_is_retryable(self) -> None:
        self.assertTrue(q._is_retryable(_conn_error_exc()))

    def test_503_is_retryable(self) -> None:
        self.assertTrue(q._is_retryable(_http_error(503)))

    def test_500_transient_is_retryable(self) -> None:
        self.assertTrue(q._is_retryable(_http_error(500, "Internal Server Error")))

    def test_400_is_not_retryable(self) -> None:
        self.assertFalse(q._is_retryable(_http_error(400)))

    def test_404_is_not_retryable(self) -> None:
        self.assertFalse(q._is_retryable(_http_error(404)))

    def test_500_permanent_is_not_retryable(self) -> None:
        self.assertFalse(q._is_retryable(_http_error(500, "invalid user_id")))

    def test_value_error_is_not_retryable(self) -> None:
        self.assertFalse(q._is_retryable(ValueError("bad")))


# ---------------------------------------------------------------------------
# _short_reason
# ---------------------------------------------------------------------------

class TestShortReason(unittest.TestCase):
    def test_timeout_reason(self) -> None:
        self.assertEqual(q._short_reason(_timeout_exc()), "timeout")

    def test_connection_error_reason(self) -> None:
        self.assertEqual(q._short_reason(_conn_error_exc()), "connection_error")

    def test_503_reason(self) -> None:
        self.assertEqual(q._short_reason(_http_error(503)), "http_503")

    def test_500_reason(self) -> None:
        self.assertEqual(q._short_reason(_http_error(500)), "http_500")


# ---------------------------------------------------------------------------
# _call_with_retry — success path
# ---------------------------------------------------------------------------

class TestCallWithRetrySuccess(unittest.TestCase):
    def test_succeeds_first_attempt(self) -> None:
        fn = MagicMock(return_value="ok")
        result = q._call_with_retry(fn, "a", max_retries=2, backoff_base=0)
        self.assertEqual(result, "ok")
        fn.assert_called_once_with("a")

    def test_succeeds_on_second_attempt(self) -> None:
        fn = MagicMock(side_effect=[_timeout_exc(), "ok"])
        with patch("time.sleep"):
            result = q._call_with_retry(fn, max_retries=2, backoff_base=0)
        self.assertEqual(result, "ok")
        self.assertEqual(fn.call_count, 2)

    def test_succeeds_on_third_attempt(self) -> None:
        fn = MagicMock(side_effect=[_timeout_exc(), _http_error(503), "ok"])
        with patch("time.sleep"):
            result = q._call_with_retry(fn, max_retries=2, backoff_base=0)
        self.assertEqual(result, "ok")
        self.assertEqual(fn.call_count, 3)


# ---------------------------------------------------------------------------
# _call_with_retry — failure / exhaustion path
# ---------------------------------------------------------------------------

class TestCallWithRetryExhaustion(unittest.TestCase):
    def test_raises_after_exhaustion(self) -> None:
        exc = _timeout_exc()
        fn = MagicMock(side_effect=exc)
        with patch("time.sleep"):
            with self.assertRaises(requests.exceptions.Timeout):
                q._call_with_retry(fn, max_retries=2, backoff_base=0)
        self.assertEqual(fn.call_count, 3)  # 1 initial + 2 retries

    def test_no_retry_for_non_retryable(self) -> None:
        exc = _http_error(400)
        fn = MagicMock(side_effect=exc)
        with self.assertRaises(requests.HTTPError):
            q._call_with_retry(fn, max_retries=2, backoff_base=0)
        fn.assert_called_once()

    def test_zero_retries_parity_with_no_retry(self) -> None:
        """--retry-on-timeout 0 must behave identically to legacy (no retries)."""
        exc = _timeout_exc()
        fn = MagicMock(side_effect=exc)
        with self.assertRaises(requests.exceptions.Timeout):
            q._call_with_retry(fn, max_retries=0, backoff_base=0)
        fn.assert_called_once()

    def test_non_retryable_500_raises_immediately(self) -> None:
        exc = _http_error(500, "invalid user_id")
        fn = MagicMock(side_effect=exc)
        with self.assertRaises(requests.HTTPError):
            q._call_with_retry(fn, max_retries=3, backoff_base=0)
        fn.assert_called_once()


# ---------------------------------------------------------------------------
# _call_with_retry — backoff doubling
# ---------------------------------------------------------------------------

class TestCallWithRetryBackoff(unittest.TestCase):
    def test_backoff_doubles(self) -> None:
        fn = MagicMock(side_effect=[_timeout_exc(), _timeout_exc(), "ok"])
        sleep_calls: list[float] = []
        with patch("time.sleep", side_effect=lambda s: sleep_calls.append(s)):
            result = q._call_with_retry(fn, max_retries=2, backoff_base=5)
        self.assertEqual(result, "ok")
        self.assertEqual(sleep_calls, [5, 10])


# ---------------------------------------------------------------------------
# _load_qids_from_file
# ---------------------------------------------------------------------------

class TestLoadQidsFromFile(unittest.TestCase):
    def test_basic(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as fh:
            fh.write("conv-30 46\nconv-30 47\n")
            path = Path(fh.name)
        try:
            result = q._load_qids_from_file(path)
            self.assertEqual(result, {("conv-30", 46), ("conv-30", 47)})
        finally:
            path.unlink()

    def test_comments_and_blank_lines_skipped(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as fh:
            fh.write("# header\n\nconv-26 0\n  \nconv-26 1\n")
            path = Path(fh.name)
        try:
            result = q._load_qids_from_file(path)
            self.assertEqual(result, {("conv-26", 0), ("conv-26", 1)})
        finally:
            path.unlink()

    def test_deduplication(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as fh:
            fh.write("conv-30 5\nconv-30 5\n")
            path = Path(fh.name)
        try:
            result = q._load_qids_from_file(path)
            self.assertEqual(len(result), 1)
        finally:
            path.unlink()

    def test_bad_int_skipped(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as fh:
            fh.write("conv-30 abc\nconv-30 1\n")
            path = Path(fh.name)
        try:
            result = q._load_qids_from_file(path)
            self.assertEqual(result, {("conv-30", 1)})
        finally:
            path.unlink()


# ---------------------------------------------------------------------------
# _RetryStats
# ---------------------------------------------------------------------------

class TestRetryStats(unittest.TestCase):
    def test_initial_state(self) -> None:
        stats = q._RetryStats()
        self.assertEqual(stats.attempts, 0)
        self.assertEqual(stats.recovered, 0)
        self.assertEqual(stats.final_errors, 0)

    def test_thread_safe_increments(self) -> None:
        import threading
        stats = q._RetryStats()
        threads = [threading.Thread(target=stats.record_attempt) for _ in range(20)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        self.assertEqual(stats.attempts, 20)

    def test_summary_output(self) -> None:
        import io
        stats = q._RetryStats()
        stats.record_attempt()
        stats.record_attempt()
        stats.record_recovered()
        stats.record_final_error()
        buf = io.StringIO()
        with patch("sys.stderr", buf):
            stats.print_summary()
        out = buf.getvalue()
        self.assertIn("retries_attempted=2", out)
        self.assertIn("retries_recovered=1", out)
        self.assertIn("final_errors=1", out)
        self.assertIn("50%", out)

    def test_summary_zero_attempts(self) -> None:
        import io
        stats = q._RetryStats()
        buf = io.StringIO()
        with patch("sys.stderr", buf):
            stats.print_summary()
        out = buf.getvalue()
        self.assertIn("n/a", out)


if __name__ == "__main__":
    unittest.main()
