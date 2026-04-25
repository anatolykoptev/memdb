# Add Pipeline — Improvement Backlog

> Derived from competitive analysis (March 2026) against mem0 22k★,
> Zep/Graphiti 4k★, Letta 12k★, LangMem 2k★.
>
> Competitive context (snapshot tables, competitor patterns):
> [docs/competitive/2026-03-add-pipeline-vs-rivals.md](../competitive/2026-03-add-pipeline-vs-rivals.md)
>
> Master roadmap: [ROADMAP.md](../../ROADMAP.md)

---

## Items

### 1. Soft-delete / temporal invalidation — 🔴 High priority

**Source:** Graphiti (`expired_at` on edges and nodes)

**Problem:** DELETE/UPDATE in `applyFineActions` do hard-delete (`DeleteByPropertyIDs`).
Data is lost permanently. No audit, no undo, no point-in-time queries.

**Solution:** `expired_at` timestamp instead of hard-delete.

```go
// Instead of DeleteByPropertyIDs:
// UPDATE SET status = 'expired', expired_at = $now WHERE id = $id

// Query filter:
// WHERE status = 'activated' AND (expired_at IS NULL OR expired_at > $query_time)

// Cleanup (optional, cron):
// DELETE WHERE status = 'expired' AND expired_at < now() - interval '30 days'
```

**What changes:**
- `add_fine.go: applyDeleteAction()` → soft-delete with `expired_at`
- `add_fine.go: applyUpdateAction()` → expire old + insert new (instead of UPDATE)
- `db/queries/` → new SQL for soft-delete
- `search/` → WHERE filter on `expired_at`
- `scheduler/reorganizer.go` → use soft-delete on merge

**Effort:** M (5-7 files, SQL migration)
**Metric:** 0 hard-deletes in add pipeline. Point-in-time query works.

---

### 2. Embedding cache — 🟡 Medium priority

**Source:** Letta (`@async_redis_cache(key_func=lambda text, model, endpoint: ...)`)

**Problem:** MemDB re-embeds identical texts on every request. ONNX local (~5-20ms),
but during buffer flush the same phrases recur.

**Solution:** Redis cache keyed on text hash.

```go
// internal/embedder/cache.go
type CachedEmbedder struct {
    inner  Embedder
    redis  *redis.Client
    ttl    time.Duration  // 24h default
}

// Key: "emb:" + SHA256(text)[:16]
// Value: []byte (binary float32 slice)
// Flow: cacheGet → miss → inner.Embed() → cacheSet → return
```

**Effort:** S (1 new file, wrapper around existing embedder)
**Metric:** cache hit rate > 15% on buffer flush. ~20% ONNX inference savings.

---

### 3. OTel tracing across the pipeline — 🟡 Medium priority

**Source:** Letta (`@trace_method` on each service method), Graphiti (configurable `Tracer`)

**Problem:** Latency debugging only via slog. No breakdown by pipeline stage.
Bottleneck (LLM extraction? Embedding? DB insert? Background tasks?) is invisible.

**Solution:** OpenTelemetry spans on each stage.

```go
// Span hierarchy:
// NativeAdd (total)
//   ├── classify_content
//   ├── fetch_candidates
//   │   ├── vset_search
//   │   └── postgres_search
//   ├── llm_extract_and_dedup
//   ├── filter_hallucinated
//   ├── embed_facts (batch)
//   ├── apply_actions
//   │   ├── insert_nodes
//   │   └── vset_write
//   └── background (fire-and-forget, linked)
//       ├── episodic_summary
//       ├── skill_extraction
//       ├── tool_trajectory
//       ├── entity_linking
//       └── profile_refresh
```

**What changes:**
- `internal/server/` → OTel TracerProvider init (OTLP exporter)
- `internal/handlers/add_fine.go` → span Start/End on each stage
- `internal/handlers/add_fast.go` → same (fewer stages)
- `docker-compose.yml` → Grafana Tempo / Jaeger container (optional)

**Effort:** M (OTel SDK integration, 3-4 files)
**Metric:** p95 latency breakdown by stage. Bottleneck identification < 5 min.

---

### 4. LLM call semaphore — 🟡 Medium priority

**Source:** Graphiti (`semaphore_gather(max_concurrency=N)`)

**Problem:** Background goroutines fire-and-forget without concurrency limit:
- episodic summary (45s timeout)
- skill extraction (90s timeout)
- tool trajectory (90s timeout)
- entity linking (15s timeout)

At burst of 10 add requests → 40 parallel LLM calls → CLIProxyAPI rate limit / OOM.

**Solution:** Shared semaphore for all background LLM calls.

```go
// internal/handlers/handler.go
type Handler struct {
    // ...existing fields...
    llmSem *semaphore.Weighted  // max concurrent background LLM calls
}

// Config: MEMDB_LLM_MAX_CONCURRENT=8 (default)

// Usage in add_fine.go, add_skill.go, add_episodic.go:
// if !h.llmSem.TryAcquire(1) {
//     h.logger.Debug("skipping background task: LLM semaphore full")
//     return
// }
// defer h.llmSem.Release(1)
```

**Effort:** S (1 field in Handler, ~5 Acquire/Release insertion points)
**Metric:** max concurrent LLM calls ≤ 8 under any load.

---

### 5. Inline preference extraction — 🟡 Medium priority

**Source:** Migration analysis (March 2026). Not in Python or MemOS upstream — new feature.

**Problem:** Preference extraction only via explicit `pref_add` scheduler task.
User preferences from normal conversations (add pipeline) are not extracted automatically.

**Solution:** 5th background goroutine in `add_fine.go`.

```go
// internal/handlers/add_pref.go
// Two types:
// 1. Explicit: "use snake_case", "I prefer dark theme"
// 2. Implicit: user consistently corrects AI on X → infer preference
//
// Flow: detect preference signals → LLM extract → vector dedup → ADD/UPDATE
// Gate: conversation has user messages with opinion/preference patterns
```

**Effort:** M (LLM prompt, dedup, integration)
**Metric:** Preferences extracted from ordinary add-requests, not only from explicit pref_add.

---

### 6. Skill extraction: chat_history support — 🟢 Low priority

**Source:** Python/MemOS have `whether_use_chat_history` + `content_of_related_chat_history` in skill extraction.

**Problem:** Go skill extractor does not use previous conversation history to enrich skill context.

**Solution:** Add optional chat history to `ExtractSkill()`.

**Effort:** S (prompt extension + parameter)
**Metric:** Skill descriptions more complete when history is available.

---

### 7. Periodic reorganizer interval tuning — 🟡 Medium priority

**Source:** Python/MemOS reorganizes every 100s, Go every 6h (60× less frequently).

**Problem:** Near-duplicates and dead memories accumulate for 6 hours before cleanup.

**Solution:** Configurable interval via env var.

```go
// MEMDB_REORG_INTERVAL=30m (default, compromise between 100s and 6h)
// Or: event-driven — trigger after N new add operations
```

**Effort:** S (1 env var, 1 config line)
**Metric:** Duplicates cleaned up within 30min of creation.

---

### 8. trustcall-style PATCH for UserMemory — 🟢 Low priority (deferred)

**Source:** LangMem (JSON Patch RFC 6902 on existing records)

**Problem:** On UserMemory/PreferenceMemory update, current flow:
LLM extract → cosine dedup → UPDATE if similar, ADD if new.
Duplicates still slip through because cosine similarity misses semantic overlaps.

**Solution:** LLM sees existing records and returns PATCH operations.

**Effort:** L
**Status:** Deferred. Revisit after Python Deprecation is fully settled.

---

### 9. Extraction prompt quality (MemOS parity) — 🔴 High priority

**Source:** Deep audit of MemOS upstream (March 2026). 30+ prompts vs our ~10.

**Problem:** `unifiedSystemPrompt` does not enforce:
- Third-person perspective ("The user..." instead of "I...")
- Temporal resolution (relative→absolute dates)
- Pronoun resolution ("she"→"Caroline")
- Source attribution ([user viewpoint] vs [assistant viewpoint])

**M9 progress (2026-04-26):** `[mention DATE]` time-anchoring was shipped (temporal
resolution item above — ISO dates in time-sensitive facts). Remaining gaps:
third-person enforcement, pronoun resolution, source attribution tags.

**Solution:** Extend extraction prompt + add post-extraction enhancement.

**Effort:** S (prompt changes only, no architectural changes)
**Metric:** 0 first-person pronouns in stored memories. Relative dates resolved (✅ partially done in M9).

---

### 10. Source attribution tagging — 🟡 Medium priority

**Source:** MemOS `STRATEGY_STRUCT_MEM_READER_PROMPT`

**Problem:** All extracted facts are stored identically — cannot distinguish a user fact from
an assistant inference. Chat prompt has "Four-Step Verdict" but cannot filter AI views
because tags do not exist.

**Solution:** Add `source` field to extracted facts: `"user" | "assistant" | "inferred"`.

**Effort:** S (new field in prompt + DB column)
**Metric:** Chat prompt can filter assistant inferences.

---

### 11. Strategy-based chunking — 🟡 Medium priority

**Source:** MemOS `mem_reader_strategy_prompts.py`

**Problem:** Go uses fixed 10-message windows. MemOS supports content_length vs message_count strategies.

**Solution:** Configurable chunking strategy via `AddParams.ChunkStrategy`.

**Effort:** M (new chunking logic in add pipeline)
**Metric:** Long conversations (>10 messages) correctly split.

---

## Implementation order

```
Phase 1 (quick wins, 1-2 days):
  4. LLM semaphore (S)
  2. Embedding cache (S)
  7. Periodic interval tuning (S)
  9. Extraction prompt quality — remaining gaps (S)

Phase 2 (architecture, 1-2 weeks):
  1. Soft-delete / temporal invalidation (M)
  5. Inline preference extraction (M)
  10. Source attribution (S)
  11. Strategy-based chunking (M)

Phase 3 (observability, 1 week):
  3. OTel tracing (M)

Deferred:
  6. Skill chat_history (S) — low impact
  8. trustcall PATCH (L) — after Python Deprecation
```

Note: Phase 0 from the original plan (item 9, "extraction prompt quality") was
partially closed by M9 `[mention DATE]` time-anchoring. The remaining prompt
gaps (third-person enforcement, pronoun resolution) are folded into Phase 1 above.

---

## M8/M9 follow-ups (added 2026-04-26)

### 12. A/B test `answer_style=factual` as default for QA workloads — 🟡 Medium priority

**Source:** M8 PR #80 (S8 PRODUCT).

**Status:** Canary live at sticky-per-user 10% split (M8). Needs 24h prod observation.

**Action:** Expand cohort to 100%, capture p50/p95 + acceptance signal, GO/HOLD/ROLLBACK.

**Why:** M7 confirmed 2.1× chat speedup (p95 14.7s → 7.0s). Canary promotion makes this
the default for all QA workloads, not just 10%.

**Effort:** S (1-2 days observation; no code change unless canary graduates).

---

### 13. `InvalidateEdgesByMemoryID` only handles `from_id`, not `to_id` — 🟡 Medium priority

**Source:** M8 S10 STRUCTURAL-EDGES (PR #83).

**Problem:** Clears outgoing edges (`from_id`) but not incoming (`to_id`). M8 S10
amplified edge volume ~30× per memory — orphan accumulation is now meaningful.

**Action:** Handle both directions, OR add periodic GC sweep for edges where either
endpoint memory no longer exists.

**Effort:** S (~1h).
