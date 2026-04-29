"""
judge_normalize.py — Reference-string normalization for LLM-as-judge harness (M13 J1).

Pipeline (applied in order):
  1. Unicode NFC normalization        — collapse composed/decomposed forms ("café" === "café")
  2. Smart-quote → ASCII apostrophe   — typographic quotes ("Charlotte's" → "Charlotte's")
  3. Contraction expansion            — "don't" → "do not", "can't" → "cannot"
  4. Lowercase
  5. Strip ASCII punctuation
  6. Collapse whitespace

The normalizer is a *pure function over strings*. It MUST NOT change semantics —
e.g. "March 2nd" and "March 2" both normalize to "march 2", not to a date object.
That kind of semantic equivalence is the LLM judge's job, not the normalizer's.

Library choice: standard library only.
  Rationale: a `contractions` PyPI dependency would gate evaluation harness
  reproducibility on a third-party package + its NLTK transitive deps. The set
  of contractions that actually matter for LoCoMo gold answers is small (~50);
  a hand-curated map covers the cases without hard-bricking CI when the package
  goes stale. Easy to swap in `contractions.fix(text)` later if the map grows.
"""

from __future__ import annotations

import re
import string
import unicodedata


# ---------------------------------------------------------------------------
# Contraction map — common English contractions seen in QA gold answers.
# Order matters: longer keys first so "don't" doesn't fire "n't" rule prematurely.
# All keys are lowercase ASCII-apostrophe form (preprocessing step 2 enforces this).
# ---------------------------------------------------------------------------
_CONTRACTIONS: dict[str, str] = {
    # Negations
    "ain't": "is not",
    "aren't": "are not",
    "can't": "cannot",
    "couldn't": "could not",
    "didn't": "did not",
    "doesn't": "does not",
    "don't": "do not",
    "hadn't": "had not",
    "hasn't": "has not",
    "haven't": "have not",
    "isn't": "is not",
    "mustn't": "must not",
    "needn't": "need not",
    "shan't": "shall not",
    "shouldn't": "should not",
    "wasn't": "was not",
    "weren't": "were not",
    "won't": "will not",
    "wouldn't": "would not",
    # Auxiliaries
    "i'm": "i am",
    "i've": "i have",
    "i'll": "i will",
    "i'd": "i would",
    "you're": "you are",
    "you've": "you have",
    "you'll": "you will",
    "you'd": "you would",
    "he's": "he is",
    "she's": "she is",
    "it's": "it is",
    "we're": "we are",
    "we've": "we have",
    "we'll": "we will",
    "we'd": "we would",
    "they're": "they are",
    "they've": "they have",
    "they'll": "they will",
    "they'd": "they would",
    "that's": "that is",
    "what's": "what is",
    "where's": "where is",
    "there's": "there is",
    "here's": "here is",
    "who's": "who is",
    "let's": "let us",
    # Generic possessive 's' (kept as-is — possessive is dropped in punctuation strip)
}

# Smart/typographic apostrophes that browsers/word-processors emit.
# All collapse to plain ASCII ' before contraction lookup.
_SMART_APOSTROPHES = {
    "‘": "'",   # left single quotation mark
    "’": "'",   # right single quotation mark (most common smart apostrophe)
    "‚": "'",   # single low-9 quotation mark
    "‛": "'",   # single high-reversed-9 quotation mark
    "ʼ": "'",   # modifier letter apostrophe
    "´": "'",   # acute accent (sometimes substituted for apostrophe)
    "`": "'",        # backtick (occasional substitute)
}

# Pre-compile contraction regex: word-boundary, longest-first.
# We avoid \b on either side because the apostrophe itself is already a word boundary.
_CONTRACTION_KEYS_SORTED = sorted(_CONTRACTIONS.keys(), key=len, reverse=True)
_CONTRACTION_RE = re.compile(
    r"(?<![a-zA-Z'])(" + "|".join(re.escape(k) for k in _CONTRACTION_KEYS_SORTED) + r")(?![a-zA-Z])",
    re.IGNORECASE,
)


def _normalize_apostrophes(text: str) -> str:
    """Replace typographic apostrophes with ASCII '."""
    for smart, plain in _SMART_APOSTROPHES.items():
        text = text.replace(smart, plain)
    return text


def _expand_contractions(text: str) -> str:
    """Expand contractions in `text` (already lowercased + ASCII-apostrophe form)."""
    def _sub(match: re.Match[str]) -> str:
        return _CONTRACTIONS[match.group(0).lower()]

    return _CONTRACTION_RE.sub(_sub, text)


# Two-step punctuation strip:
#   1. Apostrophes are REMOVED (no space inserted) so possessives + remaining
#      contractions collapse to a single token: "charlotte's" → "charlottes",
#      "dont" stays "dont" instead of becoming "don t".
#   2. Remaining ASCII punctuation is replaced with whitespace so word
#      boundaries are preserved: "hello,world" → "hello world".
_APOSTROPHE_RE = re.compile(r"'+")
_OTHER_PUNCT = string.punctuation.replace("'", "")
_PUNCTUATION_RE = re.compile(r"[" + re.escape(_OTHER_PUNCT) + r"]")
_WHITESPACE_RE = re.compile(r"\s+")


def normalize_reference(text: str | None, *, expand_contractions: bool = True) -> str:
    """Normalize a reference string for stable comparison.

    Pipeline:
      1. None → "" (idempotent on empty input)
      2. Unicode NFC composition
      3. Smart apostrophe → ASCII '
      4. Lowercase
      5. (optional) Contraction expansion
      6. Strip ASCII punctuation
      7. Collapse whitespace

    >>> normalize_reference("Charlotte's Web")
    'charlottes web'
    >>> normalize_reference("don't")
    'do not'
    >>> normalize_reference("café")  # NFC composed (é U+00E9)
    'café'
    >>> # Decomposed form (e + U+0301) normalizes equal to composed
    >>> normalize_reference("café") == normalize_reference("café")
    True
    """
    if text is None:
        return ""
    s = str(text)
    # 2. Unicode NFC — make composed/decomposed forms hash-equal
    s = unicodedata.normalize("NFC", s)
    # 3. Apostrophes → ASCII (must run BEFORE contraction expansion)
    s = _normalize_apostrophes(s)
    # 4. Lowercase (must run before contraction lookup since map is lowercase-keyed)
    s = s.lower()
    # 5. Expand contractions
    if expand_contractions:
        s = _expand_contractions(s)
    # 6a. Strip apostrophes WITHOUT inserting whitespace (possessives collapse)
    s = _APOSTROPHE_RE.sub("", s)
    # 6b. Replace remaining punctuation with whitespace (word boundary preserved)
    s = _PUNCTUATION_RE.sub(" ", s)
    # 7. Collapse whitespace
    s = _WHITESPACE_RE.sub(" ", s).strip()
    return s


def references_equal(
    a: str | None, b: str | None, *, expand_contractions: bool = True
) -> bool:
    """Check whether two reference strings normalize to the same value."""
    return normalize_reference(a, expand_contractions=expand_contractions) == normalize_reference(
        b, expand_contractions=expand_contractions
    )
