"""
Unit tests for cat-5 exclusion + dual-track reporting (M9 Stream 3).

Run:
    cd /home/krolik/src/MemDB
    python3 -m pytest evaluation/locomo/tests/test_cat5_exclusion.py -v
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

# Make score.py importable without installing the package
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from score import (  # noqa: E402
    build_aggregate_tracks,
    excl_key,
    parse_exclude_categories,
    summarize,
)


# ── helpers ──────────────────────────────────────────────────────────────────

def _make_qa(category: int, em: float = 0.5, f1: float = 0.5,
             semsim: float = 0.5, hit_at_k: float = 1.0) -> dict:
    return {
        "conv_id": "test",
        "question_idx": 0,
        "question": "q",
        "category": category,
        "gold_answer": "gold",
        "prediction": "pred",
        "em": em,
        "f1": f1,
        "semsim": semsim,
        "hit_at_k": hit_at_k,
        "error": None,
    }


# ── parse_exclude_categories ─────────────────────────────────────────────────

def test_parse_empty():
    assert parse_exclude_categories("") == frozenset()


def test_parse_single():
    assert parse_exclude_categories("5") == frozenset({5})


def test_parse_multi():
    assert parse_exclude_categories("4,5") == frozenset({4, 5})


def test_parse_whitespace():
    assert parse_exclude_categories(" 4 , 5 ") == frozenset({4, 5})


# ── excl_key ─────────────────────────────────────────────────────────────────

def test_excl_key_none():
    assert excl_key(frozenset()) == "aggregate_with_excl_none"


def test_excl_key_single():
    assert excl_key(frozenset({5})) == "aggregate_with_excl_5"


def test_excl_key_sorted():
    # sorted order: 4 before 5
    assert excl_key(frozenset({5, 4})) == "aggregate_with_excl_4_5"


# ── build_aggregate_tracks ───────────────────────────────────────────────────

def _make_per_qa() -> list[dict]:
    """10 QAs: 2 per category (1-5). cat-5 QAs have lower F1."""
    rows = []
    for cat in range(1, 6):
        f1 = 0.1 if cat == 5 else 0.5
        for _ in range(2):
            rows.append(_make_qa(cat, f1=f1, em=0.0, semsim=f1, hit_at_k=1.0))
    return rows


def test_tracks_always_has_none_and_5():
    per_qa = _make_per_qa()
    tracks = build_aggregate_tracks(per_qa, [])
    assert "aggregate_with_excl_none" in tracks
    assert "aggregate_with_excl_5" in tracks


def test_excl_none_includes_all():
    per_qa = _make_per_qa()
    tracks = build_aggregate_tracks(per_qa, [])
    assert tracks["aggregate_with_excl_none"]["n"] == 10


def test_excl_5_reduces_n():
    per_qa = _make_per_qa()
    tracks = build_aggregate_tracks(per_qa, [])
    assert tracks["aggregate_with_excl_5"]["n"] == 8  # 10 - 2 cat-5 rows


def test_excl_5_raises_f1():
    """Removing low-F1 cat-5 rows should increase aggregate F1."""
    per_qa = _make_per_qa()
    tracks = build_aggregate_tracks(per_qa, [])
    f1_all = tracks["aggregate_with_excl_none"]["f1"]
    f1_no5 = tracks["aggregate_with_excl_5"]["f1"]
    assert f1_no5 > f1_all, f"Expected f1_no5 ({f1_no5}) > f1_all ({f1_all})"


def test_extra_excl_track_emitted():
    per_qa = _make_per_qa()
    tracks = build_aggregate_tracks(per_qa, [frozenset({4})])
    assert "aggregate_with_excl_4" in tracks
    assert tracks["aggregate_with_excl_4"]["n"] == 8


def test_extra_excl_45_emitted():
    per_qa = _make_per_qa()
    tracks = build_aggregate_tracks(per_qa, [frozenset({4, 5})])
    assert "aggregate_with_excl_4_5" in tracks
    assert tracks["aggregate_with_excl_4_5"]["n"] == 6


def test_exact_f1_math():
    """Assert aggregate is a simple mean over filtered rows."""
    per_qa = [
        _make_qa(1, f1=0.8),
        _make_qa(1, f1=0.2),
        _make_qa(5, f1=0.0),  # excluded in excl_5 track
    ]
    tracks = build_aggregate_tracks(per_qa, [])
    # excl_none: (0.8 + 0.2 + 0.0) / 3
    assert abs(tracks["aggregate_with_excl_none"]["f1"] - (1.0 / 3)) < 1e-9
    # excl_5: (0.8 + 0.2) / 2
    assert abs(tracks["aggregate_with_excl_5"]["f1"] - 0.5) < 1e-9


def test_empty_after_exclusion():
    """Excluding all rows → empty aggregate (n=0, all zeros)."""
    per_qa = [_make_qa(5, f1=0.9)]
    tracks = build_aggregate_tracks(per_qa, [frozenset({5})])
    agg = tracks["aggregate_with_excl_5"]
    assert agg["n"] == 0
    assert agg["f1"] == 0.0


# ── smoke test against existing results file ─────────────────────────────────

RESULTS_DIR = Path(__file__).resolve().parents[1] / "results"
M7_SCORE = RESULTS_DIR / "m7-stage2-score.json"


@pytest.mark.skipif(not M7_SCORE.exists(), reason="m7-stage2-score.json not present")
def test_smoke_existing_score_file():
    """Re-derive excl_5 aggregate from m7-stage2-score per_qa and verify n drops."""
    with M7_SCORE.open() as fh:
        doc = json.load(fh)
    per_qa = doc.get("per_qa", [])
    if not per_qa:
        pytest.skip("no per_qa entries in score file")

    tracks = build_aggregate_tracks(per_qa, [])

    n_all = tracks["aggregate_with_excl_none"]["n"]
    n_no5 = tracks["aggregate_with_excl_5"]["n"]

    # Cat-5 rows must exist in m7 stage2 (it covers all 5 categories)
    assert n_no5 < n_all, f"Expected n_no5 ({n_no5}) < n_all ({n_all})"

    # F1 directional: removing cat-5 (adversarial) should not lower aggregate F1
    # Cat-5 in m7 stage2 has F1=0.092, below cat-1/3/4 — exclusion raises aggregate
    f1_all = tracks["aggregate_with_excl_none"]["f1"]
    f1_no5 = tracks["aggregate_with_excl_5"]["f1"]
    assert f1_no5 >= f1_all, (
        f"Expected f1_no5 ({f1_no5:.4f}) >= f1_all ({f1_all:.4f}) "
        "when removing below-average cat-5 rows"
    )
