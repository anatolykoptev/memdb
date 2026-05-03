#!/usr/bin/env python3
"""
score.py — compute EM / F1 / f1_strict / semsim / hit@k / LLM Judge on a predictions JSON.

Reads the output of query.py, writes a results JSON:

{
  "meta": {...},
  "aggregate": {"em": 0.33, "f1": 0.51, "f1_strict": 0.49, "semsim": 0.78,
                "hit_at_k": 0.72,
                "llm_judge": 0.62,  # only when --llm-judge
                "n": 20, "by_category": {...}},
  "aggregate_with_excl_none": {...},   # all categories included (same as aggregate)
  "aggregate_with_excl_5": {...},       # category 5 excluded (mirrors Memobase benchmark)
  "per_qa": [{"conv_id", "question_idx", "em", "f1", "f1_strict", "semsim", "hit_at_k",
              "llm_score": 0|1, "llm_reason": str}, ...]  # llm_* only with --llm-judge
}

f1_strict uses Mem0-fork tokenization: lowercase + strip all Unicode punctuation.
Does NOT replace f1 (BoW with article/article removal); both are reported.

Two-track reporting: the JSON always contains BOTH aggregate_with_excl_none
(no exclusions) and aggregate_with_excl_5 (category 5 excluded) regardless of
any --exclude-categories flag. This mirrors Memobase's hardcoded exclusion of
category 5 in their published 75.78% score
(memobase_search.py:147 `exclude_category={5}`), enabling fair apples-to-apples
comparison without hiding our full-inclusive score.

If --exclude-categories is passed, additional aggregate_with_excl_<N> keys are
also emitted for each requested exclusion set.

Scoring uses the chat_answer if present, else the top-retrieved memory
content. If you want pure retrieval scoring, pass --retrieval-only.

Pass --llm-judge to additionally score each prediction via Gemini Flash
(CLIProxyAPI at http://127.0.0.1:8317/v1). Results are cached in
evaluation/locomo/results/.llm_judge_cache.json and reused on re-runs —
the second pass over unchanged predictions takes <1s.

Pre-M9 MILESTONES.md numbers are F1-only. LLM Judge (--llm-judge) produces
a number directly comparable to public leaderboard figures:
Memobase 75.78%, Mem0, Zep, LangMem all use the same binary judge.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import string
import subprocess
import sys
import unicodedata
from concurrent.futures import ThreadPoolExecutor, as_completed

from collections import Counter, defaultdict
from pathlib import Path

import requests


# --------------------- text normalization ---------------------
def normalize(text: str) -> str:
    if text is None:
        return ""
    text = str(text).lower()
    text = re.sub(r"\b(a|an|the)\b", " ", text)
    text = text.translate(str.maketrans("", "", string.punctuation))
    text = re.sub(r"\s+", " ", text).strip()
    return text


def tokens(text: str) -> list[str]:
    return normalize(text).split()



# --------------------- date/number/yn equivalence ---------------------

# Word-to-digit map for numbers 0-19 + decades
_WORD_TO_NUM: dict[str, str] = {
    "zero": "0", "one": "1", "two": "2", "three": "3", "four": "4",
    "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
    "ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13",
    "fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17",
    "eighteen": "18", "nineteen": "19", "twenty": "20", "thirty": "30",
    "forty": "40", "fifty": "50", "sixty": "60", "seventy": "70",
    "eighty": "80", "ninety": "90", "hundred": "100",
    # temporal synonyms → canonical digit form
    "decade": "10 years", "decades": "10 years",
    "century": "100 years", "half a century": "50 years",
}

# Yes/No normalization → canonical tokens
_YES_NORM = {"yes", "yeah", "yep", "yup", "affirmative", "correct", "indeed",
             "sure", "absolutely", "certainly", "of course"}
_NO_NORM = {"no", "nope", "nah", "negative", "not", "incorrect", "never"}

# Regex for ISO/slash/dot dates: 2023-09-13, 09/13/2023, 13.09.2023
_RE_ISO_DATE = re.compile(r"\b(\d{4})-(\d{1,2})-(\d{1,2})\b")
_RE_MDY_DATE = re.compile(r"\b(\d{1,2})[/.](\d{1,2})[/.](\d{4})\b")

# Regex for "N years (ago|later|earlier)" → extract year count
_RE_YEARS_PHRASE = re.compile(r"\b(\d+)\s*-?\s*year(?:s)?\b", re.IGNORECASE)
_RE_SINCE_YEAR = re.compile(r"\bsince\s+(\d{4})\b", re.IGNORECASE)


def _try_parse_date(text: str):
    """Try to extract a date from text; returns datetime.date or None."""
    try:
        from dateutil import parser as dup
        from dateutil.parser import ParserError
    except ImportError:
        return None
    # Only attempt if text looks date-ish (contains digits + month-word or separator)
    if not re.search(r"\d", text):
        return None
    try:
        return dup.parse(text, default=None, fuzzy=True).date()
    except (ParserError, ValueError, OverflowError, TypeError):
        return None


def _normalize_since(text: str) -> str:
    """Convert 'since YYYY' → 'N years' using 2023 as reference year.

    This makes 'Since 2016' comparable to '7 years' (both → '7 years' in 2023 context).
    LoCoMo conversations are set in 2023, so we use 2023 as the reference.
    """
    def _replace(m: re.Match) -> str:
        year = int(m.group(1))
        delta = 2023 - year
        return f"{delta} years"
    return _RE_SINCE_YEAR.sub(_replace, text)


def normalize_equivalent(text: str) -> str:
    """Normalize text for equivalence-aware F1.

    Applies three transforms (in order, on lowercased text):
    1. Yes/No synonyms → 'yes' / 'no'
    2. Word numbers → digits; decade/century synonyms → digit equivalents
    3. ISO dates (2023-09-13) → natural format that dateutil would also parse;
       'since YYYY' → 'N years'
    """
    if text is None:
        return ""
    t = str(text).lower().strip()

    # --- 1. yes/no ---
    stripped = t.rstrip(".,!?")
    if stripped in _YES_NORM:
        return "yes"
    if stripped in _NO_NORM:
        return "no"

    # --- 2. word numbers → digits ---
    def _replace_word(m: re.Match) -> str:
        w = m.group(0).lower()
        return _WORD_TO_NUM.get(w, w)

    t = re.sub(
        r"\b(" + "|".join(re.escape(k) for k in sorted(_WORD_TO_NUM, key=len, reverse=True)) + r")\b",
        _replace_word,
        t,
        flags=re.IGNORECASE,
    )

    # --- 3a. 'since YYYY' → 'N years' ---
    t = _normalize_since(t)

    # --- 3b. ISO date → human date string for consistent overlap with gold ---
    def _iso_to_human(m: re.Match) -> str:
        y, mo, d = int(m.group(1)), int(m.group(2)), int(m.group(3))
        import datetime
        try:
            dt = datetime.date(y, mo, d)
            return dt.strftime("%B %-d %Y")  # e.g. "September 13 2023"
        except ValueError:
            return m.group(0)

    t = _RE_ISO_DATE.sub(_iso_to_human, t)

    # normalize hyphenated year phrases: "10-year" → "10 year"
    t = re.sub(r"(\d+)-year", r"\1 year", t)

    return t


def token_f1_equivalent(pred: str, gold: str) -> float:
    """F1 on equivalence-normalized forms.

    Handles:
    - Date format variants (ISO ↔ natural language, within ~±1 week via dateutil)
    - Number/word equivalence ('7' ≡ 'seven'; 'decade' ≡ '10 years')
    - 'since YYYY' ≡ 'N years' (2023-anchored)
    - Yes/No synonyms
    Falls back to token_f1 on the normalized strings.
    """
    # First try date-aware comparison: if both sides resolve to the same date
    # (or within 7 days), substitute pred's date token with gold's date string
    # so token overlap is maximised.
    pred_n = normalize_equivalent(pred)
    gold_n = normalize_equivalent(gold)

    # Attempt dateutil parse on the *original* strings to check date proximity
    pred_date = _try_parse_date(pred)
    gold_date = _try_parse_date(gold)
    if pred_date and gold_date:
        import datetime
        if abs((pred_date - gold_date).days) <= 7:
            # Treat as same date — replace pred with gold text for token overlap
            pred_n = gold_n

    return token_f1(pred_n, gold_n)


def normalize_strict(text: str) -> str:
    """Mem0-fork strict tokenization: lowercase + strip all Unicode punctuation.

    Differs from normalize() which additionally removes English articles and
    uses string.punctuation (ASCII only).  Matches the Mem0 eval harness F1
    computation for apples-to-apples comparison.
    """
    if text is None:
        return ""
    text = str(text).lower()
    # Strip all Unicode punctuation categories (P*) and symbol categories (S*)
    text = "".join(ch if not unicodedata.category(ch).startswith(("P", "S")) else " " for ch in text)
    text = re.sub(r"\s+", " ", text).strip()
    return text


def tokens_strict(text: str) -> list[str]:
    return normalize_strict(text).split()


def token_f1_strict(pred: str, gold: str) -> float:
    """F1 using Mem0-fork strict tokenization (no article removal, Unicode punct strip)."""
    p_toks = tokens_strict(pred)
    g_toks = tokens_strict(gold)
    if not p_toks or not g_toks:
        return float(p_toks == g_toks)
    common = Counter(p_toks) & Counter(g_toks)
    num_same = sum(common.values())
    if num_same == 0:
        return 0.0
    precision = num_same / len(p_toks)
    recall = num_same / len(g_toks)
    return 2 * precision * recall / (precision + recall)


# --------------------- metrics ---------------------
def exact_match(pred: str, gold: str) -> float:
    return float(normalize(pred) == normalize(gold))


def token_f1(pred: str, gold: str) -> float:
    p_toks = tokens(pred)
    g_toks = tokens(gold)
    if not p_toks or not g_toks:
        return float(p_toks == g_toks)
    common = Counter(p_toks) & Counter(g_toks)
    num_same = sum(common.values())
    if num_same == 0:
        return 0.0
    precision = num_same / len(p_toks)
    recall = num_same / len(g_toks)
    return 2 * precision * recall / (precision + recall)


# Stop words that match in any text and produce false positives.
# Original hit_at_k counted any single token overlap — "in", "of", "the"
# were matching every memory and reporting hit@k=1.0 even when no real
# evidence was retrieved. Karpathy r3 forensic 2026-05-02 found that 11/15
# of "successful" retrievals on cat4/5 were actually false positives —
# gold "in awe of the universe" matched memos about painting/camping just
# because both contained "the/of/in".
_HIT_AT_K_STOP = frozenset((
    "a an the and or but if then else when where while of in on at to for from "
    "by with about as is are was were be been being am has have had do does did "
    "will would could should may might must can shall this that these those "
    "i you he she it we they me him her us them my your his its our their "
    "not no yes so very also too only just any some all each every other"
).split())


def hit_at_k(retrieved_contents: list[str], gold: str, *, min_overlap: int = 2) -> float:
    """Hit@k with stopword filtering + min overlap requirement.

    Counts a hit only when at least `min_overlap` content tokens of length>=4
    survive the stopword filter AND appear together in any retrieved memory.
    Falls back to single-overlap on len<3 — short answers like "Yes"/"3"/"2022"
    have only one informative token and shouldn't be penalised by the threshold.
    """
    if not retrieved_contents:
        return 0.0
    raw_toks = [t for t in tokens(gold) if t not in _HIT_AT_K_STOP]
    g_toks_long = [t for t in raw_toks if len(t) >= 4]
    g_set = set(g_toks_long) if g_toks_long else set(raw_toks)
    if not g_set:
        # gold is all stopwords (shouldn't normally happen) — fall back to original.
        g_set = set(tokens(gold))
        if not g_set:
            return 0.0
    threshold = min_overlap if len(g_set) >= min_overlap else 1
    for content in retrieved_contents:
        c_toks = set(tokens(content))
        if len(g_set & c_toks) >= threshold:
            return 1.0
    return 0.0


# --------------------- semantic similarity ---------------------
def bow_cosine(a: str, b: str) -> float:
    """Fallback: bag-of-words cosine on normalized tokens."""
    ca, cb = Counter(tokens(a)), Counter(tokens(b))
    if not ca or not cb:
        return 0.0
    vocab = set(ca) | set(cb)
    dot = sum(ca[t] * cb[t] for t in vocab)
    na = math.sqrt(sum(v * v for v in ca.values()))
    nb = math.sqrt(sum(v * v for v in cb.values()))
    if not na or not nb:
        return 0.0
    return dot / (na * nb)


class Embedder:
    """Calls an OpenAI-compatible embeddings endpoint.

    Supports embed-server (http://127.0.0.1:8082, no auth required) and any
    OpenAI-compatible endpoint.  When api_key is empty, the Authorization header
    is omitted so embed-server (which ignores auth) works without a dummy key.
    """

    def __init__(self, base: str, api_key: str, model: str):
        self.base = base.rstrip("/")
        self.api_key = api_key
        self.model = model
        self._cache: dict[str, list[float]] = {}

    def embed(self, text: str) -> list[float]:
        if text in self._cache:
            return self._cache[text]
        headers: dict[str, str] = {"Content-Type": "application/json"}
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        resp = requests.post(
            f"{self.base}/embeddings",
            json={"input": text, "model": self.model, "encoding_format": "float"},
            headers=headers,
            timeout=30,
        )
        resp.raise_for_status()
        emb = resp.json()["data"][0]["embedding"]
        self._cache[text] = emb
        return emb


def cosine(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if not na or not nb:
        return 0.0
    return dot / (na * nb)


# --------------------- main ---------------------
def pick_prediction_text(rec: dict, retrieval_only: bool) -> str:
    if not retrieval_only and rec.get("chat_answer"):
        return rec["chat_answer"]
    retrieved = rec.get("retrieved") or []
    # concatenate top-3 retrieved contents as pseudo-answer
    top = retrieved[:3]
    return " ".join(item.get("content", "") for item in top)


def _latency_percentiles(ms_values: list[float]) -> dict:
    """Return p50/p95 for a list of millisecond values; None entries are skipped."""
    vals = sorted(v for v in ms_values if v is not None)
    if not vals:
        return {"p50_ms": None, "p95_ms": None}
    p50 = vals[len(vals) // 2]
    p95 = vals[min(len(vals) - 1, int(len(vals) * 0.95))]
    return {"p50_ms": p50, "p95_ms": p95}


def summarize(per_qa: list[dict]) -> dict:
    if not per_qa:
        return {"em": 0.0, "f1": 0.0, "f1_strict": 0.0, "f1_equivalent": 0.0, "semsim": 0.0, "hit_at_k": 0.0, "n": 0, "by_category": {}}
    n = len(per_qa)
    agg: dict = {
        "em": sum(r["em"] for r in per_qa) / n,
        "f1": sum(r["f1"] for r in per_qa) / n,
        "f1_strict": sum(r["f1_strict"] for r in per_qa) / n,
        "f1_equivalent": sum(r.get("f1_equivalent", 0.0) for r in per_qa) / n,
        "semsim": sum(r["semsim"] for r in per_qa) / n,
        "hit_at_k": sum(r["hit_at_k"] for r in per_qa) / n,
        "n": n,
    }
    # LLM Judge aggregate — only when scores are present
    llm_rows = [r["llm_score"] for r in per_qa if "llm_score" in r]
    if llm_rows:
        agg["llm_judge"] = sum(llm_rows) / len(llm_rows)
        agg["llm_judge_n"] = len(llm_rows)

    # Latency aggregates (search + chat, from query.py captured fields)
    agg["latency"] = {
        "search": _latency_percentiles([r.get("search_ms") for r in per_qa]),
        "chat": _latency_percentiles([r.get("chat_ms") for r in per_qa]),
    }

    by_cat: dict[str, list[dict]] = defaultdict(list)
    for r in per_qa:
        by_cat[str(r.get("category"))].append(r)

    cat_summaries: dict[str, dict] = {}
    for cat, rs in by_cat.items():
        cat_agg: dict = {
            "em": sum(r["em"] for r in rs) / len(rs),
            "f1": sum(r["f1"] for r in rs) / len(rs),
            "f1_strict": sum(r["f1_strict"] for r in rs) / len(rs),
            "f1_equivalent": sum(r.get("f1_equivalent", 0.0) for r in rs) / len(rs),
            "semsim": sum(r["semsim"] for r in rs) / len(rs),
            "hit_at_k": sum(r["hit_at_k"] for r in rs) / len(rs),
            "n": len(rs),
            "latency": {
                "search": _latency_percentiles([r.get("search_ms") for r in rs]),
                "chat": _latency_percentiles([r.get("chat_ms") for r in rs]),
            },
        }
        cat_llm = [r["llm_score"] for r in rs if "llm_score" in r]
        if cat_llm:
            cat_agg["llm_judge"] = sum(cat_llm) / len(cat_llm)
        cat_summaries[cat] = cat_agg

    agg["by_category"] = cat_summaries
    return agg


def git_sha() -> str:
    try:
        return (
            subprocess.check_output(
                ["git", "-C", str(Path(__file__).resolve().parent), "rev-parse", "HEAD"],
                stderr=subprocess.DEVNULL,
            )
            .decode()
            .strip()
        )
    except Exception:
        return "unknown"


def parse_exclude_categories(value: str) -> frozenset[int]:
    """Parse comma-separated ints like '5' or '4,5' into a frozenset."""
    if not value.strip():
        return frozenset()
    return frozenset(int(x.strip()) for x in value.split(",") if x.strip())


def excl_key(excl: frozenset[int]) -> str:
    """Return the JSON key suffix for a given exclusion set.

    frozenset()  → 'aggregate_with_excl_none'
    frozenset({5}) → 'aggregate_with_excl_5'
    frozenset({4,5}) → 'aggregate_with_excl_4_5'
    """
    if not excl:
        return "aggregate_with_excl_none"
    return "aggregate_with_excl_" + "_".join(str(c) for c in sorted(excl))


def build_aggregate_tracks(
    per_qa: list[dict], extra_excl_sets: list[frozenset[int]]
) -> dict[str, dict]:
    """Return a dict of aggregate dicts keyed by excl_key().

    Always includes excl_none (full) and excl_{5} (cat-5 excluded).
    extra_excl_sets adds further tracks if requested via --exclude-categories.
    """
    # Canonical set of tracks: always emit these two regardless of flags
    tracks: dict[str, frozenset[int]] = {
        "aggregate_with_excl_none": frozenset(),
        "aggregate_with_excl_5": frozenset({5}),
    }
    for s in extra_excl_sets:
        k = excl_key(s)
        if k not in tracks:
            tracks[k] = s
        # Also emit the union with {5} for publication-comparable scoring
        # (Memobase convention: cat-5 always excluded).  Skip when s already
        # contains 5 — the union would be identical to the track just added.
        union = s | frozenset({5})
        uk = excl_key(union)
        if uk not in tracks:
            tracks[uk] = union
    result: dict[str, dict] = {}
    for key, excl in tracks.items():
        filtered = [r for r in per_qa if int(r.get("category") or 0) not in excl]
        result[key] = summarize(filtered)
    return result


def print_dual_track_summary(agg_tracks: dict[str, dict]) -> None:
    """Print a side-by-side summary block for all aggregate tracks."""
    keys = sorted(agg_tracks.keys())
    if not keys:
        return
    col_w = 12
    header = f"{'Metric':<12s}" + "".join(f"  {k:<{col_w}s}" for k in keys)
    print("\n── Dual-track aggregate ──")
    print(header)
    print("-" * len(header))
    # Include llm_judge row only if at least one track has it
    metrics = ["n", "em", "f1", "f1_strict", "f1_equivalent", "semsim", "hit_at_k"]
    if any("llm_judge" in agg_tracks[k] for k in keys):
        metrics.append("llm_judge")
    for metric in metrics:
        row = f"{metric:<12s}"
        for k in keys:
            val = agg_tracks[k].get(metric, 0)
            if metric == "n":
                row += f"  {int(val):<{col_w}d}"
            else:
                row += f"  {val:<{col_w}.3f}"
        print(row)


def _fmt_ms(val: float | None) -> str:
    if val is None:
        return "  n/a"
    return f"{int(val):>5}ms"


def _print_telemetry(
    per_qa: list[dict],
    by_cat: dict[str, dict],
    cat_names: dict[str, str],
) -> None:
    """Print competitor-comparable telemetry block (latency p50/p95 per category).

    Token fields (prompt_tokens / completion_tokens) are NOT yet captured by
    query.py — if they appear in future predictions, add them here.
    """
    print("\n── Telemetry ──")

    # Overall latency
    all_search = [r.get("search_ms") for r in per_qa]
    all_chat = [r.get("chat_ms") for r in per_qa]
    s_lat = _latency_percentiles(all_search)
    c_lat = _latency_percentiles(all_chat)

    has_latency = any(v is not None for v in all_search + all_chat)
    if not has_latency:
        print("  (no latency data in predictions file)")
    else:
        print(
            f"  {'overall':<12}  search p50={_fmt_ms(s_lat['p50_ms'])}  "
            f"p95={_fmt_ms(s_lat['p95_ms'])}   "
            f"chat p50={_fmt_ms(c_lat['p50_ms'])}  p95={_fmt_ms(c_lat['p95_ms'])}"
        )

    if len(by_cat) > 1 and has_latency:
        print(f"\n  {'cat':<3}  {'name':<12}  {'srch p50':>8}  {'srch p95':>8}  {'chat p50':>8}  {'chat p95':>8}")
        for cat in sorted(by_cat.keys(), key=lambda c: int(c) if c.isdigit() else 99):
            cv = by_cat[cat]
            lat = cv.get("latency", {})
            s = lat.get("search", {})
            c = lat.get("chat", {})
            name = cat_names.get(cat, "unknown")
            print(
                f"  {cat:<3}  {name:<12}  "
                f"{_fmt_ms(s.get('p50_ms')):>8}  {_fmt_ms(s.get('p95_ms')):>8}  "
                f"{_fmt_ms(c.get('p50_ms')):>8}  {_fmt_ms(c.get('p95_ms')):>8}"
            )

    # Token usage — not available until query.py captures them from the LLM response
    total_prompt = sum(r.get("prompt_tokens") or 0 for r in per_qa)
    total_completion = sum(r.get("completion_tokens") or 0 for r in per_qa)
    if total_prompt or total_completion:
        print(f"\n  tokens (total): prompt={total_prompt}  completion={total_completion}  "
              f"sum={total_prompt + total_completion}")
    else:
        print("\n  tokens: not captured (add prompt_tokens/completion_tokens to query.py predictions)")


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--predictions", type=Path, required=True)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument(
        "--retrieval-only", action="store_true", help="Score top-retrieved instead of chat_answer."
    )
    p.add_argument(
        "--embed-model",
        default=os.getenv("LOCOMO_EMBED_MODEL", "multilingual-e5-large"),
        help="Embedding model name (default: multilingual-e5-large, matching embed-server).",
    )
    # EMBED_URL takes priority for semsim; falls back to LLM_URL for backward compat.
    # embed-server (http://127.0.0.1:8082) requires no auth — set EMBED_API_KEY="" or omit.
    p.add_argument(
        "--embed-url",
        default=os.getenv("EMBED_URL", os.getenv("LLM_URL", "")),
        help=(
            "Base URL for embeddings endpoint including /v1 prefix "
            "(e.g. http://127.0.0.1:8082/v1). "
            "Uses EMBED_URL env, falls back to LLM_URL."
        ),
    )
    p.add_argument(
        "--embed-api-key",
        default=os.getenv("EMBED_API_KEY", os.getenv("LLM_API_KEY", "")),
        help="Bearer token for embeddings endpoint (empty = no auth, for embed-server).",
    )
    p.add_argument("--llm-url", default=os.getenv("LLM_URL", ""))
    p.add_argument("--llm-api-key", default=os.getenv("LLM_API_KEY", ""))
    p.add_argument("--no-embed", action="store_true", help="Skip embedding API, use BoW cosine.")
    p.add_argument(
        "--exclude-categories",
        default="",
        help=(
            "Comma-separated category ints to exclude, e.g. '5' or '4,5'. "
            "Output always contains aggregate_with_excl_none and aggregate_with_excl_5 "
            "regardless of this flag. Flag adds additional tracks."
        ),
    )
    p.add_argument(
        "--llm-judge",
        action="store_true",
        help=(
            "Score each prediction via Gemini Flash LLM judge (CLIProxyAPI). "
            "Adds llm_score/llm_reason per QA and llm_judge aggregate. "
            "Results cached in results/.llm_judge_cache.json."
        ),
    )
    p.add_argument(
        "--llm-judge-model",
        default=os.getenv("LOCOMO_JUDGE_MODEL", "gemini-2.5-flash"),
        help="Model for LLM judge (default: gemini-2.5-flash).",
    )
    p.add_argument(
        "--llm-judge-workers",
        type=int,
        default=10,
        help="Concurrent judge calls (default: 10).",
    )
    args = p.parse_args()
    extra_excl = parse_exclude_categories(args.exclude_categories)

    with args.predictions.open() as f:
        pred_doc = json.load(f)
    predictions = pred_doc.get("predictions", pred_doc if isinstance(pred_doc, list) else [])
    predictions.sort(key=lambda r: (r.get("conv_id", ""), r.get("question_idx", 0)))

    embedder = None
    semsim_mode = "bow"
    # embed-server requires no auth, so we only need a non-empty URL (not api_key).
    if not args.no_embed and args.embed_url:
        embedder = Embedder(args.embed_url, args.embed_api_key, args.embed_model)
        semsim_mode = f"embed:{args.embed_model}@{args.embed_url}"

    per_qa: list[dict] = []
    for rec in predictions:
        gold = str(rec.get("gold_answer", ""))
        pred = pick_prediction_text(rec, args.retrieval_only)
        retrieved_contents = [i.get("content", "") for i in (rec.get("retrieved") or [])]
        em = exact_match(pred, gold)
        f1 = token_f1(pred, gold)
        f1s = token_f1_strict(pred, gold)
        f1e = token_f1_equivalent(pred, gold)
        h = hit_at_k(retrieved_contents, gold)
        if embedder and pred.strip() and gold.strip():
            try:
                sim = cosine(embedder.embed(pred[:4000]), embedder.embed(gold[:4000]))
            except (requests.RequestException, KeyError, ValueError) as exc:
                print(f"  embed fail ({exc}) — fallback to BoW", file=sys.stderr)
                sim = bow_cosine(pred, gold)
        else:
            sim = bow_cosine(pred, gold)
        per_qa.append(
            {
                "conv_id": rec.get("conv_id"),
                "question_idx": rec.get("question_idx"),
                "question": rec.get("question"),
                "category": rec.get("category"),
                "gold_answer": gold,
                "prediction": pred[:500],
                "em": em,
                "f1": f1,
                "f1_strict": f1s,
                "f1_equivalent": f1e,
                "semsim": sim,
                "hit_at_k": h,
                "search_ms": rec.get("search_ms"),
                "chat_ms": rec.get("chat_ms"),
                "error": rec.get("error"),
            }
        )

    # ----- LLM Judge (concurrent) -----
    if args.llm_judge:
        from llm_judge import get_shared_cache, judge  # noqa: PLC0415

        judge_cache = get_shared_cache()
        # CLI flags win over env: --llm-url and --llm-api-key are explicit user intent.
        # Falling back to env was the pre-CLI-flag default and caused 401 cascades when
        # the runner did not export CLI_PROXY_API_KEY/LLM_API_KEY into the python subshell.
        judge_api_base = (
            args.llm_url or os.getenv("LLM_API_BASE") or "http://127.0.0.1:8317/v1"
        )
        judge_api_key = (
            args.llm_api_key
            or os.getenv("CLI_PROXY_API_KEY")
            or os.getenv("LLM_API_KEY")
            or ""
        )

        print(
            f"Running LLM Judge ({args.llm_judge_model}, "
            f"workers={args.llm_judge_workers}, n={len(per_qa)}) ...",
            file=sys.stderr,
        )

        def _judge_row(row: dict) -> dict:
            result = judge(
                question=row.get("question") or "",
                gold=row.get("gold_answer") or "",
                prediction=row.get("prediction") or "",
                model=args.llm_judge_model,
                api_base=judge_api_base,
                api_key=judge_api_key,
                cache=judge_cache,
            )
            return result

        try:
            with ThreadPoolExecutor(max_workers=args.llm_judge_workers) as executor:
                futures = {executor.submit(_judge_row, row): i for i, row in enumerate(per_qa)}
                done = 0
                for fut in as_completed(futures):
                    idx = futures[fut]
                    result = fut.result()
                    per_qa[idx]["llm_score"] = result["score"]
                    per_qa[idx]["llm_reason"] = result["reason"]
                    done += 1
                    if done % 50 == 0:
                        print(f"  judge {done}/{len(per_qa)} ...", file=sys.stderr)
        finally:
            judge_cache.flush()

        errors = sum(1 for r in per_qa if str(r.get("llm_reason", "")).startswith("judge_error:"))
        if errors:
            print(f"  judge_error count: {errors}/{len(per_qa)}", file=sys.stderr)

    agg = summarize(per_qa)
    # Build dual-track (and any extra) aggregate dicts
    extra_excl_sets: list[frozenset[int]] = [extra_excl] if extra_excl else []
    agg_tracks = build_aggregate_tracks(per_qa, extra_excl_sets)

    # by_category is available both nested in aggregate (backward compat with compare.py)
    # and as a top-level key per M2 spec so callers can read it directly.
    out: dict = {
        "meta": {
            "commit_sha": git_sha(),
            "predictions_file": str(args.predictions),
            "retrieval_only": args.retrieval_only,
            "semsim_mode": semsim_mode,
            "exclude_categories": sorted(extra_excl) if extra_excl else [],
            "llm_judge_model": args.llm_judge_model if args.llm_judge else None,
            **pred_doc.get("meta", {}),
        },
        "aggregate": agg,
        "by_category": agg.get("by_category", {}),
        **agg_tracks,
        "per_qa": per_qa,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)

    judge_line = ""
    if "llm_judge" in agg:
        judge_line = f"  llm_judge={agg['llm_judge']:.3f}"
    print(
        f"n={agg['n']}  em={agg['em']:.3f}  f1={agg['f1']:.3f}  f1_strict={agg['f1_strict']:.3f}  "
        f"f1_equiv={agg['f1_equivalent']:.3f}  semsim={agg['semsim']:.3f}  hit@k={agg['hit_at_k']:.3f}"
        + judge_line
    )
    # Print per-category summary if more than one category present
    by_cat = agg.get("by_category", {})
    has_llm = any("llm_judge" in cv for cv in by_cat.values())
    cat_names = {
        "1": "single-hop",
        "2": "multi-hop",
        "3": "temporal",
        "4": "open-domain",
        "5": "adversarial",
    }
    if len(by_cat) > 1:
        hdr_llm = f"  {'llm_j':>6}" if has_llm else ""
        print("\nPer-category breakdown:")
        print(
            f"  {'cat':<3}  {'name':<12}  {'n':>4}  {'em':>6}  {'f1':>6}  {'f1_str':>7}  "
            f"{'f1_eqv':>7}  {'semsim':>7}  {'hit@k':>6}"
            + hdr_llm
        )
        for cat in sorted(by_cat.keys(), key=lambda c: int(c) if c.isdigit() else 99):
            cv = by_cat[cat]
            name = cat_names.get(cat, "unknown")
            llm_col = f"  {cv['llm_judge']:>6.3f}" if "llm_judge" in cv else ""
            print(
                f"  {cat:<3}  {name:<12}  {cv['n']:>4}  "
                f"{cv['em']:>6.3f}  {cv['f1']:>6.3f}  {cv['f1_strict']:>7.3f}  "
                f"{cv.get('f1_equivalent', 0.0):>7.3f}  {cv['semsim']:>7.3f}  {cv['hit_at_k']:>6.3f}"
                + llm_col
            )
    # Always print dual-track summary
    print_dual_track_summary(agg_tracks)

    # ── Telemetry ──
    _print_telemetry(per_qa, by_cat, cat_names)

    print(f"wrote → {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
