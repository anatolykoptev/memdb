"""
score_report.py — orchestrator for paper-quality eval reports.

Consumes the per-question verdict rows produced by ``score.py`` and renders a
publication-ready JSON dict + human-readable text block. Every headline number
gets a 95% confidence interval (CI) and a kappa against gold/human labels when
available.

The output JSON is stable — downstream tooling (CI gates, leaderboard, blog
generator) parses these keys directly. Compatibility note for J1: we accept
either the legacy ``llm_score`` int (0/1) or the new ``JudgeVerdict`` dataclass
that J1 will land — see :func:`extract_verdict` for the resolution rules.
"""

from __future__ import annotations

import json
from collections import defaultdict
from dataclasses import dataclass, field
from typing import Any, Iterable, Mapping, Sequence

from score_statistics import (
    ScoreReport,
    cohens_kappa,
    fleiss_kappa,
    mcnemar_test,
    mean_report,
    proportion_report,
)


# ---------------------------------------------------------------------------
# JudgeVerdict-compatible struct (fallback if J1 hasn't merged yet)
# ---------------------------------------------------------------------------
@dataclass(frozen=True)
class _JudgeVerdictShim:
    """Minimal struct matching the J1 ``JudgeVerdict`` shape."""

    verdict: bool
    score_5pt: int | None = None
    trials: list[int] = field(default_factory=list)
    reasoning: str = ""
    judge_model: str = ""


def extract_verdict(row: Mapping[str, Any]) -> bool | None:
    """Pull a binary verdict out of a per-QA row.

    Resolution order:
      1. ``row['judge_verdict']`` — J1 dataclass (either real ``JudgeVerdict``
         or this module's shim).
      2. ``row['llm_score']`` — legacy 0/1 int.

    Returns ``None`` when no verdict is available (e.g. judge errored or
    pre-judge run).
    """
    v = row.get("judge_verdict")
    if v is not None:
        if hasattr(v, "verdict"):
            return bool(v.verdict)
        if isinstance(v, dict) and "verdict" in v:
            return bool(v["verdict"])
        # raw bool
        if isinstance(v, bool):
            return v
    if "llm_score" in row:
        try:
            return bool(int(row["llm_score"]))
        except (TypeError, ValueError):
            return None
    return None


# ---------------------------------------------------------------------------
# Category names (match score.py)
# ---------------------------------------------------------------------------
_CAT_NAMES = {
    1: "single-hop",
    2: "multi-hop",
    3: "temporal",
    4: "open-domain",
    5: "adversarial",
}


# ---------------------------------------------------------------------------
# Full report builder
# ---------------------------------------------------------------------------
def generate_full_report(
    verdicts: Sequence[Mapping[str, Any]],
    *,
    gold_labels: Sequence[bool] | None = None,
    ensemble_verdicts: Sequence[Sequence[bool]] | None = None,
    judge_config_signature: str | None = None,
    sample_metadata: Mapping[str, Any] | None = None,
    headline_excl: set[int] = frozenset({5}),
    ci: float = 0.95,
    n_resamples: int = 10_000,
    seed: int | None = 42,
) -> dict[str, Any]:
    """Build a publication-quality statistics report.

    Parameters
    ----------
    verdicts : sequence of per-QA rows
        Each row must have a ``category`` (int) and a verdict reachable via
        :func:`extract_verdict`. Optional fields used: ``conv_id``,
        ``question_idx`` (for paired tests).
    gold_labels : sequence of bool, optional
        Human gold for the same rows in the same order. Triggers Cohen's kappa
        per-cat and headline.
    ensemble_verdicts : list of lists, optional
        Outer list = N rows; inner list = M judges' verdicts. Triggers
        Fleiss kappa + ensemble disagreement rate.
    judge_config_signature : str, optional
        Stable hash of judge configuration; surfaced in the meta block.
    sample_metadata : dict, optional
        Free-form provenance (dataset version, commit SHA, retrieval mode...).

    Returns
    -------
    dict
        Stable schema:

        ``{
            "headline_excl_5": ScoreReport.to_dict() + optional 'kappa_human',
            "by_category": {"1": {...}, "2": {...}, ...},
            "ensemble": {"disagreement_rate": float, "fleiss_kappa": float} | None,
            "judge_config_signature": str | None,
            "sample_metadata": dict,
            "n_total": int,
            "n_with_verdict": int,
        }``
    """
    rows = list(verdicts)
    n_total = len(rows)

    # -- per-category bucketing ------------------------------------------------
    by_cat_rows: dict[int, list[Mapping[str, Any]]] = defaultdict(list)
    by_cat_gold: dict[int, list[bool]] = defaultdict(list)
    by_cat_verdicts: dict[int, list[bool]] = defaultdict(list)

    headline_verdicts: list[bool] = []
    headline_gold: list[bool] = []

    for i, row in enumerate(rows):
        cat = int(row.get("category") or 0)
        v = extract_verdict(row)
        if v is None:
            continue
        by_cat_rows[cat].append(row)
        by_cat_verdicts[cat].append(v)
        if gold_labels is not None and i < len(gold_labels):
            by_cat_gold[cat].append(bool(gold_labels[i]))
        if cat not in headline_excl:
            headline_verdicts.append(v)
            if gold_labels is not None and i < len(gold_labels):
                headline_gold.append(bool(gold_labels[i]))

    # -- headline (excl set) ---------------------------------------------------
    headline_report = proportion_report(
        headline_verdicts,
        ci=ci,
        method="auto",
        n_resamples=n_resamples,
        seed=seed,
    )
    headline_dict = headline_report.to_dict()
    if headline_gold and len(headline_gold) == len(headline_verdicts):
        headline_dict["kappa_human"] = cohens_kappa(headline_verdicts, headline_gold)

    # -- per-category --------------------------------------------------------
    by_category: dict[str, dict[str, Any]] = {}
    for cat, vs in sorted(by_cat_verdicts.items()):
        rep = proportion_report(vs, ci=ci, method="auto", n_resamples=n_resamples, seed=seed)
        cat_dict: dict[str, Any] = {
            "name": _CAT_NAMES.get(cat, "unknown"),
            **rep.to_dict(),
        }
        if cat in by_cat_gold and len(by_cat_gold[cat]) == len(vs):
            cat_dict["kappa_human"] = cohens_kappa(vs, by_cat_gold[cat])
        cat_dict["excluded_from_headline"] = cat in headline_excl
        by_category[str(cat)] = cat_dict

    # -- ensemble metrics ----------------------------------------------------
    ensemble_block: dict[str, Any] | None = None
    if ensemble_verdicts:
        # Disagreement rate: fraction of rows where at least one judge differs
        # from the majority.
        disagree = 0
        total = 0
        for row in ensemble_verdicts:
            if not row:
                continue
            total += 1
            n_yes = sum(1 for v in row if bool(v))
            n_no = len(row) - n_yes
            if n_yes != 0 and n_no != 0:
                disagree += 1
        disagreement_rate = (disagree / total) if total else 0.0
        ensemble_block = {
            "disagreement_rate": disagreement_rate,
            "fleiss_kappa": fleiss_kappa(ensemble_verdicts),
            "n_rows": total,
            "n_judges": len(ensemble_verdicts[0]) if ensemble_verdicts else 0,
        }

    return {
        "headline_excl_5": headline_dict,
        "by_category": by_category,
        "ensemble": ensemble_block,
        "judge_config_signature": judge_config_signature,
        "sample_metadata": dict(sample_metadata or {}),
        "n_total": n_total,
        "n_with_verdict": sum(len(v) for v in by_cat_verdicts.values()),
    }


# ---------------------------------------------------------------------------
# Sprint-over-sprint paired comparison
# ---------------------------------------------------------------------------
def _key(row: Mapping[str, Any]) -> tuple[Any, Any]:
    return (row.get("conv_id"), row.get("question_idx"))


def compare_runs(
    predictions_a: Sequence[Mapping[str, Any]],
    predictions_b: Sequence[Mapping[str, Any]],
    *,
    headline_excl: set[int] = frozenset({5}),
    label_a: str = "A",
    label_b: str = "B",
) -> dict[str, Any]:
    """Sprint-over-sprint paired comparison.

    Aligns rows by ``(conv_id, question_idx)``. Only rows present in *both*
    runs **with a non-null verdict on each side** participate in the paired
    test — silent drops are reported in the meta block (``n_unpaired_a/b``).

    Returns a dict with:

    * ``headline`` — McNemar result for the excl-5 headline
    * ``by_category`` — McNemar result per category
    * ``deltas`` — point-estimate deltas in percentage points (``b - a``)
    * ``meta`` — alignment counts + label provenance
    """
    idx_a: dict[tuple[Any, Any], Mapping[str, Any]] = {_key(r): r for r in predictions_a}
    idx_b: dict[tuple[Any, Any], Mapping[str, Any]] = {_key(r): r for r in predictions_b}

    paired_keys = sorted(set(idx_a) & set(idx_b))
    n_unpaired_a = len(idx_a) - len(paired_keys)
    n_unpaired_b = len(idx_b) - len(paired_keys)

    paired_a: list[bool] = []
    paired_b: list[bool] = []
    paired_cat: list[int] = []
    n_missing_verdict = 0

    for k in paired_keys:
        va = extract_verdict(idx_a[k])
        vb = extract_verdict(idx_b[k])
        if va is None or vb is None:
            n_missing_verdict += 1
            continue
        cat = int(idx_a[k].get("category") or idx_b[k].get("category") or 0)
        paired_a.append(va)
        paired_b.append(vb)
        paired_cat.append(cat)

    # -- headline --
    headline_a = [v for v, c in zip(paired_a, paired_cat) if c not in headline_excl]
    headline_b = [v for v, c in zip(paired_b, paired_cat) if c not in headline_excl]
    headline = mcnemar_test(headline_a, headline_b)
    delta_headline_pp = (
        100.0 * (sum(headline_b) / len(headline_b) - sum(headline_a) / len(headline_a))
        if headline_a
        else 0.0
    )

    # -- per-cat --
    by_cat: dict[str, dict[str, Any]] = {}
    cats = sorted(set(paired_cat))
    for cat in cats:
        cat_a = [v for v, c in zip(paired_a, paired_cat) if c == cat]
        cat_b = [v for v, c in zip(paired_b, paired_cat) if c == cat]
        if not cat_a:
            continue
        m = mcnemar_test(cat_a, cat_b)
        delta = 100.0 * (sum(cat_b) / len(cat_b) - sum(cat_a) / len(cat_a))
        by_cat[str(cat)] = {
            "name": _CAT_NAMES.get(cat, "unknown"),
            "delta_pp": delta,
            "mcnemar": m.to_dict(),
            "n_paired": len(cat_a),
            "rate_a": sum(cat_a) / len(cat_a),
            "rate_b": sum(cat_b) / len(cat_b),
        }

    return {
        "headline": {
            "label_a": label_a,
            "label_b": label_b,
            "delta_pp": delta_headline_pp,
            "rate_a": (sum(headline_a) / len(headline_a)) if headline_a else 0.0,
            "rate_b": (sum(headline_b) / len(headline_b)) if headline_b else 0.0,
            "mcnemar": headline.to_dict(),
            "n_paired": len(headline_a),
            "excl": sorted(headline_excl),
        },
        "by_category": by_cat,
        "meta": {
            "label_a": label_a,
            "label_b": label_b,
            "n_total_a": len(idx_a),
            "n_total_b": len(idx_b),
            "n_paired_keys": len(paired_keys),
            "n_paired_with_verdict": len(paired_a),
            "n_unpaired_a": n_unpaired_a,
            "n_unpaired_b": n_unpaired_b,
            "n_missing_verdict": n_missing_verdict,
        },
    }


# ---------------------------------------------------------------------------
# Human-readable formatter (paper-citation block)
# ---------------------------------------------------------------------------
def format_report_text(report: dict[str, Any], *, title: str = "LoCoMo Evaluation Report") -> str:
    """Render the report dict as the paper-style text block in the spec."""
    lines: list[str] = [title, "=" * len(title)]
    h = report["headline_excl_5"]
    pe = h["point_estimate"] * 100
    lo, hi = h["ci_low"] * 100, h["ci_high"] * 100
    n = h["n"]
    kappa = h.get("kappa_human")
    kappa_str = f"  κ={kappa:.2f}" if kappa is not None else ""
    lines.append(f"Headline (excl-5):  {pe:.1f}% [{lo:.1f} — {hi:.1f}]{kappa_str} (n={n})")
    lines.append("")
    lines.append("By category:")
    for cat in sorted(report["by_category"], key=lambda x: int(x) if x.isdigit() else 99):
        cd = report["by_category"][cat]
        pe = cd["point_estimate"] * 100
        lo, hi = cd["ci_low"] * 100, cd["ci_high"] * 100
        kappa = cd.get("kappa_human")
        kappa_str = f"  κ={kappa:.2f}" if kappa is not None else ""
        excl = "  (excluded from headline)" if cd.get("excluded_from_headline") else ""
        name = cd.get("name", "unknown")
        lines.append(
            f"  cat-{cat} {name:<13} {pe:5.1f}% [{lo:5.1f} — {hi:5.1f}]{kappa_str}  n={cd['n']}{excl}"
        )

    if report.get("ensemble"):
        e = report["ensemble"]
        lines.append("")
        lines.append(
            f"Ensemble disagreement rate: {e['disagreement_rate']*100:.1f}%  "
            f"(Fleiss κ={e['fleiss_kappa']:.2f}, "
            f"{e['n_judges']} judges, {e['n_rows']} rows)"
        )
    return "\n".join(lines)


def format_compare_text(
    cmp: dict[str, Any], *, title: str | None = None
) -> str:
    """Render a sprint-over-sprint compare dict as the paper-style block."""
    a = cmp["headline"]["label_a"]
    b = cmp["headline"]["label_b"]
    title = title or f"Sprint {a} → {b} (paired McNemar)"
    lines: list[str] = [title, "-" * len(title)]
    h = cmp["headline"]
    m = h["mcnemar"]
    sign = "+" if h["delta_pp"] >= 0 else ""
    lines.append(
        f"  excl-5:  {sign}{h['delta_pp']:.1f}pp  "
        f"(χ²={m['chi2']:.1f}, p={_fmt_p(m['p_value'])}, n={h['n_paired']})"
    )
    for cat in sorted(cmp["by_category"], key=lambda x: int(x) if x.isdigit() else 99):
        cd = cmp["by_category"][cat]
        m = cd["mcnemar"]
        sign = "+" if cd["delta_pp"] >= 0 else ""
        lines.append(
            f"  cat-{cat}:   {sign}{cd['delta_pp']:.1f}pp   "
            f"(χ²={m['chi2']:.1f}, p={_fmt_p(m['p_value'])}, n={cd['n_paired']})"
        )
    return "\n".join(lines)


def _fmt_p(p: float) -> str:
    if p != p:  # NaN
        return "n/a"
    if p < 0.001:
        return "<0.001"
    return f"{p:.3f}"


__all__ = [
    "extract_verdict",
    "generate_full_report",
    "compare_runs",
    "format_report_text",
    "format_compare_text",
]
