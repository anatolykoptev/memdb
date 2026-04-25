# Memobase Deep Dive — How They Hit 75.78% LoCoMo
> Researched 2026-04-26 via go-code MCP on `/home/krolik/src/compete-research/memobase`. Memobase v0.0.37 published 75.78% LLM Judge overall, the public top — above MemOS 73.31, Mem0 66.88, Zep* 75.14 (self-reported).
>
> **TL;DR (apples-to-apples honesty first):** Memobase's headline 75.78% uses **LLM Judge** (binary correct/incorrect by GPT-4o), not F1. They also **exclude cat-5 adversarial entirely** (`exclude_category={5}`, `memobase_search.py:147`). MemDB's M7 result of 0.238 F1 on a different metric is **not directly comparable** to 75.78%. Before any port-target conclusions: if we adopt their measurement (LLM Judge + cat-5 excluded), our published numbers will look meaningfully higher with no model change.

---

## Part 1 — What they measure

From `docs/experiments/locomo-benchmark/README.md`:

| Method | Single-Hop | Multi-Hop | Open Domain | Temporal | Overall |
|--------|-----------|-----------|-------------|----------|---------|
| Mem0 | 67.13 | 51.15 | 72.93 | 55.51 | 66.88 |
| Mem0-Graph | 65.71 | 47.19 | 75.71 | 58.13 | 68.44 |
| LangMem | 62.23 | 47.92 | 71.12 | 23.43 | 58.10 |
| Zep | 61.70 | 41.35 | 76.60 | 49.31 | 65.99 |
| OpenAI | 63.79 | 42.92 | 62.29 | 21.71 | 52.90 |
| Memobase v0.0.32 | 63.83 | **52.08** | 71.82 | 80.37 | 70.91 |
| **Memobase v0.0.37** | **70.92** | **46.88 ↓** | **77.17** | **85.05** | **75.78** |

**Key observations**:
1. **Memobase v0.0.37 LOST on multi-hop** (52.08 → 46.88) but won overall via single-hop + open-domain + temporal gains. Multi-hop is NOT their strength.
2. **Memobase temporal 85.05** is the public best — 5+ pp above next-best.
3. **`exclude_category={5}`** in `memobase_search.py:147` — cat-5 adversarial dropped from the table. Their averages don't include it.
4. Metric is **LLM Judge** (`fixture/.../*_eval_*.json`), not F1. Binary "1 if matches gold else 0" by GPT-4o.

For us this means:
- We currently report F1, which is much stricter than LLM Judge for the same answer quality.
- We currently include cat-5 in our aggregate, which Memobase doesn't.
- A direct port of Memobase's measurement methodology to our harness would lift our published number even before any architectural change.

---

## Part 2 — Architecture: what makes Memobase Memobase

### 2.1 Dual-speaker ingestion, dual-speaker retrieval (THE killer trick)

`memobase_add.py:65-150` and `memobase_search.py:73-79`:

```python
# Ingest each conversation TWICE — once per speaker, with role-swapped messages
speaker_a_user_id = f"{speaker_a}_{idx}"
speaker_b_user_id = f"{speaker_b}_{idx}"
# ... thread_a + thread_b run in parallel, each ingests the SAME conversation but
#     with the messages role-swapped (speaker_b's lines become "user", a's become "assistant"
#     in speaker_b's profile, vice versa).

# At answer time, query BOTH user stores and pack BOTH contexts into the answer prompt:
def answer_question(self, speaker_1_user_id, speaker_2_user_id, question, ...):
    speaker_1_memories, _ = self.search_memory(speaker_1_user_id, question)
    speaker_2_memories, _ = self.search_memory(speaker_2_user_id, question)
    # template renders BOTH memory blocks into the system prompt
```

**This directly addresses our M7 cat-5 finding**: 32% of our cat-5 errors were "attribution suppressions" (model rejecting cross-speaker evidence). Memobase sidesteps the attribution question entirely by giving the model BOTH speakers' perspectives explicitly.

**Note**: this is part of the BENCHMARK harness (`memobase_client/`), not the server. Memobase's server is single-user-id — the dual-speaker pattern is something the harness orchestrates by storing two profiles per conversation.

### 2.2 Structured per-user profile with topic taxonomy

`prompts/extract_profile.py` and `prompts/user_profile_topics.py`:

Each ingest fires an LLM extract that maps facts into a `topic / sub_topic / memo` triple:

```
- basic_info\tname\tmelinda
- work\ttitle\tsoftware engineer
- interest\tmovie\tInception, Interstellar [mention 2025/01/01]
```

Topic taxonomy is bounded — operator can pin "valid topics" via `Topics Guidelines` in the prompt. New extractions either re-use existing topics OR create new ones (gated by `strict_mode` flag). Multi-LLM-call pipeline:

1. **Summarize** chat batch → `prompts/summary_entry_chats.py`
2. **Extract profile** facts from summary → `prompts/extract_profile.py`
3. **Pick related profiles** from existing user profile → `prompts/pick_related_profiles.py`
4. **Merge** new facts into existing profile → `prompts/merge_profile.py` (or `_yolo` variant)
5. **Organize** profile into clean form → `prompts/organize_profile.py`
6. **Tag events** with categories → `prompts/event_tagging.py`

This is a **structured user profile layer** sitting ABOVE raw memories. MemDB has no equivalent — we have raw memories + edges + preferences (D8) but no `User Profile` first-class object.

### 2.3 Time-anchoring via `[mention DATE]` tags

In every extracted memo: `Inception, Interstellar [mention 2025/01/01]; favorite movie is Tenet [mention 2025/01/02]`.

Why: when the user asks "what was my favorite movie last January?", the LLM sees the `[mention DATE]` directly in the retrieved memo and can resolve the temporal answer without needing a separate temporal index.

Combined with the extract_profile prompt instruction:
> "If the user mentions time-sensitive information, try to infer the specific date from the data. Use specific dates when possible, never use relative dates like 'today' or 'yesterday'."

This is **why their temporal score is 85.05 (the public best)**.

### 2.4 Context-pack injection at answer time

`prompts/chat_context_pack.py`:

```python
def en_context_prompt(profile_section: str, event_section: str) -> str:
    return f"""---
# Memory
Unless the user has relevant queries, do not actively mention those memories.
## User Current Profile:
{profile_section}

## Past Events:
{event_section}
---
"""
```

Two distinct sections — **structured profile** (the `topic/sub_topic/memo` block) AND **temporal events** (raw event log). Both injected into system prompt.

We do something similar with `factualQAPromptEN` + `<memories>` block, but only one section — raw memories. We don't have a separate "user profile" block to inject.

### 2.5 Search params

```python
memories = u.context(
    max_token_size=3000,                # cap context window
    chats=[{"role": "user", "content": query}],
    event_similarity_threshold=0.2,     # low cosine threshold (we use 0.0 in eval)
    fill_window_with_events=True,       # pack window full
)
```

Their threshold 0.2 is conservative-ish; our `LOCOMO_RETRIEVAL_THRESHOLD=0.0` is more permissive. They cap context at 3000 tokens; we don't have a similar cap currently.

### 2.6 Concurrency

Both ingest and search use `ThreadPoolExecutor(max_workers=10)`. Their server handles dual-thread ingest per conversation. Not architecturally novel but worth noting their throughput posture.

---

## Part 3 — Direct mapping: their solutions to our problems

| Our problem | M7 number | Memobase solution | Expected lift |
|-------------|-----------|--------------------|----------------|
| **cat-5 adversarial F1=0.092** (32% attribution suppressions per S7 analysis) | 0.092 | **Dual-speaker retrieval** — query BOTH speaker stores, pack both into LLM prompt | High (closes attribution-suppression class) |
| **cat-3 temporal F1=0.201** | 0.201 | `[mention DATE]` tags in memo + "use specific dates not relative" prompt instruction | High (Memobase 85.05 is the public best) |
| **cat-1 single-hop F1=0.267** | 0.267 | Structured profile lookup: "What's user's job?" → `work/title` direct hit | Medium-High (Memobase 70.92 is best) |
| **cat-2 multi-hop F1=0.091** | 0.091 | Memobase has NO answer here — they regressed to 46.88. Look at **Zep* 66.04** (graph traversal) instead | None from Memobase |
| **cat-4 open-domain F1=0.407** | 0.407 | Profile context injection — "what does user think about X" answered from structured profile | Medium |

---

## Part 4 — Ranked port targets (M9 candidates)

### 🥇 #1 — Dual-speaker retrieval in LoCoMo harness
**Effort**: S (harness-only change, no server changes needed)

**What**: modify `evaluation/locomo/query.py` to:
1. Accept conv_id + question
2. Run TWO `/product/search` queries — one per speaker (`{conv_id}__speaker_a`, `{conv_id}__speaker_b`)
3. Concatenate retrieved memories
4. Pass merged context to chat endpoint with explicit speaker labels

**Why first**: cheapest, addresses S7 finding (32% attribution suppressions), Memobase confirms this works. Pure harness change — no server diff.

**Risk**: 2× search calls per question doubles benchmark wall-time. Mitigation: parallelise via asyncio (Memobase uses ThreadPoolExecutor for similar reason).

### 🥈 #2 — `[mention DATE]` tags + temporal-strict prompt
**Effort**: M

**What**: 
1. Modify our `add_fine.go` LLM extraction prompt to require `[mention <ISO_DATE>]` tag whenever a fact references time
2. Add prompt instruction: "use specific ISO dates, never relative dates like 'today'"
3. Verify retrieved memories carry through to chat prompt

**Why second**: closes our cat-3 gap (current 0.201 vs Memobase 0.85). High impact, moderate effort. Architecture-compatible — just enriches existing memory text.

### 🥉 #3 — Add LLM Judge metric to our harness (apples-to-apples)
**Effort**: S

**What**: extend `evaluation/locomo/score.py` to optionally call GPT-4o (or our Gemini Flash via cliproxyapi) to judge each prediction binary correct/incorrect, alongside existing F1/EM/semsim. Output in same JSON.

**Why third**: lets us publish numbers comparable to Memobase / Mem0 / Zep. May reveal our actual performance is closer to MemOS-tier than F1 suggested.

**Risk**: LLM cost per question. Mitigation: cache results by hash(question, prediction).

### #4 — Structured user profile layer (architectural, M9-large)
**Effort**: XL — this is a new first-class object in MemDB

**What**: Profile object with `topic/sub_topic/memo` schema, populated by LLM extraction at ingest, queryable separately from raw memories. New table `user_profiles`, new extract pipeline, new prompt templates (port from `extract_profile.py` + `merge_profile.py`).

**Why fourth**: high lift potential but significant scope. Should be its own M-sprint, not a follow-up.

### #5 — Cat-5 exclusion in our published numbers (or honest dual-track)
**Effort**: S (config flag in score.py)

**What**: add `--exclude-categories=5` flag to `evaluation/locomo/score.py`. Publish two aggregate numbers: "Overall (with cat-5)" and "Overall (excluding cat-5, comparable to Memobase/Mem0)".

**Why**: pure honesty. Our number with cat-5 included is being unfairly compared to Memobase's number that excludes it. This isn't gaming the metric — it's making the comparison apples-to-apples.

---

## Part 5 — What Memobase does NOT have (where MemDB already wins)

- **Real graph reasoning**: their multi-hop is 46.88 (worse than Mem0). We have D2 multi-hop infrastructure.
- **CE rerank**: BGE-reranker-v2-m3 via embed-server. Memobase has no rerank.
- **MMR / RRF / temporal decay**: all in our search pipeline. Memobase: pure cosine.
- **Per-cube isolation**: Memobase uses single global user store. We have proper multi-tenancy.
- **Latency**: our search 15-30ms, Memobase server unknown but Python+OpenAI roundtrip per extract.

---

## Part 6 — What this means for our roadmap

**Short-term (M9 sprint)**: take #1 + #2 + #3 + #5. Together: cat-5 attribution closed, cat-3 temporal lifted, comparable measurement, honest reporting. Estimate: 3-5 days.

**Medium-term (M10 sprint)**: take #4. Profile layer is a multi-week project — needs design doc, schema migration, extraction pipeline rewrite, query API extension. Could lift cat-1/cat-4 substantially.

**Long-term**: D2 multi-hop remains our differentiator. Memobase can't catch us on cat-2 because they don't have graph traversal. Our M8 S3+S10 work in this area is the right bet for a category Memobase explicitly LOST on.

**Strategic positioning**: 
- We compete with Memobase on **temporal + open-domain** (port their tricks)
- We BEAT Memobase on **multi-hop reasoning** (our graph layer)
- We win on **production characteristics** (latency, multi-tenancy, observability)

That's a credible "best in class" pitch — once the LLM Judge measurement lands and we publish on a level field.

---

## Code refs (all from `/home/krolik/src/compete-research/memobase`)

| Concept | File | Line |
|---------|------|------|
| Dual-speaker ingest | `docs/experiments/locomo-benchmark/src/memobase_client/memobase_add.py` | 65-150 |
| Dual-speaker query | `docs/experiments/locomo-benchmark/src/memobase_client/memobase_search.py` | 71-105 |
| Cat-5 exclusion | `docs/experiments/locomo-benchmark/src/memobase_client/memobase_search.py` | 147 |
| Extract profile prompt | `src/server/api/memobase_server/prompts/extract_profile.py` | 39-107 |
| Time-sensitive instruction | `src/server/api/memobase_server/prompts/extract_profile.py` | 97 |
| `[mention DATE]` example | `src/server/api/memobase_server/prompts/extract_profile.py` | 26 |
| Topic taxonomy guidelines | `src/server/api/memobase_server/prompts/extract_profile.py` | 49-53 |
| Context pack (profile + events) | `src/server/api/memobase_server/prompts/chat_context_pack.py` | 6-16 |
| Search params (threshold, max tokens) | `docs/experiments/locomo-benchmark/src/memobase_client/memobase_search.py` | 53-58 |
| Merge profile prompts | `src/server/api/memobase_server/prompts/merge_profile.py`, `merge_profile_yolo.py` | full file |
| Pick-related profiles | `src/server/api/memobase_server/prompts/pick_related_profiles.py` | full file |
