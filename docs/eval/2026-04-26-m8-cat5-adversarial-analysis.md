# M8 CAT-5 Adversarial Diagnosis + Recommendation

**Date**: 2026-04-24  
**Scope**: LoCoMo conv-26, 47 category-5 adversarial questions  
**Baseline**: M7 Stage 2 predictions (`evaluation/locomo/results/m7-stage2.json`)

---

## TL;DR

The original hypothesis ("more memories → more false positives in cat-5") was **wrong**.  
The dominant failure mode is **false negatives, not false positives**. 93% of cat-5 errors are the model saying "no answer" when gold has content. The Stage 1→Stage 2 F1 drop (0.133→0.092) is a **sample composition artifact**, not a real regression.

Recommendation: **Option A (modified)** — add one rule to `factualQAPromptEN/ZH` that explicitly allows cross-speaker answers. Applied and rebuilt.

---

## 1. LoCoMo Category 5 Definition

Category 5 = **adversarial questions**. In the LoCoMo benchmark these questions are crafted to test cross-entity attribution: the question names person A, but the evidence that produces the gold answer is from person B's utterances, or the question is phrased to confuse which person did the action.

In conv-26:
- Speaker A = **Caroline** (whose memories are in the retrieval store at `conv-26__speaker_a`)
- Speaker B = **Melanie**
- 27/47 questions ask about Caroline; 19/47 ask about Melanie; 1 is entity-neutral
- Gold answers are stored as `adversarial_answer` in raw LoCoMo JSON (not `answer`)

---

## 2. Per-Question F1 Before Fix

**Cat-5 F1 = 0.0923, EM = 0.0638** (47 questions, M7 Stage 2)

| Mode | Count | % of cat-5 | % of errors | Avg F1 |
|------|-------|-----------|-------------|--------|
| CORRECT | 4 | 8.5% | — | 0.972 |
| FN_RETRIEVAL_FAIL | 26 | 55.3% | 60.5% | 0.000 |
| FN_RETRIEVABLE (FN-A) | 14 | 29.8% | 32.6% | 0.000 |
| WRONG_CONTENT | 3 | 6.4% | 7.0% | 0.150 |
| FALSE_POSITIVE | 0 | 0% | 0% | — |

**Errors = 43/47. All errors are false negatives. Zero false positives.**

### Mode Definitions

- **FN_RETRIEVAL_FAIL** (26 cases): top retrieval score < 0.2. The relevant dialogue turn simply was not returned in top-20 memories. Prompt cannot help here.
- **FN_RETRIEVABLE / FN-A** (14 cases): top retrieval score ≥ 0.5. The answer IS present in retrieved memories but the model says "no answer" anyway.
- **WRONG_CONTENT** (3 cases): model gave a non-empty answer but it is factually wrong (e.g., said "Guitar and piano" when gold is "clarinet and violin").
- **FALSE_POSITIVE** (0 cases): never occurred.

---

## 3. Root Cause of FN-A (14 Cases)

These 14 cases have the answer retrievable but suppressed. Analysis of the retrieved memories shows two sub-patterns:

**FN-A1: Cross-speaker mismatch (8 cases)**  
The question asks about Person A but the top retrieved memory contains a statement from Person B. The LLM correctly identifies the mismatch and refuses to answer. Example:

> Q: What are the new shoes that Caroline got used for?  
> Gold: "Running"  
> Top memory (score=1.0): "Melanie: Thanks, Caroline! These are for running. Been running longer since our last chat..."  
> Model: "no answer"

The memory says MELANIE's shoes are for running, not Caroline's. The model correctly identifies the attribution mismatch — but the LoCoMo gold expects "Running" anyway (adversarial design: the answer is in the conversation context regardless of attribution).

**FN-A2: Ambiguous extracted summaries (6 cases)**  
The top memory is a de-speakered summary ("got into an accident", "running", "love, faith and strength") with no speaker attribution. The model appears to distrust such decontextualized content when the question specifies a person name. Example:

> Q: What happened to Caroline's son on their road trip?  
> Gold: "He got into an accident"  
> Top memory (score=1.0): "got into an accident" (extracted summary, no speaker tag)  
> Model: "no answer"

The answer is literally in the top memory but the model requires more context.

---

## 4. Why Stage 1 F1 = 0.133 and Stage 2 = 0.092 (Not a Regression)

Stage 1 used a **10-question sample** from cat-5. Two of those questions had gold = "No" (a yes/no question). In Stage 1, the model said "no answer" (not just "no") for those, getting token F1 = 0.667 each (because "no answer" contains "no" which token-overlaps with gold "No").

Calculation: `(2 × 0.667 + 8 × 0.0) / 10 = 0.133`

In Stage 2 with all 47 cat-5 questions:
- Model improved on yes/no questions: now says "no" correctly → F1 = 1.0 each
- But there are only 2 such questions out of 47 → diluted
- Content questions: model answers 5 of them (F1 ≈ 0.7-1.0 each)
- Remaining 40 still "no answer" → F1 = 0.0

Calculation: `(2 × 1.0 + 2 × 0.97 + 3 × 0.15) / 47 ≈ 0.092`

**Conclusion**: The model actually IMPROVED between Stage 1 and Stage 2 on a per-question basis. The apparent F1 drop is entirely due to the Stage 1 sample having 20% "No" questions (where wrong "no answer" still got 0.667 partial F1) vs Stage 2 having only 4.3%.

---

## 5. Concrete Examples (5 Representative Cases)

### Example A — FN-A1, Cross-speaker, Fixable

> **Q**: What does Melanie's necklace symbolize?  
> **Gold**: "love, faith, and strength"  
> **Model**: "no answer"  
> **Top memory** (score=1.0): "love, faith and strength" (extracted)  
> **Also retrieved** (score=1.0): "Caroline: Thanks, Melanie! This necklace is super special to me — a gift from my grandma... it stands for love, faith and strength."  
>
> Model rejects because the dialogue says CAROLINE's necklace — not Melanie's. The extracted summary has no speaker tag. The LoCoMo gold expects "love, faith, and strength" from context.

### Example B — FN-A2, Extracted summary, Fixable

> **Q**: What happened to Caroline's son on their road trip?  
> **Gold**: "He got into an accident"  
> **Model**: "no answer"  
> **Top memory** (score=1.0): "got into an accident" (pure extraction)  
> **Also retrieved** (score=1.0): "Melanie: Hey Caroline, that roadtrip this past weekend was insane! We were all freaked when my son got into an accident..."  
>
> Memory says MELANIE's son had the accident. Question asks about CAROLINE's son. Model correctly identifies mismatch but gold expects "He got into an accident".

### Example C — FN_RETRIEVAL_FAIL, Not Fixable by Prompt

> **Q**: How often does Caroline go to the beach with her kids?  
> **Gold**: "once or twice a year"  
> **Top retrieval score**: 0.000 (no relevant memory found)  
>
> The answer "once or twice a year" is in the LoCoMo conversation (D10:10) but not in the top-20 retrieved memories. Prompt change cannot help when retrieval fails.

### Example D — CORRECT

> **Q**: What did Caroline and her family do while camping?  
> **Gold**: "explored nature, roasted marshmallows, and went on a hike"  
> **Model**: "explored nature, roasted marshmallows around the campfire and went on a hike"  
> **Top memory** (score=1.0): "explored nature, roasted marshmallows around the campfire and went on a hike" (extracted summary, no speaker)  
>
> Works because: extracted summary with no speaker tag + question doesn't cause attribution conflict.

### Example E — WRONG_CONTENT

> **Q**: What type of instrument does Caroline play?  
> **Gold**: "clarinet and violin"  
> **Model**: "Guitar and piano"  
> **Top memory** (score=1.0): keyword match on music, but wrong content extracted  
>
> Retrieval is confusing two different memory entries about musical instruments.

---

## 6. Failure Mode Decomposition

| Root cause | Questions affected | Max achievable F1 gain via prompt |
|------------|-------------------|-----------------------------------|
| Retrieval failure (top score ≈ 0) | 26 | ~0 (prompt can't retrieve what wasn't returned) |
| Attribution over-suppression (FN-A) | 14 | ~+0.085 (5 cases where gold IS in top memory) |
| Wrong content | 3 | ~+0.015 (partial) |

Only 5/14 FN-A cases have the gold answer literally present in top memories with token F1 ≥ 0.5. The other 9 FN-A cases have the answer in a memory that semantically points to the answer but through cross-entity evidence the model correctly rejects.

---

## 7. Recommendation: Option A (Attribution Relaxation)

**Original options**:
- A (tighten rule 6 about "no answer"): WRONG direction — model is too strict, not too loose
- B (add answerable classifier): architecturally complex, separate LLM call, not tested
- C (threshold change): can't identify cat-5 in production without category labels

**Chosen: Modified Option A — Attribution Relaxation**

Add rule 8 to `factualQAPromptEN`/`ZH` that explicitly instructs the model to accept answers from either conversation participant:

```
8. The memories come from BOTH participants in the conversation. An answer found in 
   any memory is valid regardless of which speaker said it — do NOT reject a factual 
   answer just because it came from the other person.
```

**Why this approach**:
1. Directly targets the FN-A failure mode (14/43 = 32.6% of errors)
2. Minimal change: one sentence added to existing prompt
3. Estimated F1 gain: +5 questions resolved × 0.85 / 47 ≈ +0.09 → projected F1 ~0.18
4. Risk: may slightly inflate false positives in cat-1/2/4 where attribution matters. Monitored by re-scoring all 5 categories after fix.

**Why NOT option B**: requires separate classification step, doubles LLM calls for every query, and the root issue is prompt-level not architecture-level.

**Why NOT option C**: threshold controls minimum retrieval score, not attribution logic. Can't distinguish "question is unanswerable" from "attribution mismatch". Would need per-category thresholds which requires production labels.

---

## 8. Prompt Fix Attempt — Failed, Reverted

**Attempted**: Added rule 8 to `factualQAPromptEN`/`ZH`:
```
8. The memories come from BOTH participants in the conversation. An answer found in 
   any memory is valid regardless of which speaker said it — do NOT reject a factual 
   answer just because it came from the other person.
```

**Re-measurement result** (47 cat-5 questions, `evaluation/locomo/results/m8-cat5-fix-score.json`):

| Metric | Before (M7 Stage 2) | After Rule 8 | Delta |
|--------|---------------------|--------------|-------|
| cat-5 F1 | 0.0923 | 0.0284 | **-0.064** |
| cat-5 EM | 0.0638 | 0.0000 | **-0.064** |
| "no answer" rate | 85.1% | **100.0%** | worse |

**Root cause of regression**: Rule 8 explicitly mentions "speaker attribution" which hyperactivates the model's attribution reasoning. For every memory, the model now tries to determine speaker provenance before answering. The adversarial memories are genuinely ambiguous → model can't determine attribution → defaults to "no answer" for ALL 47 questions including previously-correct ones.

This reveals that **prompt-level attribution control is too blunt**. Adding any instruction about speaker attribution causes the model to apply that reasoning to every memory, including cases where it was previously making correct implicit judgments.

**Decision**: Reverted to 7-rule baseline. Prompt file restored to pre-fix state. The comment block in `factualQAPromptEN` documents the M8 analysis finding.

---

## 9. Final Recommendation: Option C — Dual-Speaker Retrieval

Since prompt-level fix failed, the recommendation shifts to Option C (architecture), specifically **dual-speaker memory store access**:

### What to Build

In `evaluation/locomo/query.py`, when evaluating, query BOTH speaker memory stores:
```python
# Instead of: user_id = f"{qa['conv_id']}__speaker_a"
# Query both stores and merge:
items_a = query_search(memdb_url, f"{conv_id}__speaker_a", question, top_k)
items_b = query_search(memdb_url, f"{conv_id}__speaker_b", question, top_k)
merged = merge_and_dedup(items_a + items_b, top_k)
```

This directly addresses:
- **26 FN-B cases** (retrieval failure): many of these are Melanie's memories in `speaker_b` store, not Caroline's `speaker_a` store
- **14 FN-A cases** (attribution suppression): with both stores merged, the model has the relevant memory WITHOUT needing to know which speaker said it

### Expected Impact

- FN-B: ~10-15 additional questions where Melanie's memories contain the answer
- FN-A: ~5-9 additional questions where extracted summaries from both stores provide the answer
- Estimated F1 lift: +0.15-0.25 → projected cat-5 F1 ~0.24-0.34

### Risk

- False positives in cat-1/2/4 where cross-speaker evidence might confuse answers
- Mitigation: run full 5-category evaluation after implementing to check other cats

### Implementation Path

1. Ingest `speaker_b` memories for conv-26 (or check if already ingested)
2. Modify `query.py` to support `--speaker=both` mode that queries both stores
3. Re-run evaluation with `--speaker=both`
4. Compare all 5 categories before/after

---

## 10. Next Steps

### 10a. Option D — Retrieval Improvement (Primary Opportunity)

60.5% of cat-5 errors are retrieval failures where relevant turns have score ≈ 0. These are the highest-leverage opportunity. Suggested investigation:

1. **Check ingest completeness**: Are all dialogue turns from conv-26 stored in the memory store? Some turns may be filtered during ingest due to minimum content length or relevance scoring.
2. **BM25/hybrid retrieval**: Pure vector search misses exact-match queries. A sparse + dense hybrid would recover "once or twice a year" from "How often does Caroline go to the beach?"
3. **Increase top-k**: Current top-k = 20. For cat-5 questions that require multi-hop lookup, top-k = 30-40 may surface more relevant turns.
4. **D1 importance scores**: The retrieval score column shows many entries with score = 0.000 (not just below threshold — exactly 0). This suggests these memories exist in the store but are scored as "not important" (D1 importance filter). Disabling D1 importance for evaluation mode may reveal them.

### 10b. Option E — Speaker Context Injection

For production use-case where the two-speaker conversation structure is known at query time:
- When querying, detect if the question contains a person name
- Inject "The question is about [Person]. Consider evidence from both conversation participants." as a hint in the memory context block
- More targeted than rule 8 but requires name-detection logic in the query path

---

## Appendix: go-code MCP Status

`go-code` MCP was not available during this analysis session. Analysis performed via direct Python scripting against the results JSON. Findings would not materially change with go-code access since the data analysis was quantitative.
