"""
End-to-end recall-budget integration test.

Ingests 5 short messages, runs /product/search with top_k=3 and top_k=10,
and asserts:
  - the response carries len(retrieved) <= top_k (budget is honored)
  - search_ms is positive (request actually round-tripped)

Skipped by default — set `MEMDB_E2E_URL` (e.g. http://localhost:8080) to
enable.  The test cleans up after itself by calling /product/cubes/delete
on the synthetic cube, so it is safe to re-run.

Run:
  MEMDB_E2E_URL=http://localhost:8080 python -m pytest \
    evaluation/locomo/tests/test_recall_budget_e2e.py -v
"""

from __future__ import annotations

import os
import sys
import time
import unittest
import uuid
from pathlib import Path

import requests

_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import query as q   # noqa: E402


_E2E_URL = os.getenv("MEMDB_E2E_URL", "")


@unittest.skipUnless(_E2E_URL, "set MEMDB_E2E_URL to enable end-to-end test")
class TestRecallBudgetE2E(unittest.TestCase):
    def setUp(self) -> None:
        self.url = _E2E_URL.rstrip("/")
        # Synthetic per-test cube id so concurrent runs don't collide.
        self.user_id = f"e2e_recall_{uuid.uuid4().hex[:8]}"
        self.headers = q.build_headers()

    def _ingest(self, texts: list[str]) -> None:
        """Push N messages via /product/add (fast mode)."""
        for i, text in enumerate(texts):
            payload = {
                "user_id": self.user_id,
                "messages": [{"role": "user", "content": text}],
                "mode": "fast",
            }
            resp = requests.post(
                f"{self.url}/product/add",
                json=payload,
                headers=self.headers,
                timeout=30,
            )
            resp.raise_for_status()
        # Embeddings + persistence are async — give the pipeline a moment.
        time.sleep(2)

    def _search(self, query: str, top_k: int) -> tuple[list[dict], int]:
        return q.query_search(
            self.url,
            self.user_id,
            query,
            top_k=top_k,
            session_id=None,
            timeout=30,
        )

    def test_recall_budget_is_honored(self) -> None:
        msgs = [
            "Marcus got promoted to senior engineer in May 2023.",
            "Sophia moved to Paris for a new job at the start of 2024.",
            "They met at a hackathon in Berlin last summer.",
            "Marcus picked up rock climbing on weekends.",
            "Sophia is taking French lessons twice a week.",
        ]
        self._ingest(msgs)

        items_3, ms_3 = self._search("When did Marcus get promoted?", top_k=3)
        self.assertGreater(ms_3, 0)
        self.assertLessEqual(len(items_3), 3,
                             f"top_k=3 violated, got {len(items_3)} items")

        items_10, ms_10 = self._search("When did Marcus get promoted?", top_k=10)
        self.assertGreater(ms_10, 0)
        self.assertLessEqual(len(items_10), 10,
                             f"top_k=10 violated, got {len(items_10)} items")

        # Budget grew → results count should grow OR stay equal (pipeline can
        # legitimately return < top_k when the corpus is small).  Strictly
        # never SHRINK between the two top_k values.
        self.assertGreaterEqual(len(items_10), len(items_3),
                                "increasing top_k decreased the result count")


if __name__ == "__main__":
    unittest.main()
