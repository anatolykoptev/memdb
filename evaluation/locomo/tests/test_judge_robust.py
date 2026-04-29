"""
test_judge_robust.py — M13 J1 robust-judge unit tests.

Coverage:
  * Reference normalization (apostrophes, Unicode NFC, contractions, punctuation)
  * Multi-trial majority vote (3-of-3, 2-of-3, tied)
  * Cache invalidation per-run + per-config signature
  * 5-point threshold mapping
  * CoT self-consistency flag
  * kappa_w_self agreement
  * Backward compat with cfg.scale="binary"

Run:
    cd /tmp/m13-j1
    python -m pytest evaluation/locomo/tests/test_judge_robust.py -v
"""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

# Make locomo directory importable when running from repo root
_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import judge_cot as cot  # noqa: E402
import judge_normalize as jn  # noqa: E402
import llm_judge as lj  # noqa: E402


# ---------------------------------------------------------------------------
# Helpers — fake the LLM HTTP layer
# ---------------------------------------------------------------------------
def _make_resp(content: str):
    """Build a fake `requests.Response` whose .json() yields chat-completions shape."""
    class FakeResp:
        def raise_for_status(self) -> None:
            return None

        def json(self) -> dict:
            return {"choices": [{"message": {"content": content}}]}

    return FakeResp()


def _cot_payload(score: int, reasoning: str = "ok") -> str:
    return json.dumps({"reasoning": reasoning, "score_5pt": score})


def _binary_payload(label: str) -> str:
    return json.dumps({"label": label, "reason": label.lower()})


def _make_cache(tmp_path: Path) -> lj.LLMJudgeCache:
    return lj.LLMJudgeCache(path=tmp_path / ".llm_judge_cache.json")


# ---------------------------------------------------------------------------
# 1. Reference normalization
# ---------------------------------------------------------------------------
class TestNormalize(unittest.TestCase):
    def test_normalize_apostrophes_smart(self) -> None:
        # Smart right single quote U+2019 vs ASCII '
        self.assertEqual(
            jn.normalize_reference("Charlotte’s Web"),
            jn.normalize_reference("Charlotte's Web"),
        )

    def test_normalize_apostrophes_strip_to_charlottes(self) -> None:
        self.assertEqual(jn.normalize_reference("Charlotte's Web"), "charlottes web")

    def test_normalize_unicode_nfc_idempotent(self) -> None:
        # 'café' as composed (é = U+00E9)  vs decomposed (e + U+0301)
        composed = "café"
        decomposed = "café"
        self.assertEqual(jn.normalize_reference(composed), jn.normalize_reference(decomposed))

    def test_normalize_contractions_dont(self) -> None:
        self.assertEqual(jn.normalize_reference("don't"), "do not")

    def test_normalize_contractions_cant(self) -> None:
        self.assertEqual(jn.normalize_reference("can't"), "cannot")

    def test_normalize_contractions_im(self) -> None:
        self.assertEqual(jn.normalize_reference("I'm"), "i am")

    def test_normalize_contractions_off_by_default_can_be_disabled(self) -> None:
        self.assertEqual(
            jn.normalize_reference("don't", expand_contractions=False),
            "dont",
        )

    def test_normalize_punctuation_stripped(self) -> None:
        self.assertEqual(jn.normalize_reference("Hello, World!"), "hello world")

    def test_normalize_whitespace_collapsed(self) -> None:
        self.assertEqual(jn.normalize_reference("a   b\tc\nd"), "a b c d")

    def test_normalize_none_returns_empty(self) -> None:
        self.assertEqual(jn.normalize_reference(None), "")

    def test_references_equal_apostrophe(self) -> None:
        self.assertTrue(jn.references_equal("Charlotte’s Web", "Charlotte's Web"))

    def test_references_equal_contraction(self) -> None:
        self.assertTrue(jn.references_equal("don't", "do not"))

    def test_normalize_idempotent(self) -> None:
        once = jn.normalize_reference("Don't go to Charlotte's Web!")
        twice = jn.normalize_reference(once)
        self.assertEqual(once, twice)


# ---------------------------------------------------------------------------
# 2. JudgeConfig.cache_signature
# ---------------------------------------------------------------------------
class TestCacheSignature(unittest.TestCase):
    def test_signature_is_16_hex_chars(self) -> None:
        sig = lj.JudgeConfig().cache_signature
        self.assertEqual(len(sig), 16)
        int(sig, 16)  # parses as hex

    def test_cache_signature_distinct_per_config_scale(self) -> None:
        c1 = lj.JudgeConfig(scale="binary")
        c2 = lj.JudgeConfig(scale="5point")
        self.assertNotEqual(c1.cache_signature, c2.cache_signature)

    def test_cache_signature_distinct_per_config_threshold(self) -> None:
        c1 = lj.JudgeConfig(threshold=3)
        c2 = lj.JudgeConfig(threshold=4)
        self.assertNotEqual(c1.cache_signature, c2.cache_signature)

    def test_cache_signature_distinct_per_config_normalize(self) -> None:
        c1 = lj.JudgeConfig(normalize=True)
        c2 = lj.JudgeConfig(normalize=False)
        self.assertNotEqual(c1.cache_signature, c2.cache_signature)

    def test_cache_signature_distinct_per_invalidation_key(self) -> None:
        c1 = lj.JudgeConfig(cache_invalidation_key="run-A")
        c2 = lj.JudgeConfig(cache_invalidation_key="run-B")
        self.assertNotEqual(c1.cache_signature, c2.cache_signature)

    def test_cache_signature_n_trials_NOT_in_signature(self) -> None:
        """Adding trials should reuse existing trial-0 / trial-1 entries."""
        c1 = lj.JudgeConfig(n_trials=1)
        c2 = lj.JudgeConfig(n_trials=5)
        self.assertEqual(c1.cache_signature, c2.cache_signature)

    def test_cache_signature_model_changes_signature(self) -> None:
        c1 = lj.JudgeConfig(model="gemini-2.5-flash")
        c2 = lj.JudgeConfig(model="claude-haiku-4-5")
        self.assertNotEqual(c1.cache_signature, c2.cache_signature)


# ---------------------------------------------------------------------------
# 3. Multi-trial majority vote
# ---------------------------------------------------------------------------
class TestMajorityVote(unittest.TestCase):
    def test_majority_3of3_correct(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(4, "perfect"))):
                v = lj.judge_prediction("q", "g", "p", lj.JudgeConfig(), cache=cache)
        self.assertTrue(v.verdict)
        self.assertEqual(v.score_5pt, 4)
        self.assertEqual(len(v.trials), 3)
        self.assertAlmostEqual(v.kappa_w_self, 1.0, places=2)

    def test_majority_3of3_wrong(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(0, "wrong"))):
                v = lj.judge_prediction("q", "g", "p", lj.JudgeConfig(), cache=cache)
        self.assertFalse(v.verdict)
        self.assertEqual(v.score_5pt, 0)
        self.assertAlmostEqual(v.kappa_w_self, 1.0, places=2)

    def test_majority_2of3_correct_majority_wins(self) -> None:
        # 2 trials say CORRECT (4), 1 says WRONG (0) → verdict=True
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            responses = [
                _make_resp(_cot_payload(4, "matches")),
                _make_resp(_cot_payload(4, "matches")),
                _make_resp(_cot_payload(0, "wrong")),
            ]
            with patch("requests.post", side_effect=responses):
                v = lj.judge_prediction(
                    "q", "g", "p", lj.JudgeConfig(cache_invalidation_key="2of3-c"),
                    cache=cache,
                )
        self.assertTrue(v.verdict)
        self.assertEqual(v.score_5pt, 4)
        self.assertLess(v.kappa_w_self, 1.0)  # disagreement → kappa < 1

    def test_majority_2of3_wrong_majority_wins(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            responses = [
                _make_resp(_cot_payload(0, "wrong")),
                _make_resp(_cot_payload(0, "wrong")),
                _make_resp(_cot_payload(4, "matches")),
            ]
            with patch("requests.post", side_effect=responses):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(cache_invalidation_key="2of3-w"),
                    cache=cache,
                )
        self.assertFalse(v.verdict)
        self.assertLess(v.kappa_w_self, 1.0)

    def test_majority_split_tied_falls_to_lower_score(self) -> None:
        # 4 trials, 2-2 split → tie-break to WRONG (lower side)
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            responses = [
                _make_resp(_cot_payload(4, "ok")),
                _make_resp(_cot_payload(0, "no")),
                _make_resp(_cot_payload(4, "ok")),
                _make_resp(_cot_payload(0, "no")),
            ]
            with patch("requests.post", side_effect=responses):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(n_trials=4, cache_invalidation_key="tied"),
                    cache=cache,
                )
        self.assertFalse(v.verdict)  # tie → WRONG (conservative)


# ---------------------------------------------------------------------------
# 4. Cache invalidation
# ---------------------------------------------------------------------------
class TestCacheInvalidation(unittest.TestCase):
    def test_cache_invalidation_key_busts_cache(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            # First run with key A — populate cache
            with patch("requests.post", return_value=_make_resp(_cot_payload(4))) as p1:
                lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(n_trials=1, cache_invalidation_key="run-A"),
                    cache=cache,
                )
            self.assertEqual(p1.call_count, 1)
            # Second run with same key A — should be cached, no new HTTP call
            with patch("requests.post") as p2:
                lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(n_trials=1, cache_invalidation_key="run-A"),
                    cache=cache,
                )
            p2.assert_not_called()
            # Third run with key B — different cache key, MUST hit HTTP again
            with patch("requests.post", return_value=_make_resp(_cot_payload(0))) as p3:
                lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(n_trials=1, cache_invalidation_key="run-B"),
                    cache=cache,
                )
            self.assertEqual(p3.call_count, 1)

    def test_cache_signature_distinct_keys(self) -> None:
        c1 = lj.JudgeConfig(scale="binary", normalize=False)
        c2 = lj.JudgeConfig(scale="5point", normalize=True)
        k1 = lj._v2_cache_key("q", "g", "p", c1, 0)
        k2 = lj._v2_cache_key("q", "g", "p", c2, 0)
        self.assertNotEqual(k1, k2)

    def test_cache_key_per_trial_distinct(self) -> None:
        c = lj.JudgeConfig()
        k0 = lj._v2_cache_key("q", "g", "p", c, 0)
        k1 = lj._v2_cache_key("q", "g", "p", c, 1)
        self.assertNotEqual(k0, k1)

    def test_v1_legacy_cache_path_preserved(self) -> None:
        """The legacy `judge()` function must still hash with the old key shape."""
        legacy_key = lj._cache_key("q", "g", "p")
        self.assertEqual(len(legacy_key), 64)
        # And it must be different from the v2 key (which includes config sig)
        v2_key = lj._v2_cache_key("q", "g", "p", lj.JudgeConfig(), 0)
        self.assertNotEqual(legacy_key, v2_key)


# ---------------------------------------------------------------------------
# 5. 5-point threshold semantics
# ---------------------------------------------------------------------------
class TestThreshold(unittest.TestCase):
    def test_5point_threshold_3_score_3_maps_correct(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(3))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(threshold=3, cache_invalidation_key="thr3-3"),
                    cache=cache,
                )
        self.assertTrue(v.verdict)

    def test_5point_threshold_3_score_2_maps_wrong(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(2))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(threshold=3, cache_invalidation_key="thr3-2"),
                    cache=cache,
                )
        self.assertFalse(v.verdict)

    def test_5point_threshold_4_strict_score_3_maps_wrong(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(3))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(threshold=4, cache_invalidation_key="thr4-3"),
                    cache=cache,
                )
        self.assertFalse(v.verdict)

    def test_5point_threshold_4_strict_score_4_maps_correct(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(4))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(threshold=4, cache_invalidation_key="thr4-4"),
                    cache=cache,
                )
        self.assertTrue(v.verdict)


# ---------------------------------------------------------------------------
# 6. CoT parser + self-consistency
# ---------------------------------------------------------------------------
class TestCoTParser(unittest.TestCase):
    def test_parse_cot_clean_json(self) -> None:
        out = cot.parse_cot_response(json.dumps({"reasoning": "good", "score_5pt": 4}))
        self.assertEqual(out["score_5pt"], 4)
        self.assertEqual(out["reasoning"], "good")
        self.assertIsNone(out["parse_error"])

    def test_parse_cot_invalid_score_clamped_with_error(self) -> None:
        out = cot.parse_cot_response(json.dumps({"reasoning": "x", "score_5pt": 9}))
        self.assertEqual(out["score_5pt"], 0)
        self.assertIsNotNone(out["parse_error"])

    def test_parse_cot_missing_score(self) -> None:
        out = cot.parse_cot_response(json.dumps({"reasoning": "x"}))
        self.assertIsNotNone(out["parse_error"])

    def test_parse_cot_garbage_returns_error(self) -> None:
        out = cot.parse_cot_response("not json at all {")
        self.assertIsNotNone(out["parse_error"])

    def test_parse_cot_handles_markdown_fence(self) -> None:
        fenced = "```json\n" + json.dumps({"reasoning": "ok", "score_5pt": 3}) + "\n```"
        out = cot.parse_cot_response(fenced)
        self.assertEqual(out["score_5pt"], 3)
        self.assertIsNone(out["parse_error"])

    def test_cot_self_consistency_flag_score_high_reasoning_negative(self) -> None:
        # score=4 says CORRECT, reasoning says "answer is incorrect" → inconsistent
        self.assertFalse(cot.is_self_consistent("the answer is incorrect", 4))

    def test_cot_self_consistency_flag_score_low_reasoning_positive(self) -> None:
        # score=0 says WRONG, reasoning says "fully correct" → inconsistent
        self.assertFalse(cot.is_self_consistent("this is fully correct", 0))

    def test_cot_self_consistency_normal_case(self) -> None:
        self.assertTrue(cot.is_self_consistent("matches the gold answer well", 4))
        self.assertTrue(cot.is_self_consistent("wrong number", 0))

    def test_cot_self_consistency_partial_score_never_flagged(self) -> None:
        # Score=2 (partial) is ambiguous — accept any reasoning
        self.assertTrue(cot.is_self_consistent("the answer is incorrect partially", 2))
        self.assertTrue(cot.is_self_consistent("fully correct", 2))


# ---------------------------------------------------------------------------
# 7. kappa_w_self agreement
# ---------------------------------------------------------------------------
class TestKappaWSelf(unittest.TestCase):
    def test_kappa_w_self_perfect_agreement(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(4, "matches gold"))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(cache_invalidation_key="kappa-perfect"),
                    cache=cache,
                )
        self.assertAlmostEqual(v.kappa_w_self, 1.0, places=2)

    def test_kappa_w_self_disagreement_drops_below_one(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            responses = [
                _make_resp(_cot_payload(4, "matches gold")),
                _make_resp(_cot_payload(4, "matches gold")),
                _make_resp(_cot_payload(0, "wrong number")),
            ]
            with patch("requests.post", side_effect=responses):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(cache_invalidation_key="kappa-2of3"),
                    cache=cache,
                )
        self.assertLess(v.kappa_w_self, 1.0)
        self.assertGreater(v.kappa_w_self, 0.0)

    def test_kappa_w_self_inconsistent_trial_drops_kappa(self) -> None:
        # All 3 score=4, but one has contradictory reasoning → inconsistent
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            responses = [
                _make_resp(_cot_payload(4, "fully correct")),
                _make_resp(_cot_payload(4, "fully correct")),
                _make_resp(_cot_payload(4, "the answer is incorrect")),
            ]
            with patch("requests.post", side_effect=responses):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(cache_invalidation_key="kappa-inconsistent"),
                    cache=cache,
                )
        self.assertTrue(v.verdict)  # all 3 agreed numerically → CORRECT
        self.assertLess(v.kappa_w_self, 1.0)  # but one was self-inconsistent


# ---------------------------------------------------------------------------
# 8. Backward compat: cfg.scale="binary"
# ---------------------------------------------------------------------------
class TestBackwardCompat(unittest.TestCase):
    def test_backward_compat_binary_mode_correct(self) -> None:
        # Per API spec, score_5pt = -1 in binary scale (5-point not exposed)
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_binary_payload("CORRECT"))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(scale="binary", n_trials=1, cache_invalidation_key="bc-c"),
                    cache=cache,
                )
        self.assertTrue(v.verdict)
        self.assertEqual(v.score_5pt, -1)

    def test_backward_compat_binary_mode_wrong(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_binary_payload("WRONG"))):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(scale="binary", n_trials=1, cache_invalidation_key="bc-w"),
                    cache=cache,
                )
        self.assertFalse(v.verdict)
        self.assertEqual(v.score_5pt, -1)

    def test_backward_compat_legacy_judge_function_unchanged(self) -> None:
        """Legacy `judge()` callers (score.py without --judge-config) must still work."""
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_binary_payload("CORRECT"))):
                result = lj.judge("q", "g", "p", cache=cache)
        # v1 contract: returns {"score": 0|1, "reason": str}
        self.assertIn("score", result)
        self.assertIn("reason", result)
        self.assertEqual(result["score"], 1)

    def test_backward_compat_v1_cache_files_readable(self) -> None:
        """Pre-M13 cache files (no config signature in keys) must still parse."""
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / ".llm_judge_cache.json"
            # Simulate a pre-M13 cache file
            legacy_key = lj._cache_key("q", "g", "p")
            path.write_text(json.dumps({legacy_key: {"score": 1, "reason": "matches"}}))
            cache = lj.LLMJudgeCache(path=path)
            # Legacy judge() should hit cache, not HTTP
            with patch("requests.post") as mock_post:
                result = lj.judge("q", "g", "p", cache=cache)
            mock_post.assert_not_called()
            self.assertEqual(result["score"], 1)


# ---------------------------------------------------------------------------
# 9. Batch API
# ---------------------------------------------------------------------------
class TestBatch(unittest.TestCase):
    def test_batch_processes_all_inputs(self) -> None:
        preds = [
            {"question": "q1", "gold_answer": "g1", "prediction": "p1"},
            {"question": "q2", "gold_answer": "g2", "prediction": "p2"},
            {"question": "q3", "gold_answer": "g3", "prediction": "p3"},
        ]
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(4))):
                verdicts = lj.judge_predictions_batch(
                    preds, lj.JudgeConfig(n_trials=1, cache_invalidation_key="batch-1"),
                    workers=2, cache=cache,
                )
        self.assertEqual(len(verdicts), 3)
        for v in verdicts:
            self.assertTrue(v.verdict)

    def test_batch_handles_alt_field_names(self) -> None:
        preds = [
            {"question": "q", "gold": "g", "pred": "p"},
            {"question": "q2", "gold_answer": "g2", "chat_answer": "p2"},
        ]
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", return_value=_make_resp(_cot_payload(4))):
                verdicts = lj.judge_predictions_batch(
                    preds, lj.JudgeConfig(n_trials=1, cache_invalidation_key="batch-alt"),
                    workers=2, cache=cache,
                )
        self.assertEqual(len(verdicts), 2)


# ---------------------------------------------------------------------------
# 10. Network failure handling
# ---------------------------------------------------------------------------
class TestNetworkFailures(unittest.TestCase):
    def test_network_failure_returns_wrong_verdict(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache = _make_cache(Path(tmp))
            with patch("requests.post", side_effect=ConnectionError("down")):
                v = lj.judge_prediction(
                    "q", "g", "p",
                    lj.JudgeConfig(n_trials=1, cache_invalidation_key="net-fail"),
                    cache=cache,
                )
        # All trials fail → score=0 → WRONG
        self.assertFalse(v.verdict)
        self.assertIsNotNone(v.trials[0].get("parse_error"))


if __name__ == "__main__":
    unittest.main(verbosity=2)
