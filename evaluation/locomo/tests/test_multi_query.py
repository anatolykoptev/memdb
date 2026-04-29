"""
test_multi_query.py — unit tests for M13 J3-Q multi-query expansion.

Run: python -m pytest evaluation/locomo/tests/test_multi_query.py -v
  or: python -m unittest evaluation.locomo.tests.test_multi_query -v

Covered:
  TestSplitterParsing      JSON shapes, fences, empty list, malformed.
  TestSplitterFallback     Network/timeout failures degrade to [question].
  TestSplitterCache        Cache hit returns identical variants.
  TestUnionDedupe          Same id → keep highest score; cap at max_size.
  TestMultiQueryRetrieve   Stubbed splitter+retriever; verifies pipeline.
  TestReassemblerVerdict   5pt → binary mapping; matched/missing parsing.
  TestReassemblerFailure   judge_error path returns score=0.
  TestReassemblerCacheKey  Cache key incorporates union ids; reorder-stable.
  TestEndToEndCat1         Synthetic Melanie cat-1 case end-to-end.
"""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

# Make locomo dir importable
_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import judge_multi_query_reassembler as jmqr  # noqa: E402
import judge_multi_query_retrieval as jmqr_ret  # noqa: E402
import judge_query_splitter as jqs  # noqa: E402


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _llm_resp(content: str) -> dict:
    """Shape an OpenAI-compatible chat completion response."""
    return {"choices": [{"message": {"content": content}}]}


def _ok_post(content: str) -> MagicMock:
    """Build a mock response.post(...) result."""
    resp = MagicMock()
    resp.raise_for_status = MagicMock()
    resp.json.return_value = _llm_resp(content)
    return resp


# ---------------------------------------------------------------------------
# Splitter — JSON parsing
# ---------------------------------------------------------------------------
class TestSplitterParsing(unittest.TestCase):
    def test_plain_json(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cfg = jqs.SplitterConfig(
                use_cache=False,
                cache_path=Path(tmp) / "_no.json",
            )
            with patch("judge_query_splitter.requests.post") as mp:
                mp.return_value = _ok_post(
                    '{"variants": ["A", "B", "C"]}'
                )
                out = jqs.split_query("What hobbies does X have?", cfg)
        self.assertEqual(out, ["A", "B", "C"])

    def test_markdown_fence(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post(
                '```json\n{"variants": ["X", "Y", "Z"]}\n```'
            )
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("q", cfg)
        self.assertEqual(out, ["X", "Y", "Z"])

    def test_inline_json_after_prose(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post(
                'Here are the variants:\n{"variants": ["P", "Q", "R"]}'
            )
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("q", cfg)
        self.assertEqual(out, ["P", "Q", "R"])

    def test_dedupe_preserves_order(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post(
                '{"variants": ["alpha", "ALPHA", "beta", "alpha", "gamma"]}'
            )
            cfg = jqs.SplitterConfig(use_cache=False, n_variants=4)
            out = jqs.split_query("q", cfg)
        # ALPHA dedupes against alpha (case-insensitive)
        self.assertEqual(out, ["alpha", "beta", "gamma"])

    def test_truncates_to_n_variants(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post(
                '{"variants": ["v1", "v2", "v3", "v4", "v5"]}'
            )
            cfg = jqs.SplitterConfig(use_cache=False, n_variants=3)
            out = jqs.split_query("q", cfg)
        self.assertEqual(len(out), 3)

    def test_long_variant_capped(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            long_v = "x" * 500
            mp.return_value = _ok_post(
                json.dumps({"variants": [long_v, "short", "ok"]})
            )
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("q", cfg)
        self.assertLessEqual(len(out[0]), 200)
        self.assertEqual(out[1], "short")


# ---------------------------------------------------------------------------
# Splitter — fallback paths
# ---------------------------------------------------------------------------
class TestSplitterFallback(unittest.TestCase):
    def test_malformed_json_falls_back_to_single(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post("totally not json")
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("original q", cfg)
        self.assertEqual(out, ["original q"])

    def test_missing_variants_key_falls_back(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post('{"results": ["a"]}')
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("orig", cfg)
        self.assertEqual(out, ["orig"])

    def test_empty_variants_list_falls_back(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post('{"variants": []}')
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("orig", cfg)
        self.assertEqual(out, ["orig"])

    def test_network_failure_falls_back(self) -> None:
        with patch("judge_query_splitter.requests.post") as mp:
            mp.side_effect = ConnectionError("boom")
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("orig", cfg)
        self.assertEqual(out, ["orig"])

    def test_empty_question_returns_empty_list(self) -> None:
        cfg = jqs.SplitterConfig(use_cache=False)
        out = jqs.split_query("", cfg)
        self.assertEqual(out, [""])

    def test_all_blank_strings_falls_back(self) -> None:
        """LLM returns variants but they're all empty/whitespace."""
        with patch("judge_query_splitter.requests.post") as mp:
            mp.return_value = _ok_post('{"variants": ["", "   ", ""]}')
            cfg = jqs.SplitterConfig(use_cache=False)
            out = jqs.split_query("orig", cfg)
        self.assertEqual(out, ["orig"])


# ---------------------------------------------------------------------------
# Splitter — cache reuse
# ---------------------------------------------------------------------------
class TestSplitterCache(unittest.TestCase):
    def test_cache_hit_skips_http(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = Path(tmp) / "splitter_cache.json"
            cfg = jqs.SplitterConfig(use_cache=True, cache_path=cache_path)
            # Force a fresh shared cache pointing at our tmp file
            jqs._shared_cache = None  # type: ignore[attr-defined]
            with patch("judge_query_splitter.requests.post") as mp:
                mp.return_value = _ok_post(
                    '{"variants": ["A", "B", "C"]}'
                )
                first = jqs.split_query("question", cfg)
                # Flush so disk state is real
                jqs.flush_shared_cache()
                second = jqs.split_query("question", cfg)
            self.assertEqual(first, second)
            # Only ONE network call — the second was served from cache.
            self.assertEqual(mp.call_count, 1)


# ---------------------------------------------------------------------------
# Union dedupe
# ---------------------------------------------------------------------------
class TestUnionDedupe(unittest.TestCase):
    def test_same_id_keeps_highest_score(self) -> None:
        a = [{"id": "x", "content": "lo", "score": 0.5}]
        b = [{"id": "x", "content": "hi", "score": 0.9}]
        merged, before = jmqr_ret._dedupe_by_id([a, b], 100)
        self.assertEqual(len(merged), 1)
        self.assertEqual(before, 1)
        self.assertEqual(merged[0]["score"], 0.9)
        self.assertEqual(merged[0]["content"], "hi")

    def test_different_ids_unioned(self) -> None:
        a = [{"id": "x", "content": "x", "score": 0.5}]
        b = [{"id": "y", "content": "y", "score": 0.6}]
        c = [{"id": "z", "content": "z", "score": 0.4}]
        merged, before = jmqr_ret._dedupe_by_id([a, b, c], 100)
        self.assertEqual(before, 3)
        ids = sorted(m["id"] for m in merged)
        self.assertEqual(ids, ["x", "y", "z"])
        # Sorted by score desc
        self.assertEqual(merged[0]["id"], "y")

    def test_max_size_truncates(self) -> None:
        items = [
            {"id": f"id{i}", "content": f"c{i}", "score": float(i)}
            for i in range(10)
        ]
        merged, before = jmqr_ret._dedupe_by_id([items], 5)
        self.assertEqual(before, 10)
        self.assertEqual(len(merged), 5)
        # Top 5 by score
        self.assertEqual([m["id"] for m in merged], [f"id{i}" for i in [9, 8, 7, 6, 5]])

    def test_missing_id_falls_back_to_content_hash(self) -> None:
        a = [{"id": "", "content": "same content", "score": 0.5}]
        b = [{"id": "", "content": "same content", "score": 0.7}]
        merged, before = jmqr_ret._dedupe_by_id([a, b], 100)
        # Same content hash → deduped
        self.assertEqual(before, 1)
        self.assertEqual(len(merged), 1)
        self.assertEqual(merged[0]["score"], 0.7)

    def test_empty_lists(self) -> None:
        merged, before = jmqr_ret._dedupe_by_id([], 10)
        self.assertEqual(merged, [])
        self.assertEqual(before, 0)
        merged, before = jmqr_ret._dedupe_by_id([[], []], 10)
        self.assertEqual(merged, [])

    def test_corrupt_rows_dropped(self) -> None:
        """Non-dict and empty content rows must not crash."""
        a = [
            "not a dict",  # type: ignore[list-item]
            {"id": "", "content": "", "score": 0.5},
            {"id": "ok", "content": "real", "score": 0.5},
        ]
        merged, _ = jmqr_ret._dedupe_by_id([a], 10)  # type: ignore[arg-type]
        self.assertEqual(len(merged), 1)
        self.assertEqual(merged[0]["id"], "ok")


# ---------------------------------------------------------------------------
# Multi-query retrieve — full pipeline with stubs
# ---------------------------------------------------------------------------
class TestMultiQueryRetrieve(unittest.TestCase):
    def test_three_subq_yields_union(self) -> None:
        # Simulate: splitter returns 3 variants; each retrieval returns a
        # disjoint top-5 list with a small overlap.
        def fake_splitter(q: str, cfg: jqs.SplitterConfig) -> list[str]:
            return ["hobbies", "outdoor activities", "sports"]

        # Variant ↦ list of memories
        per_q_map = {
            "What activities does Melanie do?": [
                {"id": "m1", "content": "pottery", "score": 0.7},
                {"id": "m2", "content": "painting", "score": 0.6},
            ],
            "hobbies": [
                {"id": "m1", "content": "pottery", "score": 0.8},  # higher
                {"id": "m3", "content": "sketching", "score": 0.5},
            ],
            "outdoor activities": [
                {"id": "m4", "content": "camping", "score": 0.7},
                {"id": "m5", "content": "swimming", "score": 0.6},
            ],
            "sports": [
                {"id": "m5", "content": "swimming", "score": 0.65},
                {"id": "m6", "content": "running", "score": 0.5},
            ],
        }

        def fake_retriever(cfg: jmqr_ret.MultiQueryConfig, sq: str) -> list[dict]:
            return list(per_q_map.get(sq, []))

        cfg = jmqr_ret.MultiQueryConfig(
            user_id="user-x",
            top_k=20,
            include_original=True,
            parallel=False,  # deterministic ordering for assertions
        )
        out = jmqr_ret.multi_query_retrieve(
            "What activities does Melanie do?",
            cfg,
            splitter_fn=fake_splitter,
            retriever_fn=fake_retriever,
        )
        # 1 original + 3 variants = 4 sub-queries
        self.assertEqual(len(out["sub_queries"]), 4)
        self.assertIn("What activities does Melanie do?", out["sub_queries"])
        # Union covers all 6 unique ids — m1 dedupe keeps higher score (0.8)
        union_ids = sorted(m["id"] for m in out["union"])
        self.assertEqual(union_ids, ["m1", "m2", "m3", "m4", "m5", "m6"])
        m1 = next(m for m in out["union"] if m["id"] == "m1")
        self.assertEqual(m1["score"], 0.8)
        # Diagnostics present
        self.assertEqual(out["union_size_after_cap"], 6)
        self.assertEqual(out["errors"], [])

    def test_retriever_failure_partial(self) -> None:
        """One failing variant doesn't sink the whole question."""
        def fake_splitter(q: str, cfg: jqs.SplitterConfig) -> list[str]:
            return ["good", "bad", "good2"]

        def fake_retriever(cfg: jmqr_ret.MultiQueryConfig, sq: str) -> list[dict]:
            if sq == "bad":
                raise ConnectionError("transient")
            return [{"id": f"id-{sq}", "content": sq, "score": 0.5}]

        cfg = jmqr_ret.MultiQueryConfig(
            user_id="u",
            include_original=False,
            parallel=False,
        )
        out = jmqr_ret.multi_query_retrieve(
            "Q",
            cfg,
            splitter_fn=fake_splitter,
            retriever_fn=fake_retriever,
        )
        self.assertEqual(len(out["errors"]), 1)
        # Union still contains the 2 good variants' results
        self.assertEqual(out["union_size_after_cap"], 2)

    def test_no_user_id_no_conv_id_raises(self) -> None:
        cfg = jmqr_ret.MultiQueryConfig()
        with self.assertRaises(ValueError):
            jmqr_ret._retrieve_one(cfg, "q")

    def test_max_union_size_respected(self) -> None:
        def fake_splitter(q: str, cfg: jqs.SplitterConfig) -> list[str]:
            return ["v1", "v2"]

        def fake_retriever(cfg: jmqr_ret.MultiQueryConfig, sq: str) -> list[dict]:
            # 50 unique items per variant → 100 total before dedupe
            return [
                {"id": f"{sq}-{i}", "content": str(i), "score": float(i)}
                for i in range(50)
            ]

        cfg = jmqr_ret.MultiQueryConfig(
            user_id="u",
            max_union_size=10,
            include_original=False,
            parallel=False,
        )
        out = jmqr_ret.multi_query_retrieve(
            "q", cfg, splitter_fn=fake_splitter, retriever_fn=fake_retriever
        )
        self.assertEqual(out["union_size_before_cap"], 100)
        self.assertEqual(out["union_size_after_cap"], 10)
        # Top-10 by score
        top_scores = [m["score"] for m in out["union"]]
        self.assertEqual(top_scores, sorted(top_scores, reverse=True))


# ---------------------------------------------------------------------------
# Reassembler — verdict parsing
# ---------------------------------------------------------------------------
class TestReassemblerVerdict(unittest.TestCase):
    def test_score_5pt_4_maps_to_binary_1(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({
                "reasoning": "all four facts present",
                "score_5pt": 4,
                "matched_facts": ["pottery", "camping", "painting", "swimming"],
                "missing_facts": [],
            }))
            v = jmqr.reassemble_verdict(
                question="q",
                gold="gold",
                sub_queries=["a", "b"],
                union_memories=[{"id": "m1", "content": "x"}],
                use_cache=False,
            )
        self.assertEqual(v.score_5pt, 4)
        self.assertEqual(v.score, 1)
        self.assertEqual(len(v.matched_facts), 4)

    def test_score_5pt_2_maps_to_binary_0(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({
                "reasoning": "half present",
                "score_5pt": 2,
                "matched_facts": ["a"],
                "missing_facts": ["b", "c"],
            }))
            v = jmqr.reassemble_verdict(
                question="q",
                gold="gold",
                sub_queries=[],
                union_memories=[],
                use_cache=False,
            )
        self.assertEqual(v.score_5pt, 2)
        self.assertEqual(v.score, 0)

    def test_score_5pt_3_at_threshold(self) -> None:
        """3 == default threshold → binary 1."""
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({
                "reasoning": "most facts present",
                "score_5pt": 3,
            }))
            v = jmqr.reassemble_verdict(
                question="q", gold="gold", sub_queries=[],
                union_memories=[], use_cache=False,
            )
        self.assertEqual(v.score, 1)

    def test_higher_threshold(self) -> None:
        """Caller can require strict 4/4 by passing binary_threshold=4."""
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({"score_5pt": 3}))
            v = jmqr.reassemble_verdict(
                question="q", gold="gold", sub_queries=[],
                union_memories=[], use_cache=False, binary_threshold=4,
            )
        self.assertEqual(v.score, 0)

    def test_score_5pt_clamped_to_range(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({"score_5pt": 99}))
            v = jmqr.reassemble_verdict(
                question="q", gold="gold", sub_queries=[],
                union_memories=[], use_cache=False,
            )
        self.assertEqual(v.score_5pt, 4)

    def test_stringy_int_tolerated(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({"score_5pt": "3"}))
            v = jmqr.reassemble_verdict(
                question="q", gold="gold", sub_queries=[],
                union_memories=[], use_cache=False,
            )
        self.assertEqual(v.score_5pt, 3)


# ---------------------------------------------------------------------------
# Reassembler — failure modes
# ---------------------------------------------------------------------------
class TestReassemblerFailure(unittest.TestCase):
    def test_network_error_returns_judge_error(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.side_effect = ConnectionError("boom")
            v = jmqr.reassemble_verdict(
                question="q", gold="g", sub_queries=[],
                union_memories=[], use_cache=False,
            )
        self.assertEqual(v.score, 0)
        self.assertEqual(v.score_5pt, 0)
        self.assertTrue(v.reason.startswith("judge_error:"))

    def test_malformed_json_returns_judge_error(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post("not json at all")
            v = jmqr.reassemble_verdict(
                question="q", gold="g", sub_queries=[],
                union_memories=[], use_cache=False,
            )
        self.assertEqual(v.score, 0)
        self.assertTrue(v.reason.startswith("judge_error:"))

    def test_missing_score_field_returns_judge_error(self) -> None:
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({"reasoning": "no score"}))
            v = jmqr.reassemble_verdict(
                question="q", gold="g", sub_queries=[],
                union_memories=[], use_cache=False,
            )
        self.assertEqual(v.score, 0)
        self.assertTrue(v.reason.startswith("judge_error:"))


# ---------------------------------------------------------------------------
# Reassembler — caching
# ---------------------------------------------------------------------------
class TestReassemblerCacheKey(unittest.TestCase):
    def test_reorder_union_same_cache_key(self) -> None:
        """Cache must be insensitive to union memory ordering."""
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = Path(tmp) / "rc.json"
            jmqr._shared_cache = None  # type: ignore[attr-defined]
            with patch("judge_multi_query_reassembler.requests.post") as mp:
                mp.return_value = _ok_post(json.dumps({"score_5pt": 4}))
                v1 = jmqr.reassemble_verdict(
                    question="q", gold="g", sub_queries=["a", "b"],
                    union_memories=[
                        {"id": "x", "content": "1"},
                        {"id": "y", "content": "2"},
                    ],
                    use_cache=True, cache_path=cache_path,
                )
                jmqr.flush_shared_cache()
                # Same content, reversed order — must hit cache
                v2 = jmqr.reassemble_verdict(
                    question="q", gold="g", sub_queries=["a", "b"],
                    union_memories=[
                        {"id": "y", "content": "2"},
                        {"id": "x", "content": "1"},
                    ],
                    use_cache=True, cache_path=cache_path,
                )
            self.assertEqual(v1.score_5pt, v2.score_5pt)
            self.assertEqual(mp.call_count, 1, "second call must come from cache")

    def test_different_subq_busts_cache(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = Path(tmp) / "rc.json"
            jmqr._shared_cache = None  # type: ignore[attr-defined]
            with patch("judge_multi_query_reassembler.requests.post") as mp:
                mp.return_value = _ok_post(json.dumps({"score_5pt": 2}))
                jmqr.reassemble_verdict(
                    question="q", gold="g", sub_queries=["a"],
                    union_memories=[{"id": "x"}],
                    use_cache=True, cache_path=cache_path,
                )
                jmqr.flush_shared_cache()
                jmqr.reassemble_verdict(
                    question="q", gold="g", sub_queries=["b"],  # different
                    union_memories=[{"id": "x"}],
                    use_cache=True, cache_path=cache_path,
                )
            # Two distinct cache keys → two HTTP calls
            self.assertEqual(mp.call_count, 2)


# ---------------------------------------------------------------------------
# End-to-end synthetic cat-1 multi-fact case
# ---------------------------------------------------------------------------
class TestEndToEndCat1(unittest.TestCase):
    def test_melanie_activities_full_pipeline(self) -> None:
        """The headline cat-1 case from the M13 spec.

        Single-query retrieval misses 'camping' and 'swimming' because the
        cosine cluster around "activities" pulls hobby-ish words first.
        Multi-query expansion paraphrases into hobby/outdoor/sports
        sub-queries and the union covers all 4 gold facts → score_5pt=4
        → binary CORRECT.  This test stubs LLM + retrieval, so it runs
        offline as part of CI.
        """
        gold = "pottery, camping, painting, swimming"
        question = "What activities does Melanie partake in?"

        # Stage 1: stub splitter
        def fake_splitter(q: str, cfg: jqs.SplitterConfig) -> list[str]:
            return [
                "What hobbies does Melanie do?",
                "What outdoor activities does Melanie enjoy?",
                "What sports does Melanie play?",
            ]

        # Stage 2: stub retrieval — original misses camping+swimming,
        # variants fill them in.
        per_q_map = {
            question: [
                {"id": "p1", "content": "Melanie does pottery", "score": 0.7},
                {"id": "p2", "content": "Melanie loves painting", "score": 0.65},
                {"id": "p7", "content": "Melanie tries hiking", "score": 0.55},
            ],
            "What hobbies does Melanie do?": [
                {"id": "p1", "content": "Melanie does pottery", "score": 0.8},
                {"id": "p2", "content": "Melanie loves painting", "score": 0.78},
                {"id": "p3", "content": "Melanie sketches sometimes", "score": 0.5},
            ],
            "What outdoor activities does Melanie enjoy?": [
                {"id": "p4", "content": "Melanie went camping last summer", "score": 0.72},
                {"id": "p5", "content": "Melanie hiked in Yosemite", "score": 0.6},
                {"id": "p6", "content": "Melanie swims at the pool", "score": 0.55},
            ],
            "What sports does Melanie play?": [
                {"id": "p6", "content": "Melanie swims at the pool", "score": 0.7},
                {"id": "p8", "content": "Melanie does charity races", "score": 0.5},
            ],
        }

        def fake_retriever(cfg: jmqr_ret.MultiQueryConfig, sq: str) -> list[dict]:
            return list(per_q_map.get(sq, []))

        cfg = jmqr_ret.MultiQueryConfig(
            user_id="conv-26__speaker_a",
            top_k=20,
            include_original=True,
            parallel=False,
        )
        mq_out = jmqr_ret.multi_query_retrieve(
            question, cfg,
            splitter_fn=fake_splitter,
            retriever_fn=fake_retriever,
        )
        # Union must contain all 4 gold facts (pottery, camping, painting, swimming)
        union_text = " ".join(m["content"].lower() for m in mq_out["union"])
        for fact in ("pottery", "camping", "painting", "swims"):
            self.assertIn(fact, union_text, f"{fact!r} missing from union")

        # Stage 3: stub reassembler LLM → returns 4/4
        with patch("judge_multi_query_reassembler.requests.post") as mp:
            mp.return_value = _ok_post(json.dumps({
                "reasoning": "All four gold facts present in union memories.",
                "score_5pt": 4,
                "matched_facts": ["pottery", "camping", "painting", "swimming"],
                "missing_facts": [],
            }))
            v = jmqr.reassemble_verdict(
                question=question,
                gold=gold,
                sub_queries=mq_out["sub_queries"],
                union_memories=mq_out["union"],
                use_cache=False,
            )
        self.assertEqual(v.score_5pt, 4)
        self.assertEqual(v.score, 1)  # binary CORRECT (vs single-query partial)
        self.assertEqual(len(v.matched_facts), 4)


if __name__ == "__main__":
    unittest.main()
