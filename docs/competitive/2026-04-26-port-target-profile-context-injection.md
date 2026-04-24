# Port Target #2: Profile Context Injection at Extraction Time

**Source:** Memobase `src/server/api/memobase_server/controllers/modal/chat/extract.py`
**Technique:** Pack current user profile state into extraction prompt so LLM generates continuations, not isolated facts
**Effort:** S (1-2 days)
**Expected F1 lift:** +3-5 points on temporal/single-hop questions; Memobase achieves 85.05% temporal vs our unverified baseline

---

## What We Port and Why

Memobase v0.0.37 achieves **75.78% overall LLM Judge** on LoCoMo (source: `docs/experiments/locomo-benchmark/README.md`), with **85.05% on temporal** (category 2). This is the highest temporal score in the comparison table.

The key mechanism: when extracting memories from a new conversation turn, Memobase packs the **current user profile state** into the extraction prompt context. The LLM therefore generates facts that are aware of what's already known — enriching existing facts, creating temporal continuations, and avoiding isolated re-extraction of known information.

```python
# extract.py:34-51
profiles = current_user_profiles.profiles
CURRENT_PROFILE_INFO = pack_current_user_profiles(
    current_user_profiles, project_profiles
)
project_profiles_slots = CURRENT_PROFILE_INFO["project_profile_slots"]

PROMPTS[USE_LANGUAGE]["extract"].pack_input(
    # ...conversation content...
    system_prompt=PROMPTS[USE_LANGUAGE]["extract"].get_prompt(
        PROMPTS[USE_LANGUAGE]["profile"].get_prompt(project_profiles_slots)
    )
)
```

The extraction prompt receives:
1. New conversation turns
2. Current profile state (topics + sub-topics + existing values)
3. Profile taxonomy slots (what topics to watch for)

Result: instead of `{"ADD": "User likes hiking"}` when "hiking" is already known, LLM generates `{"ADD": "User went hiking in the Alps last week (update to previous hiking interest)"}` — a temporal continuation.

---

## Source Code Reference

File: `github.com/memodb-io/memobase/blob/main/src/server/api/memobase_server/controllers/modal/chat/extract.py`

```python
# Line 26-80: extract_topics with profile context injection
async def extract_topics(
    chat_data,
    project_profiles: ProfileConfig,
    current_user_profiles: UserProfilesData,
    ...
):
    profiles = current_user_profiles.profiles
    CURRENT_PROFILE_INFO = pack_current_user_profiles(
        current_user_profiles, project_profiles
    )
    project_profiles_slots = CURRENT_PROFILE_INFO["project_profile_slots"]

    results = await llm_complete(
        PROMPTS["extract"].pack_input(
            ...conversation...,
            current_profile=profiles,        # ← EXISTING state
        ),
        system_prompt=PROMPTS["extract"].get_prompt(
            PROMPTS["profile"].get_prompt(project_profiles_slots)  # ← TAXONOMY
        ),
    )
    parsed_facts = parse_string_into_profiles(results)
```

Taxonomy file: `github.com/memodb-io/memobase/blob/main/src/server/api/memobase_server/prompts/user_profile_topics.py`
```python
CANDIDATE_PROFILE_TOPICS = [
    UserProfileTopic("basic_info", sub_topics=["Name", "Age", "Gender", ...]),
    UserProfileTopic("work", sub_topics=["company", "title", "work_skills", ...]),
    UserProfileTopic("psychological", sub_topics=["personality", "values", "goals"]),
    UserProfileTopic("life_event", sub_topics=["marriage", "relocation", "retirement"]),
    # 8 topics total
]
```

---

## Where It Lives in MemDB

MemDB already has:
- 22-category preference taxonomy (D8, PR #48) in `memdb-go/internal/handlers/add_pref.go`
- Extraction prompts in `memdb-go/internal/llm/prompts/` (or equivalent)
- `add_fine.go` — fine-grained add pipeline that calls LLM extraction

Integration point: `add_fine.go` extraction step.

Current flow:
```go
// add_fine.go — extraction
extractionInput := buildExtractionPrompt(messages, existingMemories)
// existingMemories = recent top-k similar memories (for dedup)
// BUT: user's preference/profile state NOT included
facts, _ := s.LLM.Extract(ctx, extractionInput)
```

Proposed change:
```go
// add_fine.go — profile context injection
userProfile, _ := s.DB.GetUserProfileSummary(ctx, cubeID)
extractionInput := buildExtractionPrompt(
    messages,
    existingMemories,
    userProfile,   // ← NEW: pack current preference/profile state
)
facts, _ := s.LLM.Extract(ctx, extractionInput)
```

Two additions needed:
1. `internal/db/queries/profile_summary.sql` — query to fetch top-K preference memories per taxonomy category for a cube
2. `internal/llm/prompts/extraction.go` — add `{{.UserProfileContext}}` block to extraction prompt template

---

## Test Plan

1. Unit: `buildExtractionPrompt` with non-nil `userProfile` includes profile section in output
2. Unit: empty profile → prompt identical to current (no regression for new users)
3. Integration: LoCoMo temporal category (cat 2) on 50 QA — verify temporal accuracy improvement
4. Regression: LoCoMo overall F1 must not decrease vs current 0.238 aggregate
5. Preference test: existing vaelor integration — verify preference memories enriched rather than duplicated
6. Gate: env var `MEMDB_EXTRACT_PROFILE_CONTEXT=true` (default false until validated)

---

## Risk

- **Prompt token bloat**: packing full profile into every extraction call. Mitigate: limit to top-5 preference memories per category, max 500 tokens. Fetch only categories relevant to conversation (semantic match).
- **Hallucination amplification**: if existing profile has errors, injection may reinforce them. Mitigate: add "do not repeat existing facts unless updating them" instruction.
- **Latency**: additional DB query for profile fetch. Mitigate: cache profile summary in Redis (same pattern as LLM rerank cache, 5min TTL).
- **Cold start**: new users have no profile → no injection → behavior identical to today.

## Estimated Effort

- `internal/db/queries/profile_summary.sql`: ~20 LOC
- `internal/handlers/add_fine.go`: ~15 LOC (fetch + pass profile)
- `internal/llm/prompts/extraction.go`: ~25 LOC (template addition)
- Tests: ~50 LOC
- **Total: ~110 LOC, 1-2 days**
