#!/usr/bin/env python3
"""test_query_dual.py — unit tests for M9 Stream 1 dual-speaker retrieval.

Stand-alone (no pytest required).  Mocks `requests.post` to verify:

  1. `query_search_dual` issues exactly two POSTs, one per speaker user_id.
  2. Merge dedups by memory id and keeps the higher score per id.
  3. Each merged item carries `speaker_label` provenance.
  4. `query_chat_dual` builds a system prompt containing both `[speaker:A]`
     and `[speaker:B]` blocks and posts to `/product/chat/complete`.
  5. `_parse_dual_speaker_env` correctly disables on `false`/`0` (case-insens).
  6. `main` honours `LOCOMO_DUAL_SPEAKER=false` by reverting to single-speaker
     code path (the legacy `query_search` is invoked, not `query_search_dual`).

Run:
    python3 evaluation/locomo/test_query_dual.py
"""

from __future__ import annotations

import importlib
import json
import os
import sys
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

# Make `query` importable when this file is run directly.
THIS_DIR = Path(__file__).resolve().parent
if str(THIS_DIR) not in sys.path:
    sys.path.insert(0, str(THIS_DIR))


def _fresh_query_module(env: dict | None = None):
    """Re-import `query.py` with the requested env to recompute module-level
    globals (LOCOMO_DUAL_SPEAKER, LOCOMO_RETRIEVAL_THRESHOLD)."""
    if env is None:
        env = {}
    with patch.dict(os.environ, env, clear=False):
        if "query" in sys.modules:
            del sys.modules["query"]
        return importlib.import_module("query")


def _fake_response(payload: dict) -> MagicMock:
    resp = MagicMock()
    resp.json.return_value = payload
    resp.raise_for_status.return_value = None
    return resp


def _search_payload(items: list[dict]) -> dict:
    """Wrap items in the memdb-go /product/search response envelope.

    Schema follows extract_memory_items: data.text_mem[].memories[].
    """
    return {
        "data": {
            "text_mem": [
                {
                    "memories": [
                        {
                            "id": it["id"],
                            "memory": it["content"],
                            "score": it["score"],
                        }
                        for it in items
                    ],
                }
            ],
        }
    }


class DualSpeakerEnvTests(unittest.TestCase):
    def test_default_enabled(self):
        q = _fresh_query_module({})
        self.assertTrue(q.LOCOMO_DUAL_SPEAKER)

    def test_explicit_false(self):
        for raw in ("false", "False", "FALSE", "0", "no", "off", ""):
            with self.subTest(raw=raw):
                self.assertFalse(q_helper := q_help_parser(raw))

    def test_explicit_true(self):
        for raw in ("true", "True", "1", "yes", "on", "anything-truthy"):
            with self.subTest(raw=raw):
                self.assertTrue(q_help_parser(raw))


def q_help_parser(raw: str) -> bool:
    """Local helper to invoke the parser without re-importing for every value."""
    q = _fresh_query_module({})
    return q._parse_dual_speaker_env(raw)


class QuerySearchDualTests(unittest.TestCase):
    def setUp(self):
        self.q = _fresh_query_module({})

    def test_issues_two_calls_with_correct_user_ids(self):
        """Verifies both speaker_a and speaker_b are queried."""
        items_a = [{"id": "m1", "content": "speaker A says hi", "score": 0.9}]
        items_b = [{"id": "m2", "content": "speaker B replies", "score": 0.8}]

        captured_payloads: list[dict] = []

        def fake_post(url, json=None, headers=None, timeout=None):  # noqa: A002
            captured_payloads.append(json)
            uid = json["user_id"]
            if uid.endswith("__speaker_a"):
                return _fake_response(_search_payload(items_a))
            if uid.endswith("__speaker_b"):
                return _fake_response(_search_payload(items_b))
            raise AssertionError(f"unexpected user_id: {uid}")

        with patch.object(self.q.requests, "post", side_effect=fake_post):
            sa, sb, merged, ms = self.q.query_search_dual(
                "http://memdb.test", "conv-26", "what time is it?", top_k=10
            )

        self.assertEqual(len(captured_payloads), 2)
        uids = sorted(p["user_id"] for p in captured_payloads)
        self.assertEqual(uids, ["conv-26__speaker_a", "conv-26__speaker_b"])
        self.assertEqual(len(sa), 1)
        self.assertEqual(len(sb), 1)
        self.assertEqual({m["id"] for m in merged}, {"m1", "m2"})
        self.assertIsInstance(ms, int)
        # All merged items must carry speaker_label.
        for m in merged:
            self.assertIn(m["speaker_label"], ("A", "B"))

    def test_merge_dedups_by_id_and_keeps_higher_score(self):
        """When the same memory id appears in both speakers, higher score wins."""
        items_a = [
            {"id": "shared", "content": "shared from A (low score)", "score": 0.3},
            {"id": "only-a", "content": "speaker A unique", "score": 0.7},
        ]
        items_b = [
            {"id": "shared", "content": "shared from B (high score)", "score": 0.95},
            {"id": "only-b", "content": "speaker B unique", "score": 0.6},
        ]

        def fake_post(url, json=None, headers=None, timeout=None):  # noqa: A002
            uid = json["user_id"]
            if uid.endswith("__speaker_a"):
                return _fake_response(_search_payload(items_a))
            return _fake_response(_search_payload(items_b))

        with patch.object(self.q.requests, "post", side_effect=fake_post):
            sa, sb, merged, _ = self.q.query_search_dual(
                "http://memdb.test", "conv-x", "q", top_k=10
            )

        merged_by_id = {m["id"]: m for m in merged}
        # No duplicates — three unique ids total.
        self.assertEqual(set(merged_by_id), {"shared", "only-a", "only-b"})
        # Higher score (B's copy) wins for the shared id, with B's content.
        self.assertEqual(merged_by_id["shared"]["score"], 0.95)
        self.assertIn("from B", merged_by_id["shared"]["content"])
        self.assertEqual(merged_by_id["shared"]["speaker_label"], "B")
        # Provenance preserved on speaker-unique items.
        self.assertEqual(merged_by_id["only-a"]["speaker_label"], "A")
        self.assertEqual(merged_by_id["only-b"]["speaker_label"], "B")
        # Sorted by score desc, capped at top_k.
        scores = [m["score"] for m in merged]
        self.assertEqual(scores, sorted(scores, reverse=True))

    def test_merge_respects_top_k(self):
        items_a = [
            {"id": f"a{i}", "content": f"A{i}", "score": 0.5 + i * 0.01}
            for i in range(8)
        ]
        items_b = [
            {"id": f"b{i}", "content": f"B{i}", "score": 0.4 + i * 0.01}
            for i in range(8)
        ]

        def fake_post(url, json=None, headers=None, timeout=None):  # noqa: A002
            uid = json["user_id"]
            if uid.endswith("__speaker_a"):
                return _fake_response(_search_payload(items_a))
            return _fake_response(_search_payload(items_b))

        with patch.object(self.q.requests, "post", side_effect=fake_post):
            _, _, merged, _ = self.q.query_search_dual(
                "http://memdb.test", "conv-z", "q", top_k=5
            )
        self.assertEqual(len(merged), 5)


class QueryChatDualTests(unittest.TestCase):
    def setUp(self):
        self.q = _fresh_query_module({})

    def test_chat_dual_builds_speaker_labelled_prompt(self):
        items_a = [{"id": "ma1", "content": "A loves dancing", "score": 0.9,
                    "speaker_label": "A"}]
        items_b = [{"id": "mb1", "content": "B loves coffee", "score": 0.85,
                    "speaker_label": "B"}]

        chat_payloads: list[dict] = []

        def fake_post(url, json=None, headers=None, timeout=None):  # noqa: A002
            chat_payloads.append({"url": url, "json": json})
            return _fake_response({"data": "answer-text"})

        with patch.object(self.q.requests, "post", side_effect=fake_post):
            answer, _, _, ms = self.q.query_chat_dual(
                "http://memdb.test",
                "conv-26",
                "what does each speaker like?",
                top_k=10,
                speaker_a_items=items_a,
                speaker_b_items=items_b,
            )

        self.assertEqual(answer, "answer-text")
        self.assertEqual(len(chat_payloads), 1)
        body = chat_payloads[0]["json"]
        self.assertTrue(chat_payloads[0]["url"].endswith("/product/chat/complete"))
        # System prompt must have both speaker blocks.
        sp = body["system_prompt"]
        self.assertIn("[speaker:A]", sp)
        self.assertIn("[speaker:B]", sp)
        self.assertIn("A loves dancing", sp)
        self.assertIn("B loves coffee", sp)
        # User_id deterministic to speaker_a.
        self.assertEqual(body["user_id"], "conv-26__speaker_a")
        self.assertIsInstance(ms, int)


class MainSelectorTests(unittest.TestCase):
    """Verify env-based dispatch in `main()`: dual ↔ single-speaker."""

    def setUp(self):
        self.tmp_dir = THIS_DIR / "_test_tmp"
        self.tmp_dir.mkdir(exist_ok=True)
        self.gold_path = self.tmp_dir / "tiny_gold.json"
        self.gold_path.write_text(
            json.dumps(
                [
                    {
                        "conv_id": "conv-tiny",
                        "question_idx": 0,
                        "question": "What does Caroline do?",
                        "answer": "advocacy",
                        "evidence": [],
                        "category": 1,
                    }
                ]
            )
        )
        self.out_path = self.tmp_dir / "preds.json"

    def tearDown(self):
        for p in (self.gold_path, self.out_path):
            if p.exists():
                p.unlink()
        try:
            self.tmp_dir.rmdir()
        except OSError:
            pass  # not empty (parallel test run) — ignore

    def _run_main(self, env: dict) -> tuple[int, dict]:
        q = _fresh_query_module(env)
        argv = [
            "query.py",
            "--gold", str(self.gold_path),
            "--memdb-url", "http://memdb.test",
            "--top-k", "5",
            "--out", str(self.out_path),
            "--skip-chat",
        ]
        captured: list[dict] = []

        def fake_post(url, json=None, headers=None, timeout=None):  # noqa: A002
            captured.append(json)
            items = [{"id": f"{json['user_id']}-1",
                      "content": f"mem from {json['user_id']}",
                      "score": 0.5}]
            return _fake_response(_search_payload(items))

        with patch.object(sys, "argv", argv), \
             patch.object(q.requests, "post", side_effect=fake_post):
            rc = q.main()

        with self.out_path.open() as f:
            out = json.load(f)
        return rc, out, captured

    def test_dual_speaker_default(self):
        """Default env → both speaker_a and speaker_b queried."""
        rc, out, captured = self._run_main({})
        self.assertEqual(rc, 0)
        self.assertEqual(len(captured), 2)
        uids = sorted(p["user_id"] for p in captured)
        self.assertEqual(uids, ["conv-tiny__speaker_a", "conv-tiny__speaker_b"])
        # Output schema includes dual-speaker fields.
        self.assertTrue(out["meta"]["dual_speaker"])
        rec = out["predictions"][0]
        self.assertTrue(rec["dual_speaker"])
        self.assertIn("speaker_a_memories", rec)
        self.assertIn("speaker_b_memories", rec)
        self.assertIn("merged_top_k", rec)

    def test_dual_speaker_disabled_reverts_to_single(self):
        """LOCOMO_DUAL_SPEAKER=false → only one search call per question."""
        rc, out, captured = self._run_main({"LOCOMO_DUAL_SPEAKER": "false"})
        self.assertEqual(rc, 0)
        self.assertEqual(len(captured), 1)
        # CLI default --speaker=a → user_id is conv-tiny__speaker_a.
        self.assertEqual(captured[0]["user_id"], "conv-tiny__speaker_a")
        self.assertFalse(out["meta"]["dual_speaker"])
        rec = out["predictions"][0]
        self.assertFalse(rec["dual_speaker"])
        # Backward-compat: dual-speaker-only fields are absent.
        self.assertNotIn("speaker_a_memories", rec)
        self.assertNotIn("speaker_b_memories", rec)
        self.assertNotIn("merged_top_k", rec)

    def test_dual_speaker_disabled_via_zero(self):
        rc, out, _ = self._run_main({"LOCOMO_DUAL_SPEAKER": "0"})
        self.assertEqual(rc, 0)
        self.assertFalse(out["meta"]["dual_speaker"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
