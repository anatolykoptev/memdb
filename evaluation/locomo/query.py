#!/usr/bin/env python3
"""
query.py — run every LoCoMo QA against memdb-go, capture predictions.

For each question:
  1. GET /product/search  → retrieved memory texts (top-k).
  2. (optional) POST /product/chat/complete → full generated answer.

Predictions JSON layout (dual-speaker mode, M9+):
  [
    {
      "conv_id": "...",
      "question_idx": 0,
      "question": "...",
      "gold_answer": "...",
      "evidence": ["D1:3"],
      "category": 2,
      "retrieved": [{"content": "...", "score": 0.87, "id": "...",
                     "speaker_label": "A"}, ...],
      "chat_answer": "...",
      "search_ms": 123,
      "chat_ms": 456,
      "dual_speaker": true,
      "speaker_a_memories": [...],
      "speaker_b_memories": [...],
      "merged_top_k": 20
    }, ...
  ]

When LOCOMO_DUAL_SPEAKER=false the dual-speaker fields (`speaker_*_memories`,
`merged_top_k`, `speaker_label` on retrieved items) are omitted; the rest of
the schema matches the M7/M8 single-speaker baseline.

Deterministic: QAs sorted by (conv_id, question_idx).

Category modes:
  --sample (default)         : 10 category-1 QAs from conv-26 (backward compat)
  --sample --categories=1,2,3,4,5 : 50 QAs from conv-26, 10 per category
  --full                     : all QAs from all 10 convs

Env:
  LOCOMO_CATEGORIES   comma-separated list (e.g. "1,2,3,4,5"). Overridden
                      by --categories.  Default: "1" (backward compat).
  LOCOMO_DUAL_SPEAKER true|false (default: true). Fan-out per question to
                      BOTH `<conv>__speaker_a` and `<conv>__speaker_b` user
                      stores in parallel and merge results. Set to `false`
                      (or `0`) to reproduce M7/M8 single-speaker baselines.
                      M9 Stream 1 (HARNESS-DUAL).
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import sys
import time

from pathlib import Path

import requests

# LoCoMo's raw-mode memories are short verbatim dialogue turns ("Caroline: Hey Mel!").
# Question-form queries match these at cosine ~0.15-0.30, well below the
# DefaultRelativity=0.5 threshold in memdb-go/internal/search/config.go.
# Override to 0.0 so retrieval surfaces raw turns and the harness can score
# the actual hit@k. Production clients keep the server-side defaults.
#
# Field-name inconsistency in memdb-go server:
#   /product/search         → searchRequest.Relativity  → field "relativity"
#   /product/chat/complete  → nativeChatRequest.Threshold *float64 → field "threshold"
# Both represent the same concept (cosine floor before returning memories) but
# the chat endpoint does NOT parse "relativity" — it silently drops it and falls
# back to a hardcoded 0.30 post-filter in chatSearchMemories. We must send the
# right field to each endpoint, controlled by a single env var here.
#
# Env priority: LOCOMO_RETRIEVAL_THRESHOLD → LOCOMO_SEARCH_RELATIVITY (legacy) → 0.0
_raw_threshold = os.getenv("LOCOMO_RETRIEVAL_THRESHOLD") or os.getenv("LOCOMO_SEARCH_RELATIVITY", "0.0")
LOCOMO_RETRIEVAL_THRESHOLD = float(_raw_threshold)


# M9 Stream 1 (HARNESS-DUAL): dual-speaker retrieval per question.
# When enabled (default), each question fans out to BOTH `<conv>__speaker_a`
# and `<conv>__speaker_b` user stores in parallel, merges the result lists
# (dedup by memory id, keep higher score), and presents the combined context
# to the chat endpoint with explicit `[speaker:A]` / `[speaker:B]` provenance
# markers in the system prompt.  Mirrors Memobase's reference benchmark
# implementation (see compete-research/memobase/.../memobase_search.py:71-105).
#
# Disable with `LOCOMO_DUAL_SPEAKER=false` (or `0`) to reproduce M7/M8
# single-speaker baselines.  Case-insensitive parse.
def _parse_dual_speaker_env(raw: str | None) -> bool:
    if raw is None:
        return True
    return raw.strip().lower() not in {"false", "0", "no", "off", ""}


LOCOMO_DUAL_SPEAKER = _parse_dual_speaker_env(os.getenv("LOCOMO_DUAL_SPEAKER"))


def build_headers() -> dict:
    """Auth headers from env: MEMDB_API_KEY (Bearer) or MEMDB_SERVICE_SECRET."""
    headers = {"Content-Type": "application/json"}
    if key := os.getenv("MEMDB_API_KEY"):
        headers["Authorization"] = f"Bearer {key}"
    if secret := os.getenv("MEMDB_SERVICE_SECRET"):
        headers["X-Service-Secret"] = secret
    return headers


EVAL_DIR = Path(__file__).resolve().parent
REPO_ROOT = EVAL_DIR.parent.parent
FULL_DATA = REPO_ROOT / "evaluation" / "data" / "locomo" / "locomo10.json"
SAMPLE_GOLD = EVAL_DIR / "sample_gold.json"

# LoCoMo category constants
ALL_CATEGORIES = {1, 2, 3, 4, 5}
# How many QAs to sample per category when building a balanced sample
CATEGORY_SAMPLE_SIZE = 10


def parse_categories(raw: str | None) -> list[int]:
    """Parse a comma-separated category string like '1,2,3,4,5' → [1, 2, 3, 4, 5].

    Returns the parsed list or raises ValueError on bad input.
    """
    if not raw:
        return [1]
    parts = [p.strip() for p in raw.split(",") if p.strip()]
    cats: list[int] = []
    for part in parts:
        try:
            c = int(part)
        except ValueError:
            raise ValueError(f"Invalid category {part!r} — must be an integer 1-5.") from None
        if c not in ALL_CATEGORIES:
            raise ValueError(f"Category {c} out of range — valid: 1-5.")
        if c not in cats:
            cats.append(c)
    return sorted(cats)


def load_gold(path: Path) -> list[dict]:
    with path.open() as f:
        return json.load(f)


def load_gold_from_locomo10(path: Path, categories: list[int] | None = None) -> list[dict]:
    """Load QAs from locomo10.json, optionally filtered to specific categories.

    When categories is None or [1,2,3,4,5], all QAs with a valid answer are
    returned.  When a subset is given (e.g. [1]), only QAs of those categories
    are included.

    Category 5 (adversarial) uses 'adversarial_answer' instead of 'answer'.
    Both fields are normalised to 'answer' in the returned records.
    """
    with path.open() as f:
        data = json.load(f)
    cat_filter = set(categories) if categories else None
    out: list[dict] = []
    for conv in data:
        sample_id = conv.get("sample_id", "locomo_unknown")
        raw_qas = conv.get("qa", [])
        qas_by_cat: dict[int, list[dict]] = {}
        for qa in raw_qas:
            cat = qa.get("category")
            if cat_filter and cat not in cat_filter:
                continue
            # Normalise answer field: category 5 uses 'adversarial_answer'
            answer = qa.get("answer") or qa.get("adversarial_answer") or ""
            if answer == "":
                continue
            qas_by_cat.setdefault(cat, []).append(
                {
                    "question": qa["question"],
                    "answer": answer,
                    "evidence": qa.get("evidence", []),
                    "category": cat,
                }
            )
        # Sort within each category for determinism; preserve category order
        q_idx = 0
        for cat in sorted(qas_by_cat.keys()):
            cat_qas = sorted(qas_by_cat[cat], key=lambda q: q["question"])
            for qa in cat_qas:
                out.append(
                    {
                        "conv_id": sample_id,
                        "question_idx": q_idx,
                        "question": qa["question"],
                        "answer": qa["answer"],
                        "evidence": qa["evidence"],
                        "category": qa["category"],
                    }
                )
                q_idx += 1
    return out


def build_sample_gold(
    path: Path, categories: list[int], per_cat: int = CATEGORY_SAMPLE_SIZE
) -> list[dict]:
    """Build a balanced sample from locomo10.json: up to per_cat QAs per category.

    Uses conv-26 (first conversation) as the fixed sample conversation so
    the ingest step only needs to push a single conversation.

    Deterministic: QAs within each category sorted alphabetically by question.
    """
    with path.open() as f:
        data = json.load(f)
    # Find conv-26 (first conversation in the dataset)
    conv = data[0]
    sample_id = conv.get("sample_id", "locomo_unknown")
    raw_qas = conv.get("qa", [])

    qas_by_cat: dict[int, list[dict]] = {}
    for qa in raw_qas:
        cat = qa.get("category")
        if cat not in set(categories):
            continue
        answer = qa.get("answer") or qa.get("adversarial_answer") or ""
        if answer == "":
            continue
        qas_by_cat.setdefault(cat, []).append(
            {
                "question": qa["question"],
                "answer": answer,
                "evidence": qa.get("evidence", []),
                "category": cat,
            }
        )

    out: list[dict] = []
    q_idx = 0
    for cat in sorted(qas_by_cat.keys()):
        cat_qas = sorted(qas_by_cat[cat], key=lambda q: q["question"])[:per_cat]
        for qa in cat_qas:
            out.append(
                {
                    "conv_id": sample_id,
                    "question_idx": q_idx,
                    "question": qa["question"],
                    "answer": qa["answer"],
                    "evidence": qa["evidence"],
                    "category": qa["category"],
                }
            )
            q_idx += 1
    return out


def extract_memory_items(payload) -> list[dict]:
    """Normalize /product/search response → list of {content, score, id}.

    memdb-go layout:
      data.text_mem[].memories[].memory
      data.pref_mem[].memories[].memory
      data.skill_mem[].memories[].memory
      data.tool_mem[].memories[].memory
    Also tolerant of older/flat layouts.
    """
    if not isinstance(payload, dict):
        return []
    data = payload.get("data", payload)
    items: list[dict] = []

    def pull_memory(m, memory_type):
        if not isinstance(m, dict):
            return
        metadata = m.get("metadata") or {}
        content = (
            m.get("memory")
            or metadata.get("memory")
            or metadata.get("context_summary")
            or m.get("content")
            or m.get("text")
            or ""
        )
        score = m.get("score") or m.get("relativity") or metadata.get("relativity")
        items.append(
            {
                "content": content,
                "score": score,
                "id": m.get("id") or m.get("memory_id") or metadata.get("id") or "",
                "type": memory_type,
            }
        )

    if isinstance(data, dict):
        for key in ("text_mem", "pref_mem", "skill_mem", "tool_mem"):
            buckets = data.get(key)
            if not isinstance(buckets, list):
                continue
            for bucket in buckets:
                if not isinstance(bucket, dict):
                    continue
                for m in bucket.get("memories") or []:
                    pull_memory(m, key)
        # flat fallbacks
        for key in (
            "long_term_memory",
            "user_memory",
            "working_memory",
            "memory",
            "memories",
            "results",
        ):
            val = data.get(key)
            if isinstance(val, list):
                for m in val:
                    pull_memory(m, key)
    elif isinstance(data, list):
        for m in data:
            pull_memory(m, "results")
    return items


def query_search(
    memdb_url: str,
    user_id: str,
    query: str,
    top_k: int,
    session_id: str | None,
    timeout: int = 60,
) -> tuple[list[dict], int]:
    payload = {
        "user_id": user_id,
        "query": query,
        "top_k": top_k,
        "mode": "fast",
        "include_preference": False,
        "search_tool_memory": False,
        "include_skill_memory": False,
        "relativity": LOCOMO_RETRIEVAL_THRESHOLD,  # searchRequest.Relativity field
    }
    if session_id:
        payload["session_id"] = session_id
    start = time.time()
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/search",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    elapsed_ms = int((time.time() - start) * 1000)
    resp.raise_for_status()
    items = extract_memory_items(resp.json())
    return items, elapsed_ms


def query_chat(
    memdb_url: str,
    user_id: str,
    query: str,
    top_k: int,
    timeout: int = 120,
) -> tuple[str, int]:
    payload = {
        "user_id": user_id,
        "query": query,
        "top_k": top_k,
        "mode": "fast",
        "include_preference": False,
        "threshold": LOCOMO_RETRIEVAL_THRESHOLD,  # nativeChatRequest.Threshold field (NOT "relativity")
        # Use server-side factual QA prompt (Stream A: answer_style=factual in buildSystemPrompt).
        # This exercises the production code path instead of the harness-side QA_SYSTEM_PROMPT
        # override that was used during M6 ablation experiments.
        "answer_style": "factual",
    }
    start = time.time()
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/chat/complete",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    elapsed_ms = int((time.time() - start) * 1000)
    resp.raise_for_status()
    body = resp.json()
    # memdb-go wraps body as {"code", "message", "data"}; data can be str or dict
    if isinstance(body, dict):
        data = body.get("data", body)
        if isinstance(data, str):
            return data, elapsed_ms
        if isinstance(data, dict):
            for key in ("response", "answer", "content", "message", "text"):
                val = data.get(key)
                if isinstance(val, str) and val:
                    return val, elapsed_ms
    return json.dumps(body)[:2000], elapsed_ms


# --- M9 Stream 1: dual-speaker retrieval helpers -------------------------------

def _speaker_user_ids(conv_id: str) -> tuple[str, str]:
    """Return `(speaker_a_uid, speaker_b_uid)` for a LoCoMo conv id."""
    return f"{conv_id}__speaker_a", f"{conv_id}__speaker_b"


def _merge_dual_results(
    speaker_a_items: list[dict],
    speaker_b_items: list[dict],
    top_k: int,
) -> list[dict]:
    """Merge dual-speaker search results.

    Rules:
      * Dedup by memory `id`.  When the same id appears in both speakers'
        lists, keep the higher `score` and remember which speaker contributed
        the winning copy via `speaker_label` ("A" or "B").
      * Items with empty/None id are kept as distinct entries (best-effort
        — no reasonable dedup key available); they still get `speaker_label`.
      * Each merged item carries `speaker_label` provenance.
      * Result is sorted by score (descending; missing/None score → 0.0)
        and truncated to `top_k`.
    """
    by_id: dict[str, dict] = {}
    no_id: list[dict] = []

    def _ingest(items: list[dict], label: str) -> None:
        for it in items:
            tagged = dict(it)
            tagged["speaker_label"] = label
            mid = tagged.get("id") or ""
            if not mid:
                no_id.append(tagged)
                continue
            existing = by_id.get(mid)
            if existing is None:
                by_id[mid] = tagged
                continue
            # Higher score wins; treat None as 0.0 for comparison.
            existing_score = existing.get("score") or 0.0
            new_score = tagged.get("score") or 0.0
            if new_score > existing_score:
                by_id[mid] = tagged

    _ingest(speaker_a_items, "A")
    _ingest(speaker_b_items, "B")

    merged = list(by_id.values()) + no_id
    merged.sort(key=lambda m: (m.get("score") or 0.0), reverse=True)
    return merged[:top_k]


def query_search_dual(
    memdb_url: str,
    conv_id: str,
    query: str,
    top_k: int,
    session_id: str | None = None,
    timeout: int = 60,
) -> tuple[list[dict], list[dict], list[dict], int]:
    """Dual-speaker fan-out of `query_search`.

    Issues two parallel `/product/search` calls — one for `<conv>__speaker_a`
    and one for `<conv>__speaker_b` — using a 2-worker thread pool, then
    returns `(speaker_a_items, speaker_b_items, merged_items, elapsed_ms)`.
    The merged list is bounded to `top_k` by `_merge_dual_results`.

    Network errors propagate (caller handles `requests.RequestException`).
    """
    uid_a, uid_b = _speaker_user_ids(conv_id)
    start = time.time()
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        fut_a = pool.submit(
            query_search, memdb_url, uid_a, query, top_k, session_id, timeout
        )
        fut_b = pool.submit(
            query_search, memdb_url, uid_b, query, top_k, session_id, timeout
        )
        items_a, _ = fut_a.result()
        items_b, _ = fut_b.result()
    elapsed_ms = int((time.time() - start) * 1000)
    merged = _merge_dual_results(items_a, items_b, top_k)
    return items_a, items_b, merged, elapsed_ms


def _build_dual_speaker_system_prompt(
    speaker_a_items: list[dict],
    speaker_b_items: list[dict],
    query: str,
) -> str:
    """Assemble a system prompt that exposes both speakers' memories.

    Mirrors Memobase's `answer_question` template (memobase_search.py:81-88):
    each speaker's memories are presented as a labelled block so the model
    cannot "miss" cross-speaker evidence.  The current date placeholder
    matches the server-side `factualQAPromptEN` style.
    """
    def _fmt(items: list[dict], label: str) -> str:
        if not items:
            return f"[speaker:{label}] (no memories retrieved)"
        lines = [f"[speaker:{label}]"]
        for i, it in enumerate(items, 1):
            content = (it.get("content") or "").strip()
            if not content:
                continue
            lines.append(f"{i}. {content}")
        return "\n".join(lines)

    now = time.strftime("%Y-%m-%d %H:%M (%A)")
    block_a = _fmt(speaker_a_items, "A")
    block_b = _fmt(speaker_b_items, "B")
    return (
        "You are a factual QA assistant answering a question about a recorded "
        "two-speaker conversation.  Below are memories retrieved separately "
        "from each speaker's personal memory store; treat both speakers' "
        "evidence as equally authoritative and combine across speakers when "
        "the question requires it.\n\n"
        f"Current time: {now}\n\n"
        "## Memories\n"
        f"{block_a}\n\n"
        f"{block_b}\n\n"
        "Answer the user's question with a short, direct factual response. "
        "If the memories do not contain the answer, say so plainly."
    )


def query_chat_dual(
    memdb_url: str,
    conv_id: str,
    query: str,
    top_k: int,
    speaker_a_items: list[dict] | None = None,
    speaker_b_items: list[dict] | None = None,
    timeout: int = 120,
) -> tuple[str, list[dict], list[dict], int]:
    """Dual-speaker chat: pass both speakers' memories to `/product/chat/complete`.

    If `speaker_a_items` / `speaker_b_items` are provided (typical caller
    path — they come from a prior `query_search_dual` call), they are reused
    verbatim to avoid duplicate retrieval.  Otherwise, a fresh dual fan-out
    runs first.

    Builds an explicit `[speaker:A]`/`[speaker:B]` system prompt that
    overrides the server's default `answer_style=factual` template and
    minimises server-side retrieval noise by setting `top_k=1` for the
    chat call (the model relies on our pre-fetched dual-speaker context).

    Returns `(answer, speaker_a_items, speaker_b_items, elapsed_ms)`.
    """
    if speaker_a_items is None or speaker_b_items is None:
        speaker_a_items, speaker_b_items, _, _ = query_search_dual(
            memdb_url, conv_id, query, top_k, session_id=None, timeout=timeout
        )

    system_prompt = _build_dual_speaker_system_prompt(
        speaker_a_items, speaker_b_items, query
    )

    # The chat endpoint requires a user_id; pick speaker_a deterministically.
    # Server-side retrieval is suppressed with top_k=1 + a high threshold —
    # the model's primary context comes from `system_prompt`.
    uid_a, _ = _speaker_user_ids(conv_id)
    payload = {
        "user_id": uid_a,
        "query": query,
        "top_k": 1,
        "mode": "fast",
        "include_preference": False,
        "threshold": 0.99,
        "system_prompt": system_prompt,
    }
    start = time.time()
    resp = requests.post(
        f"{memdb_url.rstrip('/')}/product/chat/complete",
        json=payload,
        headers=build_headers(),
        timeout=timeout,
    )
    elapsed_ms = int((time.time() - start) * 1000)
    resp.raise_for_status()
    body = resp.json()
    answer = ""
    if isinstance(body, dict):
        data = body.get("data", body)
        if isinstance(data, str):
            answer = data
        elif isinstance(data, dict):
            for key in ("response", "answer", "content", "message", "text"):
                val = data.get(key)
                if isinstance(val, str) and val:
                    answer = val
                    break
    if not answer:
        answer = json.dumps(body)[:2000]
    return answer, speaker_a_items, speaker_b_items, elapsed_ms


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    g = p.add_mutually_exclusive_group(required=True)
    g.add_argument("--sample", action="store_true")
    g.add_argument("--full", action="store_true")
    g.add_argument("--gold", type=Path, help="Custom gold JSON.")
    p.add_argument("--memdb-url", default="http://localhost:8080")
    p.add_argument("--top-k", type=int, default=20)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument(
        "--skip-chat",
        action="store_true",
        help="Only call /product/search, skip /product/chat/complete.",
    )
    p.add_argument(
        "--speaker",
        choices=["a", "b"],
        default="a",
        help="Which speaker user ID to query as (stable across runs).",
    )
    p.add_argument(
        "--categories",
        default=os.getenv("LOCOMO_CATEGORIES", ""),
        help=(
            "Comma-separated LoCoMo QA categories to include (default: '1', "
            "backward-compat single-hop only). Use '1,2,3,4,5' for the full "
            "5-category 50-QA sample. Env: LOCOMO_CATEGORIES."
        ),
    )
    args = p.parse_args()

    try:
        categories = parse_categories(args.categories)
    except ValueError as exc:
        print(f"ERROR: --categories: {exc}", file=sys.stderr)
        return 2

    multi_cat = len(categories) > 1
    cat_label = ",".join(str(c) for c in categories)

    if args.sample:
        if multi_cat:
            # Build balanced sample on-the-fly from locomo10.json
            gold = build_sample_gold(FULL_DATA, categories)
            print(
                f"[query] 5-category mode: {len(gold)} QAs from conv-26 "
                f"(categories {cat_label}, {CATEGORY_SAMPLE_SIZE} each)",
                flush=True,
            )
        else:
            # Default backward-compat path: use committed sample_gold.json
            gold = load_gold(SAMPLE_GOLD)
            print(f"[query] sample mode: {len(gold)} QAs (category-1 only)", flush=True)
    elif args.full:
        gold = load_gold_from_locomo10(FULL_DATA, categories if multi_cat else None)
        print(f"[query] full mode: {len(gold)} QAs (categories {cat_label})", flush=True)
    else:
        gold = load_gold(args.gold)
        print(f"[query] custom gold: {len(gold)} QAs from {args.gold}", flush=True)

    gold.sort(key=lambda g: (g["conv_id"], g["question_idx"]))

    dual_speaker = LOCOMO_DUAL_SPEAKER
    print(
        f"[query] dual_speaker={dual_speaker} "
        f"(env LOCOMO_DUAL_SPEAKER={os.getenv('LOCOMO_DUAL_SPEAKER', '<unset>')!r})",
        flush=True,
    )

    predictions: list[dict] = []
    errors: list[str] = []
    for qi, qa in enumerate(gold, 1):
        question = qa["question"]
        print(f"[{qi}/{len(gold)}] {qa['conv_id']} q={question[:70]!r}", flush=True)
        rec = {
            "conv_id": qa["conv_id"],
            "question_idx": qa["question_idx"],
            "question": question,
            "gold_answer": qa["answer"],
            "evidence": qa.get("evidence", []),
            "category": qa.get("category"),
            "retrieved": [],
            "chat_answer": None,
            "search_ms": None,
            "chat_ms": None,
            "error": None,
            "dual_speaker": dual_speaker,
        }

        if dual_speaker:
            speaker_a_items: list[dict] = []
            speaker_b_items: list[dict] = []
            try:
                speaker_a_items, speaker_b_items, merged, ms = query_search_dual(
                    args.memdb_url,
                    qa["conv_id"],
                    question,
                    args.top_k,
                    session_id=None,
                )
                rec["retrieved"] = merged
                rec["search_ms"] = ms
                rec["speaker_a_memories"] = speaker_a_items
                rec["speaker_b_memories"] = speaker_b_items
                rec["merged_top_k"] = len(merged)
            except requests.RequestException as exc:
                rec["error"] = f"search: {exc}"
                errors.append(rec["error"])
                print(f"  SEARCH ERROR: {exc}", file=sys.stderr, flush=True)

            if not args.skip_chat and rec["error"] is None:
                try:
                    answer, _, _, ms = query_chat_dual(
                        args.memdb_url,
                        qa["conv_id"],
                        question,
                        args.top_k,
                        speaker_a_items=speaker_a_items,
                        speaker_b_items=speaker_b_items,
                    )
                    rec["chat_answer"] = answer
                    rec["chat_ms"] = ms
                except requests.RequestException as exc:
                    rec["error"] = f"chat: {exc}"
                    errors.append(rec["error"])
                    print(f"  CHAT ERROR: {exc}", file=sys.stderr, flush=True)
        else:
            user_id = f"{qa['conv_id']}__speaker_{args.speaker}"
            try:
                items, ms = query_search(
                    args.memdb_url,
                    user_id,
                    question,
                    args.top_k,
                    session_id=None,
                )
                rec["retrieved"] = items
                rec["search_ms"] = ms
            except requests.RequestException as exc:
                rec["error"] = f"search: {exc}"
                errors.append(rec["error"])
                print(f"  SEARCH ERROR: {exc}", file=sys.stderr, flush=True)

            if not args.skip_chat and rec["error"] is None:
                try:
                    answer, ms = query_chat(args.memdb_url, user_id, question, args.top_k)
                    rec["chat_answer"] = answer
                    rec["chat_ms"] = ms
                except requests.RequestException as exc:
                    rec["error"] = f"chat: {exc}"
                    errors.append(rec["error"])
                    print(f"  CHAT ERROR: {exc}", file=sys.stderr, flush=True)

        predictions.append(rec)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w") as f:
        json.dump(
            {
                "predictions": predictions,
                "meta": {
                    "memdb_url": args.memdb_url,
                    "top_k": args.top_k,
                    "speaker": args.speaker,
                    "total": len(gold),
                    "errors": len(errors),
                    "skip_chat": args.skip_chat,
                    "categories": categories,
                    "dual_speaker": dual_speaker,
                },
            },
            f,
            indent=2,
            ensure_ascii=False,
        )
    print(f"wrote {len(predictions)} predictions → {args.out}")
    return 0 if not errors else 1


if __name__ == "__main__":
    sys.exit(main())
