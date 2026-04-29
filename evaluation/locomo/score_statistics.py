"""
score_statistics.py — paper-grade statistical primitives for LoCoMo eval.

Every headline number in our reports gets a 95% confidence interval (CI), every
sprint-over-sprint comparison gets a paired McNemar's chi-square test, and every
multi-judge configuration gets inter-rater agreement (Cohen's / Fleiss kappa).

Pure NumPy + SciPy + Python stdlib. No I/O, no LLM calls — all functions are
deterministic and seedable. Use ``numpy.random.default_rng(seed)`` to reproduce
bootstrap intervals exactly.

Validated against the canonical references at unit-test time:

* McNemar's chi-square with no continuity correction == ``(b-c)**2 / (b+c)``,
  p-value via :func:`scipy.stats.chi2.sf` (1 - cdf, df=1).
* Wilson score interval == standard formula in Wallis (2013) §3.
* Cohen's kappa == ``(po - pe) / (1 - pe)``.
* Fleiss kappa == Wikipedia worked example (six raters, ten subjects, five cats).

The module is intentionally compact so it ships as part of the open-source
harness deliverable (J7).
"""

from __future__ import annotations

import math
from dataclasses import asdict, dataclass, field
from typing import Iterable, Sequence

import numpy as np
from scipy.stats import binom, chi2, norm


# ---------------------------------------------------------------------------
# ScoreReport — public dataclass for headline numbers
# ---------------------------------------------------------------------------
@dataclass(frozen=True)
class ScoreReport:
    """Headline number with a 95% confidence interval and provenance.

    ``method`` distinguishes the CI construction:

    * ``"bootstrap_percentile"`` — non-parametric, used for arbitrary metrics.
    * ``"wilson_score"``         — closed-form, recommended for binary props.
    * ``"exact_binomial"``       — Clopper-Pearson, used when n is tiny (≤30).
    """

    point_estimate: float
    ci_low: float
    ci_high: float
    n: int
    method: str
    ci: float = 0.95

    def to_dict(self) -> dict:
        return asdict(self)


# ---------------------------------------------------------------------------
# Bootstrap CI — non-parametric percentile method
# ---------------------------------------------------------------------------
def bootstrap_ci(
    verdicts: Sequence[bool] | Sequence[float],
    n_resamples: int = 10_000,
    ci: float = 0.95,
    seed: int | None = 42,
) -> tuple[float, float]:
    """Percentile bootstrap of the mean.

    For ``verdicts`` of binary booleans this estimates the proportion-correct
    rate; for floats (e.g. F1 per question) it estimates the mean.

    Parameters
    ----------
    verdicts : sequence of {bool, 0/1, float}
        Per-sample observation. Coerced to float.
    n_resamples : int
        Number of bootstrap replicates. 10 000 is the default for stable
        two-decimal-place CI bounds at n ≤ 2000.
    ci : float
        Two-sided coverage, e.g. 0.95 = 95 % CI.
    seed : int | None
        RNG seed for reproducibility. ``None`` uses fresh entropy.

    Returns
    -------
    (low, high) : tuple of floats
        Percentile bounds in the same units as ``mean(verdicts)``. Empty input
        returns ``(0.0, 0.0)``; single-trial input returns ``(value, value)``.
    """
    arr = np.asarray(verdicts, dtype=float)
    n = arr.size
    if n == 0:
        return 0.0, 0.0
    if n == 1:
        v = float(arr[0])
        return v, v

    rng = np.random.default_rng(seed)
    # Vectorised resampling: (n_resamples, n) index matrix
    idx = rng.integers(0, n, size=(n_resamples, n))
    means = arr[idx].mean(axis=1)

    alpha = 1.0 - ci
    low = float(np.quantile(means, alpha / 2.0))
    high = float(np.quantile(means, 1.0 - alpha / 2.0))
    return low, high


# ---------------------------------------------------------------------------
# Wilson score interval — closed-form CI for proportions
# ---------------------------------------------------------------------------
def wilson_score_interval(k: int, n: int, ci: float = 0.95) -> tuple[float, float]:
    """Wilson (1927) score interval for a binomial proportion.

    More accurate than the normal approximation, especially for small n or
    proportions near 0/1. Falls back to ``(0.0, 0.0)`` for n=0 to avoid div-0.

    Parameters
    ----------
    k : int
        Number of successes.
    n : int
        Number of trials. Must be >= k.
    ci : float
        Two-sided coverage, default 0.95.
    """
    if n <= 0:
        return 0.0, 0.0
    if k < 0 or k > n:
        raise ValueError(f"k={k} must be in [0, n={n}]")

    alpha = 1.0 - ci
    z = float(norm.ppf(1.0 - alpha / 2.0))
    phat = k / n
    denom = 1.0 + z * z / n
    centre = (phat + z * z / (2.0 * n)) / denom
    halfw = (z * math.sqrt(phat * (1.0 - phat) / n + z * z / (4.0 * n * n))) / denom
    return max(0.0, centre - halfw), min(1.0, centre + halfw)


# ---------------------------------------------------------------------------
# Exact binomial (Clopper-Pearson) — recommended for tiny n
# ---------------------------------------------------------------------------
def exact_binomial_ci(k: int, n: int, ci: float = 0.95) -> tuple[float, float]:
    """Clopper-Pearson exact CI for a binomial proportion.

    Conservative (always covers >= ``ci``) — preferred over Wilson when ``n`` is
    small (≤30). Uses the Beta-distribution dual: the CI bounds are
    ``Beta.ppf(alpha/2, k, n-k+1)`` and ``Beta.ppf(1-alpha/2, k+1, n-k)``.
    """
    if n <= 0:
        return 0.0, 0.0
    if k < 0 or k > n:
        raise ValueError(f"k={k} must be in [0, n={n}]")
    alpha = 1.0 - ci
    from scipy.stats import beta as _beta  # local import: rare path

    low = 0.0 if k == 0 else float(_beta.ppf(alpha / 2.0, k, n - k + 1))
    high = 1.0 if k == n else float(_beta.ppf(1.0 - alpha / 2.0, k + 1, n - k))
    return low, high


# ---------------------------------------------------------------------------
# McNemar's paired chi-square — sprint-over-sprint comparisons
# ---------------------------------------------------------------------------
@dataclass(frozen=True)
class McNemarResult:
    """Paired test outcome.

    Attributes
    ----------
    chi2 : float
        Chi-square statistic, df=1. ``nan`` when b+c == 0 (perfect agreement).
    p_value : float
        Two-sided p-value. Uses chi-square asymptotic by default; the exact
        binomial form is reported in ``p_value_exact`` for n_pairs <= 25.
    p_value_exact : float | None
        Two-sided exact binomial p-value. Set only when ``b + c <= 25``.
    b : int
        Pairs where rater A says CORRECT and rater B says WRONG.
    c : int
        Pairs where rater A says WRONG and rater B says CORRECT.
    n_pairs : int
        Total pairs (== len(verdicts_a) == len(verdicts_b)).
    correction : bool
        Whether continuity correction was applied (``b - c → max(0, |b-c|-1)``).
    """

    chi2: float
    p_value: float
    b: int
    c: int
    n_pairs: int
    p_value_exact: float | None = None
    correction: bool = False

    def to_dict(self) -> dict:
        return asdict(self)


def mcnemar_test(
    verdicts_a: Sequence[bool],
    verdicts_b: Sequence[bool],
    *,
    correction: bool = False,
) -> McNemarResult:
    """Paired McNemar's test for two binary judges on the same questions.

    Both inputs must be aligned 1:1 — i.e. ``verdicts_a[i]`` and
    ``verdicts_b[i]`` are the two judges' verdicts on the *same* question.

    Returns a :class:`McNemarResult`. When ``b + c <= 25`` the exact binomial
    two-sided p-value is also computed; for larger samples it is omitted to
    keep callers from overinterpreting an asymptotic approximation that has
    already converged.

    Use ``correction=True`` for the Edwards (1948) continuity correction —
    appropriate when ``b + c < 25`` and the chi-square approximation is
    desired anyway.
    """
    if len(verdicts_a) != len(verdicts_b):
        raise ValueError(
            f"paired test needs equal lengths, got {len(verdicts_a)} vs {len(verdicts_b)}"
        )
    n_pairs = len(verdicts_a)
    if n_pairs == 0:
        return McNemarResult(chi2=float("nan"), p_value=float("nan"), b=0, c=0, n_pairs=0)

    # b: A correct (1), B wrong (0); c: A wrong (0), B correct (1)
    b = sum(1 for a, x in zip(verdicts_a, verdicts_b) if bool(a) and not bool(x))
    c = sum(1 for a, x in zip(verdicts_a, verdicts_b) if not bool(a) and bool(x))
    n_disc = b + c

    if n_disc == 0:
        # Perfect concordance: chi2 undefined, p-value = 1.0 (no evidence of difference)
        return McNemarResult(
            chi2=float("nan"),
            p_value=1.0,
            b=b,
            c=c,
            n_pairs=n_pairs,
            correction=correction,
        )

    if correction:
        chi2_stat = (max(0, abs(b - c) - 1)) ** 2 / n_disc
    else:
        chi2_stat = (b - c) ** 2 / n_disc
    p_asymp = float(chi2.sf(chi2_stat, df=1))

    p_exact: float | None = None
    if n_disc <= 25:
        # Two-sided exact binomial: under H0 each discordant pair is +1/-1 with
        # p=0.5. Test statistic min(b, c). Two-sided p = 2 * Binom.cdf(min, n, 0.5)
        # capped at 1.
        k = min(b, c)
        p_exact = float(min(1.0, 2.0 * binom.cdf(k, n_disc, 0.5)))

    return McNemarResult(
        chi2=float(chi2_stat),
        p_value=p_asymp,
        b=b,
        c=c,
        n_pairs=n_pairs,
        p_value_exact=p_exact,
        correction=correction,
    )


# ---------------------------------------------------------------------------
# Cohen's kappa — pairwise inter-rater agreement
# ---------------------------------------------------------------------------
def cohens_kappa(
    verdicts_a: Sequence[bool],
    verdicts_b: Sequence[bool],
) -> float:
    """Cohen's kappa for two raters with binary verdicts.

    Range: [-1, +1].

    * 0   = chance agreement
    * 0.41-0.60 = moderate (Landis-Koch)
    * 0.61-0.80 = substantial
    * 0.81-1.00 = almost perfect

    Edge cases:
    * Empty input  → 0.0
    * Perfect agreement with no variance (e.g. both raters say "yes" to all)
      makes ``pe == 1`` and the formula divides by zero — returns 1.0 by
      convention (raters agree perfectly even though chance would too).
    """
    if len(verdicts_a) != len(verdicts_b):
        raise ValueError(
            f"kappa needs equal lengths, got {len(verdicts_a)} vs {len(verdicts_b)}"
        )
    n = len(verdicts_a)
    if n == 0:
        return 0.0

    # Confusion table: n11 (both yes), n00 (both no), n10 (A yes / B no), n01
    n11 = sum(1 for a, x in zip(verdicts_a, verdicts_b) if bool(a) and bool(x))
    n00 = sum(1 for a, x in zip(verdicts_a, verdicts_b) if not bool(a) and not bool(x))
    n10 = sum(1 for a, x in zip(verdicts_a, verdicts_b) if bool(a) and not bool(x))
    n01 = sum(1 for a, x in zip(verdicts_a, verdicts_b) if not bool(a) and bool(x))

    po = (n11 + n00) / n
    pa1 = (n11 + n10) / n
    pa0 = 1.0 - pa1
    pb1 = (n11 + n01) / n
    pb0 = 1.0 - pb1
    pe = pa1 * pb1 + pa0 * pb0

    if abs(1.0 - pe) < 1e-12:
        # No variance — both raters constant. Return 1 if agreement is also
        # perfect (po == 1), else 0 (Cohen 1960 convention).
        return 1.0 if abs(po - 1.0) < 1e-12 else 0.0
    return (po - pe) / (1.0 - pe)


# ---------------------------------------------------------------------------
# Fleiss' kappa — multi-rater extension
# ---------------------------------------------------------------------------
def fleiss_kappa(verdict_matrix: Sequence[Sequence[bool]]) -> float:
    """Fleiss kappa for N subjects rated by M raters with K=2 categories.

    Parameters
    ----------
    verdict_matrix : list of list of bool
        Each row is one subject (question); columns are the M raters' binary
        verdicts. All rows must have the same length.

    Returns
    -------
    kappa : float
        Multi-rater agreement coefficient. Same scale as Cohen's kappa.

    Notes
    -----
    Implementation follows Fleiss (1971). For binary verdicts (K=2) this
    reduces to the two-category form: for each subject ``i`` count
    ``n_i1`` = number of raters voting "yes", ``n_i0 = M - n_i1``. Then::

        Pi = (n_i1*(n_i1-1) + n_i0*(n_i0-1)) / (M*(M-1))
        Pbar = mean(Pi)
        p1 = sum(n_i1) / (N*M);  p0 = 1 - p1
        Pe = p1**2 + p0**2
        kappa = (Pbar - Pe) / (1 - Pe)
    """
    if not verdict_matrix:
        return 0.0
    rows = [list(r) for r in verdict_matrix]
    n_subjects = len(rows)
    n_raters = len(rows[0])
    if n_raters < 2:
        raise ValueError(f"Fleiss kappa needs >= 2 raters, got {n_raters}")
    if any(len(r) != n_raters for r in rows):
        raise ValueError("Fleiss kappa requires uniform rater count per subject")

    p1_subjects: list[float] = []
    pi_sum = 0.0
    yes_total = 0
    for r in rows:
        n_yes = sum(1 for v in r if bool(v))
        n_no = n_raters - n_yes
        yes_total += n_yes
        # within-subject agreement
        pi = (n_yes * (n_yes - 1) + n_no * (n_no - 1)) / (n_raters * (n_raters - 1))
        pi_sum += pi
        p1_subjects.append(n_yes / n_raters)

    p_bar = pi_sum / n_subjects
    p1 = yes_total / (n_subjects * n_raters)
    p0 = 1.0 - p1
    pe = p1 * p1 + p0 * p0

    if abs(1.0 - pe) < 1e-12:
        return 1.0 if abs(p_bar - 1.0) < 1e-12 else 0.0
    return (p_bar - pe) / (1.0 - pe)


# ---------------------------------------------------------------------------
# Convenience: build a ScoreReport from binary verdicts
# ---------------------------------------------------------------------------
def proportion_report(
    verdicts: Sequence[bool],
    *,
    ci: float = 0.95,
    method: str = "auto",
    n_resamples: int = 10_000,
    seed: int | None = 42,
) -> ScoreReport:
    """Compute point estimate + 95% CI for a binary verdict proportion.

    ``method='auto'`` picks Wilson when n>=30, exact-binomial when n<30. Set
    explicitly for reproducibility in tables.

    ``method='bootstrap_percentile'`` is available for parity with non-binary
    metrics (F1, semsim) — gives the same answer as Wilson asymptotically.
    """
    arr = np.asarray(verdicts, dtype=bool)
    n = int(arr.size)
    if n == 0:
        return ScoreReport(0.0, 0.0, 0.0, 0, method=method if method != "auto" else "wilson_score", ci=ci)
    k = int(arr.sum())
    point = k / n

    chosen = method
    if method == "auto":
        chosen = "wilson_score" if n >= 30 else "exact_binomial"

    if chosen == "wilson_score":
        low, high = wilson_score_interval(k, n, ci=ci)
    elif chosen == "exact_binomial":
        low, high = exact_binomial_ci(k, n, ci=ci)
    elif chosen == "bootstrap_percentile":
        low, high = bootstrap_ci(arr.tolist(), n_resamples=n_resamples, ci=ci, seed=seed)
    else:
        raise ValueError(f"unknown method: {method}")

    return ScoreReport(
        point_estimate=point,
        ci_low=low,
        ci_high=high,
        n=n,
        method=chosen,
        ci=ci,
    )


def mean_report(
    values: Sequence[float],
    *,
    ci: float = 0.95,
    n_resamples: int = 10_000,
    seed: int | None = 42,
) -> ScoreReport:
    """Bootstrap CI for an arbitrary mean (use for F1, semsim, hit@k)."""
    arr = np.asarray(values, dtype=float)
    n = int(arr.size)
    if n == 0:
        return ScoreReport(0.0, 0.0, 0.0, 0, method="bootstrap_percentile", ci=ci)
    point = float(arr.mean())
    low, high = bootstrap_ci(arr.tolist(), n_resamples=n_resamples, ci=ci, seed=seed)
    return ScoreReport(
        point_estimate=point,
        ci_low=low,
        ci_high=high,
        n=n,
        method="bootstrap_percentile",
        ci=ci,
    )


__all__ = [
    "ScoreReport",
    "McNemarResult",
    "bootstrap_ci",
    "wilson_score_interval",
    "exact_binomial_ci",
    "mcnemar_test",
    "cohens_kappa",
    "fleiss_kappa",
    "proportion_report",
    "mean_report",
]
