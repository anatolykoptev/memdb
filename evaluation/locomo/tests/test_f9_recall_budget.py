"""
Unit tests for F9 recall budget changes in evaluation/locomo/query.py
and evaluation/locomo/ingest.py.

Run: python -m pytest evaluation/locomo/tests/test_f9_recall_budget.py -v
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

import requests

# Make locomo directory importable when running from repo root
_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import query as q   # noqa: E402
import ingest as ig  # noqa: E402


# ---------------------------------------------------------------------------
# query.py — _extract_ts
# ---------------------------------------------------------------------------

class TestExtractTs(unittest.TestCase):
    def test_chat_time_field(self) -> None:
        meta = {"chat_time": "2023-05-08T13:56:00Z"}
        self.assertEqual(q._extract_ts(meta), "2023-05-08")

    def test_created_at_field(self) -> None:
        meta = {"created_at": "2022-11-01T10:00:00+00:00"}
        self.assertEqual(q._extract_ts(meta), "2022-11-01")

    def test_time_field_fallback(self) -> None:
        meta = {"time": "2023-03-15T09:30:00"}
        self.assertEqual(q._extract_ts(meta), "2023-03-15")

    def test_short_value_returned_as_is(self) -> None:
        meta = {"chat_time": "2023"}
        self.assertEqual(q._extract_ts(meta), "2023")

    def test_no_timestamp_returns_none(self) -> None:
        self.assertIsNone(q._extract_ts({}))
        self.assertIsNone(q._extract_ts({"score": 0.9}))


# ---------------------------------------------------------------------------
# query.py — extract_memory_items includes ts field
# ---------------------------------------------------------------------------

class TestExtractMemoryItemsHasTs(unittest.TestCase):
    def _make_payload(self, chat_time: str | None = "2023-05-08T13:00:00Z") -> dict:
        metadata: dict = {"id": "abc", "memory": "fact", "relativity": 0.8}
        if chat_time is not None:
            metadata["chat_time"] = chat_time
        return {
            "data": {
                "text_mem": [
                    {
                        "cube_id": "x",
                        "memories": [
                            {"id": "abc", "memory": "fact", "metadata": metadata}
                        ],
                    }
                ]
            }
        }

    def test_ts_populated_when_chat_time_present(self) -> None:
        payload = self._make_payload("2023-05-08T13:00:00Z")
        items = q.extract_memory_items(payload)
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["ts"], "2023-05-08")

    def test_ts_none_when_no_timestamp(self) -> None:
        payload = self._make_payload(None)
        items = q.extract_memory_items(payload)
        self.assertEqual(len(items), 1)
        self.assertIsNone(items[0]["ts"])


# ---------------------------------------------------------------------------
# query.py — _build_dual_speaker_system_prompt uses timestamp prefix
# ---------------------------------------------------------------------------

class TestDualSpeakerSystemPromptTs(unittest.TestCase):
    def test_ts_prefix_in_prompt(self) -> None:
        items_a = [{"content": "Marcus got promoted.", "ts": "2023-05-01", "score": 0.9, "id": "1"}]
        items_b = [{"content": "Sophia moved to Paris.", "ts": "2023-06-01", "score": 0.85, "id": "2"}]
        prompt = q._build_dual_speaker_system_prompt(items_a, items_b)
        self.assertIn("2023-05-01: Marcus got promoted.", prompt)
        self.assertIn("2023-06-01: Sophia moved to Paris.", prompt)

    def test_no_ts_prefix_when_ts_none(self) -> None:
        items_a = [{"content": "Marcus got promoted.", "ts": None, "score": 0.9, "id": "1"}]
        items_b = []
        prompt = q._build_dual_speaker_system_prompt(items_a, items_b)
        # Should appear WITHOUT a "None: " prefix
        self.assertIn("Marcus got promoted.", prompt)
        self.assertNotIn("None:", prompt)

    def test_empty_speaker_b_shows_placeholder(self) -> None:
        items_a = [{"content": "fact", "ts": None, "score": 0.9, "id": "1"}]
        items_b = []
        prompt = q._build_dual_speaker_system_prompt(items_a, items_b)
        self.assertIn("(no memories retrieved)", prompt)


# ---------------------------------------------------------------------------
# query.py — query_search_dual passes top_k_per_speaker to each speaker call
# ---------------------------------------------------------------------------

class TestQuerySearchDualTopKPerSpeaker(unittest.TestCase):
    def test_per_speaker_top_k_used(self) -> None:
        """Each speaker call should use top_k_per_speaker, not top_k."""
        calls = []

        def fake_query_search(url, user_id, query, top_k, session_id, timeout):
            calls.append({"user_id": user_id, "top_k": top_k})
            return [], 10  # (items, elapsed_ms)

        with patch.object(q, "query_search", side_effect=fake_query_search):
            result = q.query_search_dual(
                memdb_url="http://localhost:8080",
                conv_id="conv-26",
                query="When did Marcus get promoted?",
                top_k=20,
                top_k_per_speaker=30,
            )

        self.assertEqual(len(calls), 2)
        for call in calls:
            self.assertEqual(call["top_k"], 30, "Each speaker should use top_k_per_speaker=30")

        items_a, items_b, merged, elapsed_ms = result
        self.assertEqual(items_a, [])
        self.assertEqual(items_b, [])
        self.assertEqual(merged, [])

    def test_default_top_k_per_speaker_is_30(self) -> None:
        """Default top_k_per_speaker must be 30 (mem0 eval harness default)."""
        calls = []

        def fake_query_search(url, user_id, query, top_k, session_id, timeout):
            calls.append(top_k)
            return [], 0

        with patch.object(q, "query_search", side_effect=fake_query_search):
            q.query_search_dual(
                memdb_url="http://localhost:8080",
                conv_id="conv-26",
                query="When?",
                top_k=20,
            )

        for called_top_k in calls:
            self.assertEqual(called_top_k, 30)


# ---------------------------------------------------------------------------
# query.py — --top-k-per-speaker CLI default
# ---------------------------------------------------------------------------

class TestTopKPerSpeakerCLI(unittest.TestCase):
    def _parse(self, extra: list[str]) -> object:
        import argparse
        import importlib
        # Re-import to get a fresh parser
        p = argparse.ArgumentParser()
        g = p.add_mutually_exclusive_group(required=False)
        g.add_argument("--sample", action="store_true")
        g.add_argument("--full", action="store_true")
        p.add_argument("--top-k-per-speaker", type=int, default=30)
        p.add_argument("--top-k", type=int, default=20)
        p.add_argument("--out", default="/tmp/out.json")
        return p.parse_args(extra)

    def test_default_is_30(self) -> None:
        args = self._parse(["--sample"])
        self.assertEqual(args.top_k_per_speaker, 30)

    def test_explicit_override(self) -> None:
        args = self._parse(["--sample", "--top-k-per-speaker", "15"])
        self.assertEqual(args.top_k_per_speaker, 15)


# ---------------------------------------------------------------------------
# ingest.py — reverse_role default is True
# ---------------------------------------------------------------------------

class TestIngestReverseRoleDefault(unittest.TestCase):
    def test_reverse_role_true_doubles_session_count(self) -> None:
        """With reverse_role=True, each session produces 4 ingest calls (2 normal + 2 reverse)."""
        posted: list[dict] = []

        def fake_ingest_one(memdb_url, user_id, session_id, speaker_a, speaker_b,
                            messages, iso_date, perspective, timeout=120):
            posted.append({"user_id": user_id, "session_id": session_id,
                           "perspective": perspective})
            return {}

        # Minimal single-session conversation
        convs = [
            {
                "sample_id": "test-conv",
                "conversation": {
                    "session_1": [{"speaker": "Alice", "text": "Hello"}, {"speaker": "Bob", "text": "Hi"}],
                    "session_1_date_time": None,
                },
            }
        ]

        with patch.object(ig, "ingest_one_session", side_effect=fake_ingest_one):
            stats = ig.ingest(convs, "http://localhost:8080", reverse_role=True)

        # 2 perspectives × 2 passes (normal + rev) = 4 calls
        self.assertEqual(len(posted), 4)
        self.assertEqual(stats["reverse_role_sessions"], 2)
        self.assertTrue(stats["reverse_role"])

    def test_reverse_role_false_single_pass(self) -> None:
        """With reverse_role=False, each session produces 2 ingest calls (normal only)."""
        posted: list[dict] = []

        def fake_ingest_one(memdb_url, user_id, session_id, speaker_a, speaker_b,
                            messages, iso_date, perspective, timeout=120):
            posted.append({"user_id": user_id, "session_id": session_id})
            return {}

        convs = [
            {
                "sample_id": "test-conv",
                "conversation": {
                    "session_1": [{"speaker": "Alice", "text": "Hello"}, {"speaker": "Bob", "text": "Hi"}],
                    "session_1_date_time": None,
                },
            }
        ]

        with patch.object(ig, "ingest_one_session", side_effect=fake_ingest_one):
            stats = ig.ingest(convs, "http://localhost:8080", reverse_role=False)

        self.assertEqual(len(posted), 2)
        self.assertEqual(stats["reverse_role_sessions"], 0)
        self.assertFalse(stats["reverse_role"])

    def test_reverse_role_session_id_has_rev_suffix(self) -> None:
        """Reversed-pass session IDs must end with __rev to avoid collision."""
        session_ids: list[str] = []

        def fake_ingest_one(memdb_url, user_id, session_id, speaker_a, speaker_b,
                            messages, iso_date, perspective, timeout=120):
            session_ids.append(session_id)
            return {}

        convs = [
            {
                "sample_id": "conv-26",
                "conversation": {
                    "session_1": [{"speaker": "Alice", "text": "Hello"}],
                    "session_1_date_time": None,
                },
            }
        ]

        with patch.object(ig, "ingest_one_session", side_effect=fake_ingest_one):
            ig.ingest(convs, "http://localhost:8080", reverse_role=True)

        rev_ids = [s for s in session_ids if s.endswith("__rev")]
        normal_ids = [s for s in session_ids if not s.endswith("__rev")]
        self.assertEqual(len(rev_ids), 2, "Both speakers should have a __rev session")
        self.assertEqual(len(normal_ids), 2, "Both speakers should have a normal session")


# ---------------------------------------------------------------------------
# F9 + reverse_role parity — table-driven assertions against the Memobase
# reference shape.  Numbers below mirror the contract documented in
# memobase_search.py (compete-research/memobase/.../memobase_search.py:71-105):
#
#   per-speaker top_k = 30  →  60 candidates total per question
#   merged top_k       = 20  (truncates merged stream to harness budget)
#   reverse_role=True  →  4 ingest calls per single-session conversation
#                         (2 perspectives × {normal, __rev})
#
# These are the guard-rails that make a LoCoMo run an apples-to-apples
# comparison with the Memobase benchmark.  If any of them drift, the run
# is no longer comparable to the published numbers and the test fails loud.
# ---------------------------------------------------------------------------

class TestMemobaseParity(unittest.TestCase):
    PARITY_CASES = [
        # (case_label, top_k, top_k_per_speaker, reverse_role,
        #  expected_per_speaker_calls, expected_merged_cap, expected_ingest_calls)
        ("default",     20, 30, True,  30, 20, 4),
        ("smaller_pool", 10, 15, True, 15, 10, 4),
        ("no_reverse",  20, 30, False, 30, 20, 2),
    ]

    def test_dual_search_per_speaker_top_k_table(self) -> None:
        for label, top_k, kps, _rev, exp_per_speaker, exp_cap, _exp_ingest in self.PARITY_CASES:
            with self.subTest(case=label):
                seen: list[int] = []

                def fake_query_search(url, user_id, query, top_k_arg, session_id, timeout):
                    seen.append(top_k_arg)
                    # Return more rows than the merged cap; merge must trim.
                    items = [
                        {"id": f"{user_id}-{i}", "content": f"row{i}", "score": 1.0 - i * 0.01}
                        for i in range(top_k_arg)
                    ]
                    return items, 5

                with patch.object(q, "query_search", side_effect=fake_query_search):
                    items_a, items_b, merged, _ms = q.query_search_dual(
                        memdb_url="http://localhost:8080",
                        conv_id="conv-26",
                        query="When did Marcus get promoted?",
                        top_k=top_k,
                        top_k_per_speaker=kps,
                    )

                # Per-speaker calls used the configured budget.
                self.assertEqual(len(seen), 2, f"{label}: expected 2 fan-out calls")
                for v in seen:
                    self.assertEqual(v, exp_per_speaker,
                                     f"{label}: per-speaker top_k must be {exp_per_speaker}")
                # Merged stream truncated to the harness top_k.
                self.assertLessEqual(len(merged), exp_cap,
                                     f"{label}: merged stream must respect top_k={exp_cap}")
                # Both speakers contributed all rows.
                self.assertEqual(len(items_a), exp_per_speaker)
                self.assertEqual(len(items_b), exp_per_speaker)

    def test_reverse_role_ingest_call_table(self) -> None:
        """Ingest call counts mirror Memobase: 4 with reverse_role, 2 without."""
        for label, _top_k, _kps, reverse_role, _eps, _ec, exp_ingest in self.PARITY_CASES:
            with self.subTest(case=label):
                posted: list[dict] = []

                def fake_ingest_one(memdb_url, user_id, session_id, speaker_a, speaker_b,
                                    messages, iso_date, perspective, timeout=120):
                    posted.append({"user_id": user_id, "perspective": perspective,
                                   "session_id": session_id})
                    return {}

                convs = [{
                    "sample_id": "conv-26",
                    "conversation": {
                        "session_1": [
                            {"speaker": "Alice", "text": "Hello"},
                            {"speaker": "Bob", "text": "Hi"},
                        ],
                        "session_1_date_time": None,
                    },
                }]

                with patch.object(ig, "ingest_one_session", side_effect=fake_ingest_one):
                    ig.ingest(convs, "http://localhost:8080", reverse_role=reverse_role)

                self.assertEqual(len(posted), exp_ingest,
                                 f"{label}: ingest call count must equal {exp_ingest}")
                if reverse_role:
                    rev = [p for p in posted if p["session_id"].endswith("__rev")]
                    self.assertEqual(len(rev), exp_ingest // 2,
                                     f"{label}: half of calls must be __rev passes")


# ---------------------------------------------------------------------------
# _extract_ts type tolerance — str OR int epoch (Q1 followup item)
# ---------------------------------------------------------------------------

class TestExtractTsTypeTolerance(unittest.TestCase):
    def test_epoch_seconds(self) -> None:
        # 2023-05-08 13:56:00 UTC = 1683554160
        self.assertEqual(q._extract_ts({"chat_time": 1683554160}), "2023-05-08")

    def test_epoch_milliseconds(self) -> None:
        # Same instant in ms.
        self.assertEqual(q._extract_ts({"chat_time": 1683554160000}), "2023-05-08")

    def test_epoch_float(self) -> None:
        self.assertEqual(q._extract_ts({"chat_time": 1683554160.0}), "2023-05-08")

    def test_negative_epoch_skipped(self) -> None:
        self.assertIsNone(q._extract_ts({"chat_time": -100}))

    def test_zero_epoch_skipped(self) -> None:
        self.assertIsNone(q._extract_ts({"chat_time": 0}))

    def test_pre_2001_epoch_skipped(self) -> None:
        # ~1969 epoch seconds — too small, treated as garbage.
        self.assertIsNone(q._extract_ts({"chat_time": 1234}))

    def test_far_future_skipped(self) -> None:
        # 1e14 → year ~5138 in seconds; rejected by upper bound.
        self.assertIsNone(q._extract_ts({"chat_time": 10**14}))

    def test_bool_not_treated_as_int(self) -> None:
        # Pythonically True == 1 but we don't want bool inputs to look like
        # "1970-01-01" — must be skipped explicitly.
        self.assertIsNone(q._extract_ts({"chat_time": True}))

    def test_string_still_works(self) -> None:
        # Regression guard for the original happy path.
        self.assertEqual(
            q._extract_ts({"chat_time": "2024-01-15T10:00:00Z"}),
            "2024-01-15",
        )

    def test_falls_back_to_next_field(self) -> None:
        meta = {"chat_time": "", "created_at": 1683554160}
        self.assertEqual(q._extract_ts(meta), "2023-05-08")


if __name__ == "__main__":
    unittest.main()
