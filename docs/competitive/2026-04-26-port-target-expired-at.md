# Port Target #3: Expired_at Soft-Delete (Temporal Invalidation)

**Source:** Graphiti `graphiti_core/utils/maintenance/edge_operations.py` + `graphiti_core/edges.py`
**Technique:** Replace hard-delete with `expired_at` timestamp; search filters `WHERE expired_at IS NULL`
**Effort:** M (5-7 days, SQL migration required)
**Expected F1 lift:** +2-3 points indirect (cleaner retrieval, no stale contradictions) + high product quality (point-in-time queries, undo, audit trail)

---

## What We Port and Why

MemDB currently uses hard-delete in `applyDeleteAction` and `applyUpdateAction` (confirmed in ROADMAP-ADD-PIPELINE.md §1). When a memory is updated or deleted, the old version is permanently gone. This has three problems:

1. **Stale contradictions reach retrieval**: a contradiction race condition (new fact extracted before old expires) can return both conflicting facts.
2. **No undo**: deleting a memory via API or LLM error is permanent.
3. **No point-in-time**: cannot answer "what did we know about user X as of last Tuesday?"

Graphiti's approach (`edges.py:271`):

```python
expired_at: datetime | None = Field(
    default=None,
    description='datetime after which the edge is no longer valid'
)
```

When a new edge contradicts an existing one (`edge_operations.py:562-570`):
```python
elif (
    edge_valid_at_utc is not None
    and resolved_edge_valid_at_utc is not None
    and edge_valid_at_utc < resolved_edge_valid_at_utc
):
    edge.invalid_at = resolved_edge.valid_at
    edge.expired_at = edge.expired_at if edge.expired_at is not None else utc_now()
    invalidated_edges.append(edge)
```

All search queries add: `WHERE expired_at IS NULL OR expired_at > $query_time`

---

## Source Code Reference

File: `/home/krolik/src/compete-research/graphiti/graphiti_core/edges.py:271`
```python
expired_at: datetime | None = Field(
    default=None,
    description='datetime after which the edge is no longer valid'
)
invalid_at: datetime | None = Field(
    default=None,
    description='reference timestamp from the episode that produced this edge'
)
```

File: `/home/krolik/src/compete-research/graphiti/graphiti_core/utils/maintenance/edge_operations.py:543-572`
```python
def resolve_edge_contradictions(resolved_edge, invalidation_candidates, now):
    invalidated_edges = []
    for edge in invalidation_candidates:
        # ... temporal overlap check ...
        elif edge_valid_at < resolved_edge_valid_at:
            edge.invalid_at = resolved_edge.valid_at
            edge.expired_at = edge.expired_at if edge.expired_at is not None else utc_now()
            invalidated_edges.append(edge)
    return invalidated_edges
```

File: `/home/krolik/src/compete-research/graphiti/graphiti_core/helpers.py:122-133`
```python
# semaphore_gather pattern (bonus: implement for MemDB LLM semaphore)
async def semaphore_gather(*coroutines, max_coroutines=None):
    semaphore = asyncio.Semaphore(max_coroutines or SEMAPHORE_LIMIT)
    async def _wrap_coroutine(coroutine):
        async with semaphore:
            return await coroutine
    return await asyncio.gather(*(_wrap_coroutine(c) for c in coroutines))
```

---

## Where It Lives in MemDB

**Files to modify:**

1. **SQL migration** (new file `memdb-go/internal/db/migrations/XXXX_add_expired_at.sql`):
```sql
ALTER TABLE memories ADD COLUMN expired_at TIMESTAMPTZ;
ALTER TABLE memories ADD COLUMN invalid_at TIMESTAMPTZ;
CREATE INDEX idx_memories_expired_at ON memories(expired_at) WHERE expired_at IS NULL;
```

2. **`memdb-go/internal/db/queries/search_queries.go`** — add `expired_at` filter to all search queries:
```sql
-- Before:
WHERE cube_id = $1 AND status = 'activated'

-- After:
WHERE cube_id = $1 AND status = 'activated'
  AND (expired_at IS NULL OR expired_at > $query_time)
```

3. **`memdb-go/internal/handlers/add_fine.go`** — `applyDeleteAction` and `applyUpdateAction`:
```go
// Instead of:
err = s.DB.DeleteByPropertyIDs(ctx, ids)

// Use:
err = s.DB.SoftDeleteByIDs(ctx, ids, time.Now())
// UPDATE memories SET status='expired', expired_at=$now WHERE id IN (...)
```

4. **`memdb-go/internal/db/queries/` (new)** — `soft_delete.sql`:
```sql
UPDATE memories
SET status = 'expired', expired_at = $2
WHERE id = ANY($1)
RETURNING id;
```

5. **`memdb-go/internal/scheduler/reorganizer.go`** — replace merge hard-deletes with soft-delete.

6. **Cleanup cron** (optional, low priority): `DELETE WHERE status='expired' AND expired_at < now() - interval '30 days'`

---

## Test Plan

1. Unit: `SoftDeleteByIDs` sets `expired_at` + `status='expired'`, does not remove row
2. Unit: search query excludes rows where `expired_at < query_time`
3. Integration: add a memory, delete it via API, verify it no longer appears in search but exists in DB with `expired_at` set
4. Integration: update a memory — old version has `expired_at = update_time`, new version has `expired_at = NULL`
5. Point-in-time: search with `query_time = yesterday` returns pre-update memories
6. Regression: LoCoMo full suite — retrieval must not return expired memories
7. Admin: new endpoint `/admin/memories/expired?cube_id=...` to view soft-deleted memories (nice-to-have)

---

## Risk

- **SQL migration on live DB**: additive (new nullable columns), backward compatible. Zero downtime.
- **Index bloat**: partial index `WHERE expired_at IS NULL` keeps active-memory index size same as before.
- **API breaking change**: `GET /memories/{id}` should 404 for expired memories (current hard-delete also 404s). Same behavior.
- **Reorganizer complexity**: periodic reorg currently hard-deletes merged-into memories. With soft-delete, merged memories stay in DB. Need `merged_into_id` reference tracking (already exists: `merged_into_id` field in ROADMAP-ADD-PIPELINE.md).
- **Storage growth**: expired memories accumulate. Cleanup cron handles this (30-day retention default).

## Estimated Effort

- SQL migration: ~30 LOC
- `search_queries.go`: ~10 LOC per query (~5 queries = 50 LOC)
- `add_fine.go`: ~25 LOC (soft-delete wrappers)
- `reorganizer.go`: ~20 LOC
- New DB function `SoftDeleteByIDs`: ~25 LOC
- Tests: ~80 LOC
- **Total: ~230 LOC, 5-7 days including migration testing**
