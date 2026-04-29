"""Unit tests for evaluation/locomo/score_statistics.py

Covers:
    * bootstrap_ci coverage on a known distribution
    * Wilson score / exact binomial parity with scipy reference
    * McNemar's chi-square parity vs the closed-form formula + scipy.chi2.sf
    * Cohen's kappa: 0 on independent random raters, 1 on perfect agreement,
      Wikipedia worked example (= 0.4)
    * Fleiss kappa: Wikipedia six-rater binary worked example
    * Edge cases: empty input, single-trial, all-same verdicts
    * proportion_report / mean_report wrappers + auto method selection

Run:  python -m pytest evaluation/locomo/tests/test_score_statistics.py -v
"""

from __future__ import annotations

import math
import sys
import unittest
from pathlib import Path

import numpy as np
from scipy.stats import beta, binom, chi2, norm

_LOCOMO_DIR = Path(__file__).resolve().parent.parent
if str(_LOCOMO_DIR) not in sys.path:
    sys.path.insert(0, str(_LOCOMO_DIR))

import score_statistics as ss  # noqa: E402


# ---------------------------------------------------------------------------
# Bootstrap CI
# ---------------------------------------------------------------------------
class TestBootstrapCI(unittest.TestCase):
    def test_empty_input(self):
        self.assertEqual(ss.bootstrap_ci([]), (0.0, 0.0))

    def test_single_trial(self):
        low, high = ss.bootstrap_ci([True])
        self.assertEqual((low, high), (1.0, 1.0))
        low, high = ss.bootstrap_ci([False])
        self.assertEqual((low, high), (0.0, 0.0))

    def test_all_same_verdicts(self):
        # All True → CI degenerates to (1, 1)
        low, high = ss.bootstrap_ci([True] * 100, n_resamples=2000)
        self.assertEqual(low, 1.0)
        self.assertEqual(high, 1.0)
        low, high = ss.bootstrap_ci([False] * 100, n_resamples=2000)
        self.assertEqual(low, 0.0)
        self.assertEqual(high, 0.0)

    def test_seeded_reproducibility(self):
        v = [True] * 30 + [False] * 70
        a1 = ss.bootstrap_ci(v, n_resamples=5000, seed=123)
        a2 = ss.bootstrap_ci(v, n_resamples=5000, seed=123)
        self.assertEqual(a1, a2)

    def test_centred_on_point_estimate(self):
        # 100 trials with k=40: bootstrap mean ~ 0.40, CI brackets it.
        v = [True] * 40 + [False] * 60
        low, high = ss.bootstrap_ci(v, n_resamples=10_000, seed=42)
        self.assertLess(low, 0.40)
        self.assertGreater(high, 0.40)
        # Width should be ~ 2 * z * sqrt(p*(1-p)/n) = 2 * 1.96 * sqrt(0.24/100)
        expected_halfwidth = 1.96 * math.sqrt(0.4 * 0.6 / 100)
        observed_halfwidth = (high - low) / 2.0
        self.assertAlmostEqual(observed_halfwidth, expected_halfwidth, delta=0.025)

    def test_converges_to_normal_approx(self):
        # Large-n: bootstrap CI for proportion should ~ match normal approx CI.
        rng = np.random.default_rng(1)
        verdicts = rng.random(2000) < 0.6
        low, high = ss.bootstrap_ci(verdicts.tolist(), n_resamples=10_000, seed=42)
        phat = float(np.mean(verdicts))
        z = 1.959963984540054
        se = math.sqrt(phat * (1 - phat) / 2000)
        self.assertAlmostEqual(low, phat - z * se, delta=0.01)
        self.assertAlmostEqual(high, phat + z * se, delta=0.01)


# ---------------------------------------------------------------------------
# Wilson score interval
# ---------------------------------------------------------------------------
class TestWilsonScore(unittest.TestCase):
    def _wilson_ref(self, k: int, n: int, alpha: float = 0.05):
        z = norm.ppf(1 - alpha / 2)
        phat = k / n
        denom = 1 + z * z / n
        centre = (phat + z * z / (2 * n)) / denom
        halfw = (z * math.sqrt(phat * (1 - phat) / n + z * z / (4 * n * n))) / denom
        return centre - halfw, centre + halfw

    def test_matches_scipy_reference(self):
        for k, n in [(50, 100), (1, 10), (9, 10), (500, 1000), (0, 5), (5, 5)]:
            with self.subTest(k=k, n=n):
                exp_low, exp_high = self._wilson_ref(k, n)
                obs_low, obs_high = ss.wilson_score_interval(k, n)
                self.assertAlmostEqual(obs_low, max(0.0, exp_low), delta=1e-9)
                self.assertAlmostEqual(obs_high, min(1.0, exp_high), delta=1e-9)

    def test_zero_n(self):
        self.assertEqual(ss.wilson_score_interval(0, 0), (0.0, 0.0))

    def test_invalid_k(self):
        with self.assertRaises(ValueError):
            ss.wilson_score_interval(11, 10)
        with self.assertRaises(ValueError):
            ss.wilson_score_interval(-1, 10)

    def test_clamps_to_unit_interval(self):
        low, high = ss.wilson_score_interval(0, 5)
        self.assertGreaterEqual(low, 0.0)
        low, high = ss.wilson_score_interval(5, 5)
        self.assertLessEqual(high, 1.0)


# ---------------------------------------------------------------------------
# Exact binomial CI
# ---------------------------------------------------------------------------
class TestExactBinomial(unittest.TestCase):
    def test_matches_beta_dual(self):
        for k, n in [(5, 10), (3, 30), (0, 10), (10, 10)]:
            with self.subTest(k=k, n=n):
                obs_low, obs_high = ss.exact_binomial_ci(k, n)
                exp_low = 0.0 if k == 0 else float(beta.ppf(0.025, k, n - k + 1))
                exp_high = 1.0 if k == n else float(beta.ppf(0.975, k + 1, n - k))
                self.assertAlmostEqual(obs_low, exp_low, delta=1e-9)
                self.assertAlmostEqual(obs_high, exp_high, delta=1e-9)

    def test_invalid_inputs(self):
        with self.assertRaises(ValueError):
            ss.exact_binomial_ci(11, 10)


# ---------------------------------------------------------------------------
# McNemar's test — scipy parity at 1e-6
# ---------------------------------------------------------------------------
class TestMcNemar(unittest.TestCase):
    def test_basic_chi2_parity(self):
        # Toy: 100 paired questions; b=10 (a yes / b no), c=30 (a no / b yes)
        # A=correct: 60 (50 both + 10 b-only=wait); we need actual lists.
        # Build: 50 both correct, 10 a-only, 30 b-only, 10 both wrong.
        a = [True] * 50 + [True] * 10 + [False] * 30 + [False] * 10
        b = [True] * 50 + [False] * 10 + [True] * 30 + [False] * 10
        m = ss.mcnemar_test(a, b)
        self.assertEqual(m.b, 10)
        self.assertEqual(m.c, 30)
        self.assertEqual(m.n_pairs, 100)
        # Reference: (10-30)^2 / (10+30) = 400/40 = 10.0; df=1; p = chi2.sf(10, 1)
        self.assertAlmostEqual(m.chi2, 10.0, delta=1e-9)
        self.assertAlmostEqual(m.p_value, float(chi2.sf(10.0, 1)), delta=1e-9)

    def test_continuity_correction(self):
        a = [True] * 50 + [True] * 10 + [False] * 30 + [False] * 10
        b = [True] * 50 + [False] * 10 + [True] * 30 + [False] * 10
        m = ss.mcnemar_test(a, b, correction=True)
        # (|10-30| - 1)^2 / 40 = 19*19/40 = 9.025
        self.assertAlmostEqual(m.chi2, 9.025, delta=1e-9)
        self.assertAlmostEqual(m.p_value, float(chi2.sf(9.025, 1)), delta=1e-9)

    def test_exact_p_value_for_small_disc(self):
        # b=3, c=7  → n_disc=10 → exact path triggers
        a = [True] * 3 + [False] * 7 + [True] * 5  # 5 both correct extra
        b = [False] * 3 + [True] * 7 + [True] * 5
        m = ss.mcnemar_test(a, b)
        self.assertEqual(m.b, 3)
        self.assertEqual(m.c, 7)
        self.assertIsNotNone(m.p_value_exact)
        # exact two-sided: 2 * Binom.cdf(min(b,c)=3, n=10, p=0.5)
        ref = float(min(1.0, 2.0 * binom.cdf(3, 10, 0.5)))
        self.assertAlmostEqual(m.p_value_exact, ref, delta=1e-9)

    def test_perfect_agreement(self):
        a = [True] * 10 + [False] * 10
        m = ss.mcnemar_test(a, list(a))
        self.assertEqual(m.b, 0)
        self.assertEqual(m.c, 0)
        self.assertTrue(math.isnan(m.chi2))
        self.assertEqual(m.p_value, 1.0)

    def test_empty_input(self):
        m = ss.mcnemar_test([], [])
        self.assertEqual(m.n_pairs, 0)
        self.assertTrue(math.isnan(m.p_value))

    def test_mismatched_lengths(self):
        with self.assertRaises(ValueError):
            ss.mcnemar_test([True], [True, False])

    def test_no_discordant_large_sample(self):
        # All concordant — but mixed correctness — chi2 undefined, p=1
        a = [True, False, True, False] * 50
        m = ss.mcnemar_test(a, list(a))
        self.assertEqual(m.b, 0)
        self.assertEqual(m.c, 0)
        self.assertEqual(m.p_value, 1.0)


# ---------------------------------------------------------------------------
# Cohen's kappa
# ---------------------------------------------------------------------------
class TestCohensKappa(unittest.TestCase):
    def test_perfect_agreement_returns_one(self):
        a = [True, False, True, False, True]
        self.assertEqual(ss.cohens_kappa(a, list(a)), 1.0)

    def test_perfect_disagreement(self):
        # When both raters use both labels but always disagree, kappa is < 0.
        a = [True, True, False, False]
        b = [False, False, True, True]
        # n11=0, n00=0, n10=2, n01=2; po=0; pa1=0.5, pb1=0.5; pe=0.5; kappa=(0-0.5)/0.5=-1
        self.assertAlmostEqual(ss.cohens_kappa(a, b), -1.0, delta=1e-9)

    def test_wikipedia_worked_example(self):
        # From Wikipedia: 50 subjects, n11=15, n10=5, n01=10, n00=20 → kappa=0.4
        a = [True] * 15 + [True] * 5 + [False] * 10 + [False] * 20
        b = [True] * 15 + [False] * 5 + [True] * 10 + [False] * 20
        self.assertAlmostEqual(ss.cohens_kappa(a, b), 0.4, delta=1e-9)

    def test_independent_random_raters_near_zero(self):
        rng = np.random.default_rng(7)
        a = (rng.random(5000) < 0.4).tolist()
        b = (rng.random(5000) < 0.4).tolist()
        kappa = ss.cohens_kappa(a, b)
        self.assertAlmostEqual(kappa, 0.0, delta=0.05)

    def test_empty_input(self):
        self.assertEqual(ss.cohens_kappa([], []), 0.0)

    def test_mismatched_lengths(self):
        with self.assertRaises(ValueError):
            ss.cohens_kappa([True], [True, False])

    def test_constant_rater_perfect_agreement(self):
        # Both raters always say True — pe=1, formula divides by zero.
        # By convention, return 1.0 since they agree perfectly.
        a = [True] * 50
        b = [True] * 50
        self.assertEqual(ss.cohens_kappa(a, b), 1.0)


# ---------------------------------------------------------------------------
# Fleiss kappa
# ---------------------------------------------------------------------------
class TestFleissKappa(unittest.TestCase):
    def test_wikipedia_binary_perfect(self):
        # All 5 raters agree on every subject — kappa = 1
        matrix = [[True] * 5 for _ in range(10)]
        self.assertEqual(ss.fleiss_kappa(matrix), 1.0)

    def test_random_raters_near_zero(self):
        rng = np.random.default_rng(9)
        matrix = [[bool(b) for b in rng.integers(0, 2, 5)] for _ in range(2000)]
        kappa = ss.fleiss_kappa(matrix)
        self.assertAlmostEqual(kappa, 0.0, delta=0.04)

    def test_partial_agreement_matches_manual(self):
        # 3 subjects, 4 raters; manual computation:
        #   subject 1: 4 yes 0 no   → P1 = (4*3 + 0)/12 = 1.0
        #   subject 2: 3 yes 1 no   → P2 = (3*2 + 1*0)/12 = 6/12 = 0.5
        #   subject 3: 1 yes 3 no   → P3 = (1*0 + 3*2)/12 = 6/12 = 0.5
        # Pbar = (1.0 + 0.5 + 0.5)/3 = 2/3
        # yes_total = 4+3+1 = 8; total cells = 3*4=12 → p1 = 8/12 = 2/3
        # Pe = (2/3)^2 + (1/3)^2 = 5/9 ≈ 0.5556
        # kappa = (2/3 - 5/9) / (1 - 5/9) = (1/9)/(4/9) = 0.25
        matrix = [
            [True, True, True, True],
            [True, True, True, False],
            [True, False, False, False],
        ]
        self.assertAlmostEqual(ss.fleiss_kappa(matrix), 0.25, delta=1e-9)

    def test_empty_input(self):
        self.assertEqual(ss.fleiss_kappa([]), 0.0)

    def test_single_rater_raises(self):
        with self.assertRaises(ValueError):
            ss.fleiss_kappa([[True], [False]])

    def test_uneven_rows_raises(self):
        with self.assertRaises(ValueError):
            ss.fleiss_kappa([[True, False, True], [True, False]])


# ---------------------------------------------------------------------------
# proportion_report / mean_report wrappers
# ---------------------------------------------------------------------------
class TestReportWrappers(unittest.TestCase):
    def test_proportion_report_auto_picks_wilson_for_n_ge_30(self):
        rep = ss.proportion_report([True] * 60 + [False] * 40)
        self.assertEqual(rep.method, "wilson_score")
        self.assertEqual(rep.n, 100)
        self.assertAlmostEqual(rep.point_estimate, 0.6, delta=1e-9)

    def test_proportion_report_auto_picks_exact_for_small_n(self):
        rep = ss.proportion_report([True] * 5 + [False] * 5)
        self.assertEqual(rep.method, "exact_binomial")
        self.assertEqual(rep.n, 10)

    def test_proportion_report_bootstrap_method(self):
        rep = ss.proportion_report(
            [True] * 60 + [False] * 40, method="bootstrap_percentile", seed=42
        )
        self.assertEqual(rep.method, "bootstrap_percentile")
        # Should bracket point estimate
        self.assertLess(rep.ci_low, rep.point_estimate)
        self.assertGreater(rep.ci_high, rep.point_estimate)

    def test_proportion_report_unknown_method_raises(self):
        with self.assertRaises(ValueError):
            ss.proportion_report([True, False], method="bogus")

    def test_proportion_report_empty(self):
        rep = ss.proportion_report([])
        self.assertEqual(rep.n, 0)
        self.assertEqual(rep.point_estimate, 0.0)

    def test_mean_report_basic(self):
        rep = ss.mean_report([0.5, 0.6, 0.7, 0.8, 0.9])
        self.assertEqual(rep.method, "bootstrap_percentile")
        self.assertAlmostEqual(rep.point_estimate, 0.7, delta=1e-9)
        self.assertEqual(rep.n, 5)

    def test_mean_report_empty(self):
        rep = ss.mean_report([])
        self.assertEqual(rep.n, 0)
        self.assertEqual(rep.point_estimate, 0.0)


# ---------------------------------------------------------------------------
# Smoke test — wide-range bootstrap should bracket reference
# ---------------------------------------------------------------------------
class TestBootstrapCoverage(unittest.TestCase):
    def test_coverage_on_known_distribution(self):
        # Generate 200 draws from p=0.7, verify CI brackets the true rate.
        rng = np.random.default_rng(2026)
        verdicts = (rng.random(200) < 0.7).tolist()
        low, high = ss.bootstrap_ci(verdicts, n_resamples=10_000, seed=42)
        self.assertLess(low, 0.7)
        self.assertGreater(high, 0.7)

    def test_ci_param_widens_for_higher_coverage(self):
        v = [True] * 50 + [False] * 50
        l95, h95 = ss.bootstrap_ci(v, n_resamples=10_000, ci=0.95, seed=42)
        l99, h99 = ss.bootstrap_ci(v, n_resamples=10_000, ci=0.99, seed=42)
        self.assertGreater(h99 - l99, h95 - l95)


if __name__ == "__main__":  # pragma: no cover
    unittest.main(verbosity=2)
