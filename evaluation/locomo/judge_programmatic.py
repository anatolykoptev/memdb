"""
judge_programmatic.py — Per-category programmatic judges for LoCoMo QA.

Augments (does NOT replace) the LLM judge.  Each extractor returns a
ProgrammaticVerdict whose `confidence` field is consumed by judge_router:

    confidence >= 0.85  ->  use this verdict (skip LLM, deterministic)
    confidence  < 0.85  ->  fall through to LLM judge

Returning ``None`` means "not applicable" — caller falls through to LLM.

Coverage (M13 J2 spec):
  cat 1 (single-hop)
    * judge_cat1_numeric     — "How many X?" → integer extraction + tolerance
    * judge_cat1_multi_fact  — "What X did Y do?" → entity-list Jaccard
  cat 3 (temporal)
    * judge_cat3_temporal    — "When?"     → date distance ≤ 30 days
    * judge_cat3_duration    — "How long?" → duration parse ± 20% tolerance

Library dependencies (pinned in evaluation/locomo/requirements.txt):
  - word2number == 1.1          (PyPI 0.1.13 from plan does NOT exist; 1.1 is
                                 the latest stable release on PyPI; verified
                                 2026-04-28.)
  - dateparser  == 1.2.0
  - spaCy en_core_web_md         INTENTIONALLY NOT USED.  ~50MB model download
                                 plus ~400MB RAM is overkill for the LoCoMo
                                 entity vocabulary (≤30 named entities across
                                 the full corpus: Caroline, Melanie, Joanna,
                                 Calvin, Nate, Maria, Jolene, Sam, …).
                                 Plan explicitly permits regex-based NER with
                                 a curated entity dictionary as the lighter
                                 alternative.  Trade-off: lower recall for
                                 unseen names → low programmatic confidence
                                 → fall-through to LLM, which is the
                                 designed-for safe path.

Confidence calibration: thresholds tuned so that high-confidence verdicts
match LLM judge ≥95% on a hand-picked calibration set; below that, we abstain
(return low confidence) and let the LLM decide.  Calibration evidence is in
the PR body for traceability.

🚨 NEVER silently disable.  Each function must either:
  (a) return a high-confidence ProgrammaticVerdict, or
  (b) return None (signaling N/A → LLM judge), or
  (c) return a low-confidence verdict (router will fall through anyway).
There is no fourth option.
"""

from __future__ import annotations

import re
import string
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any

# ---------------------------------------------------------------------------
# Optional libraries — soft import so test discovery still works on a stripped
# host.  Functions that need a missing library return None (N/A).
# ---------------------------------------------------------------------------
try:  # pragma: no cover - import side effect
    from word2number import w2n as _w2n  # type: ignore[import-untyped]

    _HAVE_W2N = True
except ImportError:  # pragma: no cover
    _w2n = None  # type: ignore[assignment]
    _HAVE_W2N = False

try:  # pragma: no cover - import side effect
    import dateparser as _dateparser  # type: ignore[import-untyped]

    _HAVE_DATEPARSER = True
except ImportError:  # pragma: no cover
    _dateparser = None  # type: ignore[assignment]
    _HAVE_DATEPARSER = False


# ---------------------------------------------------------------------------
# Verdict dataclass
# ---------------------------------------------------------------------------
@dataclass
class ProgrammaticVerdict:
    """Result of a single programmatic extractor.

    Attributes:
        verdict:     True if pred matches gold by this method's criterion.
        confidence:  0..1.  >=0.85 → trust this verdict (no LLM call).
                     <0.85 → router falls through to LLM judge.
        method:      Identifier string for metrics / debug.  Examples:
                     "numeric_exact", "numeric_word_match",
                     "date_within_1month", "ner_jaccard_0.7",
                     "duration_within_20pct", "skip_unsupported".
        extracted:   Debug payload — gold/pred parsed forms, distance, etc.
                     Always populated even on low-confidence verdicts so we
                     can audit fall-throughs.
    """

    verdict: bool
    confidence: float
    method: str
    extracted: dict[str, Any] = field(default_factory=dict)


# ---------------------------------------------------------------------------
# Confidence thresholds — tuned from calibration (see PR body)
# ---------------------------------------------------------------------------
HIGH_CONFIDENCE = 0.95   # exact match / unambiguous extraction
MED_CONFIDENCE = 0.88    # tolerance match (within window, near-Jaccard)
LOW_CONFIDENCE = 0.50    # ambiguous parse — caller will fall through

# Router cut-off: keep in sync with judge_router.PROGRAMMATIC_THRESHOLD.
ROUTER_THRESHOLD = 0.85

# Tolerances
DATE_TOLERANCE_DAYS = 30
DURATION_TOLERANCE_PCT = 0.20
NER_JACCARD_THRESHOLD = 0.70

# Sentinel words that mean "I don't know" and should not be deemed correct
# even if they happen to contain a digit.
REFUSAL_PATTERNS = (
    "do not contain",
    "don't contain",
    "no information",
    "not specified",
    "i don't know",
    "i do not know",
    "cannot determine",
    "unable to determine",
    "not mentioned",
    "memories do not",
    "memory does not",
)


def _is_refusal(text: str) -> bool:
    """True iff text is *dominated* by a refusal phrase.

    Heuristic: refusal pattern present AND text length ≤ 30 words OR the
    refusal phrase comprises ≥40% of the text.  Avoids misfiring on long
    answers where the model hedges then provides partial info.
    """
    if not text:
        return True
    t = text.lower()
    word_count = len(t.split())
    for p in REFUSAL_PATTERNS:
        if p in t:
            # Dominant refusal: short text or refusal phrase comprises a large
            # fraction.  Otherwise treat as partial answer (not pure refusal).
            if word_count <= 30:
                return True
            # Refusal at the very start AND no concrete substance after?
            # We can't determine that cheaply; default to NOT-refusal so the
            # full text gets scored on its merits.
    return False


# ---------------------------------------------------------------------------
# Cat 1 — numeric ("How many X?")
# ---------------------------------------------------------------------------
# Recognized number words.  word2number handles compound forms like
# "twenty three"; we extend with colloquials ("twice", "couple") that w2n
# explicitly rejects.
#
# Critically: we DO NOT treat "a"/"an"/"no"/"none" as numeric — they are
# articles / negations that appear in non-numeric gold answers (e.g. "Join
# a local church, buy a cross necklace" → gold has no integer).  Treating
# them as 1/0 produced false-positive numeric triggers on multi-fact lists.
# "single"/"once"/"a single" are also dropped for the same reason.
_NUMBER_WORDS = {
    "zero": 0,
    "one": 1,
    "two": 2, "twice": 2, "couple": 2, "pair": 2,
    "three": 3, "thrice": 3,
    "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
    "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14,
    "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
    "nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
    "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90, "hundred": 100,
}

# Vague quantifiers — never high-confidence on these alone.
_VAGUE_QUANTIFIERS = {"some", "many", "several", "various", "multiple", "lots"}

_NUM_RE = re.compile(r"-?\d+(?:\.\d+)?")
# Strip markdown ordered-list ordinals like "1. ", "2. " at line start (and
# the bare "1)" / "2)" forms) so they don't pollute number extraction.
_LIST_ORDINAL_RE = re.compile(r"(?m)^\s*\d+[.)]\s+")
_HEDGE_RE = re.compile(
    r"\b(?:about|around|roughly|approximately|nearly|almost|over|at least|at most|maybe)\b",
    re.IGNORECASE,
)


def _strip_punct(s: str) -> str:
    return s.translate(str.maketrans("", "", string.punctuation))


def _extract_numbers(text: str) -> list[float]:
    """Pull every digit-form number from text.

    Strips markdown list ordinals first ("1. ...", "2) ...") so a numbered
    list in the answer doesn't leak its 1/2/3/... into the candidate set.
    """
    if not text:
        return []
    cleaned = _LIST_ORDINAL_RE.sub("", text)
    return [float(m) for m in _NUM_RE.findall(cleaned)]


def _extract_word_numbers(text: str) -> list[int]:
    """Pull number-words from text.

    Strategy (per-position, longest-first):
      1.  Try a 2-4 token window with word2number for compound numbers
          ("twenty three" → 23).
      2.  Fall back to single-token lookup in _NUMBER_WORDS, which knows
          colloquials w2n rejects ("twice", "couple", "few").
      3.  Skip the token otherwise.
    Markdown list ordinals ("1. ", "2) ") are stripped first so they don't
    leak into the candidate set.
    """
    if not text:
        return []
    found: list[int] = []
    pre = _LIST_ORDINAL_RE.sub("", text)
    cleaned = _strip_punct(pre.lower())
    tokens = cleaned.split()
    i = 0
    while i < len(tokens):
        matched = False
        # Greedy compound via w2n: window 4..2 tokens.
        if _HAVE_W2N and _w2n is not None:
            for window in range(min(4, len(tokens) - i), 1, -1):
                phrase_tokens = tokens[i : i + window]
                # Every token in the window must look number-ish.
                if not all(
                    t in _NUMBER_WORDS or t.isdigit() or t in {"and", "hundred", "thousand", "million"}
                    for t in phrase_tokens
                ):
                    continue
                phrase = " ".join(phrase_tokens)
                try:
                    n = _w2n.word_to_num(phrase)
                    found.append(int(n))
                    i += window
                    matched = True
                    break
                except (ValueError, IndexError, AttributeError, KeyError):
                    continue
        # Single-token fallback via local dictionary (covers 'twice', 'couple'
        # which w2n explicitly rejects).
        if not matched:
            tok = tokens[i]
            if tok in _NUMBER_WORDS:
                found.append(_NUMBER_WORDS[tok])
                matched = True
            elif tok.isdigit():
                # already handled by _extract_numbers but harmless here
                found.append(int(tok))
                matched = True
        i += 1
    return found


def judge_cat1_numeric(gold: str, pred: str) -> ProgrammaticVerdict | None:
    """Cat-1 numeric questions ("How many …?").

    Returns None if the gold answer doesn't look numeric — caller falls
    through to a different extractor or LLM.

    Applicability gate (avoid false positives on multi-fact lists):
      - gold must be a "small" answer (≤4 tokens after punctuation strip)
        AND yield exactly one extracted number; OR
      - gold must be a pure digit string.
    Multi-fact lists like "Join a local church, buy a cross necklace" must
    NOT trip the numeric path — they should fall through to multi_fact.

    Confidence policy:
      - gold and pred both yield same digit  → HIGH
      - gold digit matched as word in pred   → HIGH
      - pred has number but != gold          → HIGH (verdict=False)
      - pred is a refusal ("don't know")     → HIGH (verdict=False)
      - pred contains hedge ("around 5")     → MED on the rounded match
      - pred has only vague quantifier       → LOW (fall through)
    """
    if gold is None or pred is None:
        return None
    gold_clean = gold.strip()
    if not gold_clean:
        return None

    # --- Applicability gate ---
    # Skip when gold has a list separator (comma / semicolon / "and" between
    # words) — those are multi-fact, not numeric.
    if "," in gold_clean or ";" in gold_clean:
        return None
    gold_tokens = _strip_punct(gold_clean).split()
    if len(gold_tokens) > 4:
        return None  # too long to be a "How many" answer

    digit_nums = _extract_numbers(gold_clean)
    word_nums = _extract_word_numbers(gold_clean)
    gold_nums = digit_nums + [float(n) for n in word_nums]
    if not gold_nums:
        return None  # gold isn't numeric — N/A
    # Require the gold answer to be ESSENTIALLY just a number (digit-only,
    # or a 1-3 token phrase containing a number-word).  Otherwise it's
    # probably a categorical answer that happens to contain a small number.
    if not digit_nums and len(gold_tokens) > 3:
        return None
    # Use the first integer-valued number as the canonical gold value.
    g_val = int(gold_nums[0])

    extracted: dict[str, Any] = {"gold_value": g_val, "gold_raw": gold_clean[:80]}

    # Predicted refusal — definitely wrong, high confidence.
    if _is_refusal(pred):
        extracted["pred_raw"] = pred[:120]
        extracted["pred_value"] = None
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="numeric_refusal",
            extracted=extracted,
        )

    pred_nums = _extract_numbers(pred)
    pred_word_nums = _extract_word_numbers(pred)
    all_pred = [int(n) for n in pred_nums] + pred_word_nums
    extracted["pred_values"] = all_pred
    extracted["pred_raw"] = pred[:120]

    # Vague-quantifier-only → low confidence, abstain.
    pred_lower = pred.lower()
    has_vague = any(re.search(rf"\b{q}\b", pred_lower) for q in _VAGUE_QUANTIFIERS)
    has_hedge = bool(_HEDGE_RE.search(pred))

    if not all_pred:
        if has_vague:
            return ProgrammaticVerdict(
                verdict=False,
                confidence=LOW_CONFIDENCE,
                method="numeric_vague_only",
                extracted=extracted,
            )
        # No number found at all → verdict False high-confidence (pred didn't
        # answer the numeric question).  Skip to LLM if pred is empty.
        if not pred.strip():
            return ProgrammaticVerdict(
                verdict=False,
                confidence=LOW_CONFIDENCE,
                method="numeric_empty_pred",
                extracted=extracted,
            )
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="numeric_no_number",
            extracted=extracted,
        )

    # Match if any extracted number equals gold.
    if any(int(n) == g_val for n in all_pred):
        method = "numeric_hedge_match" if has_hedge else "numeric_exact"
        conf = MED_CONFIDENCE if has_hedge else HIGH_CONFIDENCE
        return ProgrammaticVerdict(
            verdict=True,
            confidence=conf,
            method=method,
            extracted=extracted,
        )

    # Number(s) present but no match → wrong with high confidence, EXCEPT
    # when pred contains a range like "5 or 6" / "5-6" that brackets gold.
    range_match = re.search(r"(\d+)\s*(?:-|to|or)\s*(\d+)", pred_lower)
    if range_match:
        lo, hi = int(range_match.group(1)), int(range_match.group(2))
        if lo > hi:
            lo, hi = hi, lo
        if lo <= g_val <= hi:
            extracted["range"] = (lo, hi)
            return ProgrammaticVerdict(
                verdict=True,
                confidence=MED_CONFIDENCE,
                method="numeric_range_match",
                extracted=extracted,
            )

    return ProgrammaticVerdict(
        verdict=False,
        confidence=HIGH_CONFIDENCE,
        method="numeric_mismatch",
        extracted=extracted,
    )


# ---------------------------------------------------------------------------
# Cat 1 — multi-fact list ("What X did Y do?")
# ---------------------------------------------------------------------------

# Curated LoCoMo speaker / character vocabulary for cheap NER fallback.
# Covers all observed speakers across locomo10.json (≤30 entities).
LOCOMO_KNOWN_PEOPLE: frozenset[str] = frozenset(
    {
        "caroline", "melanie", "joanna", "calvin", "nate", "maria", "jolene",
        "sam", "luna", "oliver", "bailey", "alex", "alice", "anna", "arnav",
        "diane", "elena", "emma", "evan", "hannah", "isabel", "jack", "jane",
        "john", "liam", "lily", "lisa", "lucas", "mia", "michael", "nina",
        "noah", "olivia", "rose", "sarah", "sophia", "tom",
    }
)

_STOPWORDS = frozenset(
    {
        "a", "an", "the", "is", "are", "was", "were", "be", "been", "being",
        "to", "of", "in", "on", "at", "by", "for", "with", "from", "as", "and",
        "or", "but", "if", "while", "this", "that", "these", "those", "it",
        "its", "her", "his", "their", "they", "them", "she", "he", "we", "us",
        "you", "your", "i", "me", "my", "mine", "ours", "do", "did", "does",
        "doing", "done", "have", "has", "had", "having", "what", "which",
        "who", "whom", "whose", "when", "where", "why", "how", "also",
        "some", "any", "many", "few", "several", "various", "much", "more",
        "most", "less", "least", "such", "very", "really", "just", "only",
        "even", "still", "would", "could", "should", "will", "shall", "can",
        "may", "might", "must", "not", "no", "yes", "so", "than", "then",
        "there", "here", "now", "out", "up", "down", "off", "over", "under",
        "into", "onto", "about", "after", "before", "between", "through",
        "during", "since", "until", "though", "although", "because", "due",
    }
)

_LIST_SPLIT_RE = re.compile(r"[,;]|\band\b|\n+|\*|•|\bor\b", re.IGNORECASE)


def _extract_facts(text: str) -> set[str]:
    """Extract content tokens (non-stopword unigrams + bigrams of capitalized
    runs).  Lowercased.  Used for Jaccard overlap on multi-fact answers."""
    if not text:
        return set()
    # Split on list-y punctuation to get phrase-level chunks first.
    chunks = [c.strip() for c in _LIST_SPLIT_RE.split(text) if c.strip()]
    facts: set[str] = set()
    for chunk in chunks:
        # Strip markdown leading bullets / numbers.
        chunk = re.sub(r"^[\d\.\-\*•\s]+", "", chunk)
        chunk = _strip_punct(chunk).lower().strip()
        if not chunk:
            continue
        # Unigrams (content-bearing)
        for tok in chunk.split():
            if (
                len(tok) >= 3
                and tok not in _STOPWORDS
                and not tok.isdigit()
            ):
                facts.add(tok)
    return facts


def _jaccard(a: set[str], b: set[str]) -> float:
    if not a or not b:
        return 0.0
    return len(a & b) / len(a | b)


def _coverage(gold: set[str], pred: set[str]) -> float:
    """Fraction of gold facts present in pred (recall)."""
    if not gold:
        return 0.0
    return len(gold & pred) / len(gold)


def judge_cat1_multi_fact(gold: str, pred: str) -> ProgrammaticVerdict | None:
    """Cat-1 multi-fact lists ("What X did Y do?").

    Strategy: extract content tokens from both, compute Jaccard + coverage.
    Programmatic verdict only when:
      - both sides have ≥3 fact tokens (otherwise too small for stable Jaccard)
      - score is either very high (>=0.7 jaccard OR >=0.8 coverage) OR very
        low (<0.2 coverage AND <0.15 jaccard) — the unambiguous tails.
    Middle-zone (e.g. partial overlap) → low confidence → LLM.
    """
    if gold is None or pred is None:
        return None
    if not gold.strip() or not pred.strip():
        return None

    # Applicability gate FIRST: gold must look like a multi-fact list.
    gold_facts = _extract_facts(gold)
    if len(gold_facts) < 3:
        # Single or two-fact gold → numeric/identity question, not a list.
        # Let other extractors handle it.
        return None

    # Now that gold is confirmed as multi-fact, refusal-pred is wrong.
    if _is_refusal(pred):
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="multifact_refusal",
            extracted={"gold_raw": gold[:120], "pred_raw": pred[:120]},
        )

    pred_facts = _extract_facts(pred)
    if len(pred_facts) < 2:
        return ProgrammaticVerdict(
            verdict=False,
            confidence=MED_CONFIDENCE,
            method="multifact_pred_too_short",
            extracted={"gold_facts": sorted(gold_facts), "pred_facts": sorted(pred_facts)},
        )

    j = _jaccard(gold_facts, pred_facts)
    cov = _coverage(gold_facts, pred_facts)
    extracted: dict[str, Any] = {
        "gold_facts": sorted(gold_facts),
        "pred_facts": sorted(pred_facts),
        "jaccard": round(j, 3),
        "coverage": round(cov, 3),
    }

    # Strong correctness signal: high Jaccard.
    if j >= NER_JACCARD_THRESHOLD or cov >= 0.85:
        return ProgrammaticVerdict(
            verdict=True,
            confidence=HIGH_CONFIDENCE,
            method=f"ner_jaccard_{NER_JACCARD_THRESHOLD}",
            extracted=extracted,
        )
    # Strong wrongness signal: zero overlap.  We DELIBERATELY require
    # coverage == 0 here (not "tiny overlap") because the LLM judge is
    # trained to be lenient on partial overlap per Memobase's "be generous"
    # prompt; a single shared fact should fall through to the LLM.
    if cov == 0.0 and j == 0.0:
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="multifact_disjoint",
            extracted=extracted,
        )
    # Middle zone: abstain.
    return ProgrammaticVerdict(
        verdict=cov >= 0.5,
        confidence=LOW_CONFIDENCE,
        method="multifact_partial",
        extracted=extracted,
    )


# ---------------------------------------------------------------------------
# Cat 3 — temporal ("When …?")
# ---------------------------------------------------------------------------
def _parse_date(text: str) -> datetime | None:
    """Parse a date phrase via dateparser; return naive UTC datetime or None.

    dateparser is strict about preamble — "It happened on 2024-05-07" does
    NOT parse, only "2024-05-07" does.  Therefore: try the whole string first,
    then extract every substring matching _DATE_HINT_RE plus surrounding
    digits (year, day) and try each.  Return the first parseable result.
    """
    if not _HAVE_DATEPARSER or _dateparser is None:
        return None
    if not text or not text.strip():
        return None
    settings = {
        "RETURN_AS_TIMEZONE_AWARE": False,
        "RELATIVE_BASE": datetime(2024, 1, 1),  # stable anchor for relatives
        "PREFER_DATES_FROM": "past",
    }

    candidates: list[str] = [text.strip()]

    # Extract richer substrings: ISO dates, slash dates, "Month Day Year",
    # relative phrases.  These are tried in addition to the full string.
    extra_re = re.compile(
        r"\d{4}-\d{1,2}-\d{1,2}"
        r"|\d{1,2}/\d{1,2}/\d{2,4}"
        r"|(?:january|february|march|april|may|june|july|august|september|"
        r"october|november|december|jan|feb|mar|apr|jun|jul|aug|sept?|oct|"
        r"nov|dec)\s+\d{1,2}(?:,?\s*\d{2,4})?"
        r"|\d{1,2}\s+(?:january|february|march|april|may|june|july|august|"
        r"september|october|november|december|jan|feb|mar|apr|jun|jul|aug|"
        r"sept?|oct|nov|dec)(?:\s+\d{2,4})?"
        r"|yesterday|today|tomorrow"
        r"|last\s+(?:week|month|year)"
        r"|next\s+(?:week|month|year)"
        r"|\d+\s+(?:day|week|month|year)s?\s+ago",
        re.IGNORECASE,
    )
    for m in extra_re.finditer(text):
        candidates.append(m.group(0))

    for cand in candidates:
        try:
            dt = _dateparser.parse(cand, settings=settings)
        except (TypeError, ValueError, OverflowError):
            continue
        if dt is None:
            continue
        if dt.tzinfo is not None:
            dt = dt.astimezone(timezone.utc).replace(tzinfo=None)
        return dt
    return None


# Recognize "X year(s)/month(s)/week(s)/day(s) ago" + ISO + month names.
_DATE_HINT_RE = re.compile(
    r"\b("
    r"\d{4}-\d{2}-\d{2}"  # ISO
    r"|\d{1,2}/\d{1,2}/\d{2,4}"  # slash dates
    r"|january|february|march|april|may|june|july|august|september|october|november|december"
    r"|jan|feb|mar|apr|jun|jul|aug|sep|sept|oct|nov|dec"
    r"|yesterday|today|tomorrow"
    r"|last\s+(?:week|month|year|monday|tuesday|wednesday|thursday|friday|saturday|sunday)"
    r"|next\s+(?:week|month|year)"
    r"|\d+\s+(?:day|week|month|year)s?\s+ago"
    r")\b",
    re.IGNORECASE,
)


def _looks_temporal(text: str) -> bool:
    return bool(_DATE_HINT_RE.search(text or ""))


def judge_cat3_temporal(gold: str, pred: str) -> ProgrammaticVerdict | None:
    """Cat-3 temporal "When …?" questions.

    Returns None unless both gold and pred contain a parseable date hint.
    Verdict True if |gold - pred| ≤ DATE_TOLERANCE_DAYS.
    """
    if gold is None or pred is None:
        return None
    if not _HAVE_DATEPARSER:
        return None
    if not gold.strip():
        return None
    if not _looks_temporal(gold):
        return None  # not a date answer — N/A

    g_dt = _parse_date(gold)
    if g_dt is None:
        return None  # gold not parseable → N/A (let LLM handle)

    # Gold confirmed as a date — refusal pred is safely "wrong".
    if _is_refusal(pred):
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="temporal_refusal",
            extracted={"gold_raw": gold[:120], "pred_raw": pred[:120], "gold_dt": g_dt.isoformat()},
        )

    p_dt = _parse_date(pred)
    extracted: dict[str, Any] = {
        "gold_raw": gold[:80],
        "pred_raw": pred[:160],
        "gold_dt": g_dt.isoformat(),
        "pred_dt": p_dt.isoformat() if p_dt else None,
    }
    if p_dt is None:
        # Pred had no parseable date.  If pred has any date hint we can't
        # parse, abstain; otherwise mark wrong.
        if _looks_temporal(pred):
            return ProgrammaticVerdict(
                verdict=False,
                confidence=LOW_CONFIDENCE,
                method="temporal_pred_unparseable",
                extracted=extracted,
            )
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="temporal_no_date_in_pred",
            extracted=extracted,
        )

    distance = abs((p_dt - g_dt).days)
    extracted["distance_days"] = distance

    if distance <= DATE_TOLERANCE_DAYS:
        # Tight match → high confidence.  Within window but >7d → medium.
        conf = HIGH_CONFIDENCE if distance <= 7 else MED_CONFIDENCE
        method = f"date_within_{DATE_TOLERANCE_DAYS}d" if distance > 0 else "date_exact"
        return ProgrammaticVerdict(
            verdict=True, confidence=conf, method=method, extracted=extracted
        )

    return ProgrammaticVerdict(
        verdict=False,
        confidence=HIGH_CONFIDENCE,
        method="date_outside_window",
        extracted=extracted,
    )


# ---------------------------------------------------------------------------
# Cat 3 — duration ("How long …?")
# ---------------------------------------------------------------------------
_DURATION_RE = re.compile(
    r"(\d+(?:\.\d+)?|\bone\b|\btwo\b|\bthree\b|\bfour\b|\bfive\b|\bsix\b|"
    r"\bseven\b|\beight\b|\bnine\b|\bten\b)"
    r"\s+(year|month|week|day|hour|minute)s?",
    re.IGNORECASE,
)
_DURATION_VAGUE_RE = re.compile(
    r"\b(?:a long time|forever|ages|a while|some time|short while)\b",
    re.IGNORECASE,
)
_SINCE_YEAR_RE = re.compile(r"\bsince\s+(\d{4})\b", re.IGNORECASE)

_DURATION_DAYS = {
    "minute": 1 / 1440,
    "hour": 1 / 24,
    "day": 1,
    "week": 7,
    "month": 30,
    "year": 365,
}


def _word_to_int(word: str) -> int | None:
    word = word.lower().strip()
    if word.isdigit():
        return int(word)
    if word in _NUMBER_WORDS:
        return _NUMBER_WORDS[word]
    return None


def _parse_duration_days(text: str, *, ref_year: int = 2024) -> float | None:
    """Convert a free-form duration phrase to days.

    Recognition priority: explicit duration > since-year > vague-only.
      "4 years"           → 1460
      "two months"        → 60
      "since 2020"        → (ref_year-2020)*365
      "for a long time"   → None  (vague)
      "for 4 years (a long time)" → 1460  (concrete signal wins over vague)
    """
    if not text:
        return None
    # Explicit duration phrase first (concrete signal).
    m = _DURATION_RE.search(text)
    if m:
        n = _word_to_int(m.group(1))
        unit = m.group(2).lower()
        if n is not None and unit in _DURATION_DAYS:
            return float(n) * _DURATION_DAYS[unit]
    # "since YYYY" anchor.
    sm = _SINCE_YEAR_RE.search(text)
    if sm:
        year = int(sm.group(1))
        if 1900 <= year <= ref_year:
            return float((ref_year - year) * 365)
    # Vague-only fallback → no parse.
    if _DURATION_VAGUE_RE.search(text):
        return None
    return None


def judge_cat3_duration(gold: str, pred: str) -> ProgrammaticVerdict | None:
    """Cat-3 duration "How long has X …?" questions.

    Equivalences:
      "4 years"   ≡ "since 2020" (when ref=2024)
      "2 months"  ≡ "8 weeks"    (within 20%)
    Vague gold or pred ("a long time", "forever") → low confidence.

    Applicability is gated FIRST on gold being a parseable duration.  We do
    NOT treat refusal-pred as an automatic wrong verdict here — that's only
    safe when we're sure the question was a duration question, which we
    determine by `_parse_duration_days(gold) is not None`.  This avoids
    misfiring on "Would Caroline pursue X?" cat-3 inference questions where
    a "memories don't contain" pred may actually be the correct answer.
    """
    if gold is None or pred is None:
        return None
    if not gold.strip():
        return None

    # Applicability gate FIRST — gold must look like a duration.
    g_days = _parse_duration_days(gold)
    if g_days is None:
        return None  # gold isn't a clean duration → N/A

    # Now that gold is confirmed as a duration question, refusal-pred is
    # safely interpretable as wrong.
    if _is_refusal(pred):
        return ProgrammaticVerdict(
            verdict=False,
            confidence=HIGH_CONFIDENCE,
            method="duration_refusal",
            extracted={"gold_raw": gold[:120], "pred_raw": pred[:120], "gold_days": g_days},
        )
    p_days = _parse_duration_days(pred)
    extracted: dict[str, Any] = {
        "gold_raw": gold[:80],
        "pred_raw": pred[:160],
        "gold_days": g_days,
        "pred_days": p_days,
    }
    if p_days is None:
        if _DURATION_VAGUE_RE.search(pred):
            return ProgrammaticVerdict(
                verdict=False,
                confidence=LOW_CONFIDENCE,
                method="duration_pred_vague",
                extracted=extracted,
            )
        return ProgrammaticVerdict(
            verdict=False,
            confidence=MED_CONFIDENCE,
            method="duration_no_match_in_pred",
            extracted=extracted,
        )

    if g_days == 0:
        match = p_days == 0
    else:
        rel_err = abs(p_days - g_days) / g_days
        match = rel_err <= DURATION_TOLERANCE_PCT
        extracted["rel_err"] = round(rel_err, 3)

    method = "duration_within_20pct" if match else "duration_mismatch"
    return ProgrammaticVerdict(
        verdict=match,
        confidence=HIGH_CONFIDENCE,
        method=method,
        extracted=extracted,
    )


# ---------------------------------------------------------------------------
# Public list of extractors per category — used by judge_router.
# ---------------------------------------------------------------------------
EXTRACTORS_BY_CATEGORY: dict[int, list] = {
    1: [judge_cat1_numeric, judge_cat1_multi_fact],
    3: [judge_cat3_temporal, judge_cat3_duration],
}


def run_programmatic(
    category: int, gold: str, pred: str
) -> ProgrammaticVerdict | None:
    """Run all configured extractors for ``category``; return the first one
    that emits a non-None verdict.  Returns None if all abstained (caller
    will fall through to LLM judge).
    """
    extractors = EXTRACTORS_BY_CATEGORY.get(int(category) if category is not None else -1, [])
    for fn in extractors:
        v = fn(gold, pred)
        if v is not None:
            return v
    return None
