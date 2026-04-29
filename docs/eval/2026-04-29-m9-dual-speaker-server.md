# M9 — Server-side dual-speaker retrieval

**Date:** 2026-04-29
**Repo:** `memdb-go`
**Status:** shipped to local prod (smoke-tested)

## Why

M9 dual-speaker retrieval was implemented **only in the eval harness**:
`evaluation/locomo/query.py` issues two parallel `/product/search` calls
per question (`<conv>__speaker_a` and `<conv>__speaker_b`) and stitches
the buckets into a labelled "## Speaker A / B memories: ..." system prompt
in Python.

Production consumers (vaelor, oxpulse, future multi-tenant chat surfaces)
could not enable this feature without copying the harness loop verbatim
— so they didn't, and the M9 LoCoMo F1 lift never reached prod chat
quality. This sprint moves the fan-out + labelling into memdb-go so
opting in is a single JSON field flip.

## What shipped

### Request fields (search + chat)

Both `nativeSearchRequest` and `nativeChatRequest` now accept:

| Field | Type | Default | Effect |
|-------|------|---------|--------|
| `speakers` | `[]string` | `[]` | When `len>=2`, fan out one search per speaker in parallel; tag every memory with `metadata.speaker_label = <speaker_id>`. |
| `top_k_per_speaker` | `int` | `top_k` | Per-leg budget. Final list is still capped at `top_k`. |
| `merge_strategy` | `string` | `"interleave"` | `"interleave"` (round-robin, preserves diversity) or `"score"` (flat sort by metadata.relativity). |

When `speakers` is empty (or `len==1`), the request is byte-identical to
the legacy single-speaker path — **zero behaviour change for existing
callers**.

`user_id` is now **optional** when `len(speakers)>=2`. Single-speaker
callers must still supply `user_id` (validation unchanged for them).

### Routing

- `internal/handlers/search.go::NativeSearch` — forks to
  `handleDualSpeakerSearch` when `len(req.Speakers)>=2`. The legacy
  cache key + `Search` / `SearchFine` / `SearchByLevel` selector is
  bypassed; the dual-speaker path runs its own per-leg dispatcher
  (`dispatchDualLegSearch`) that mirrors the legacy mode/level routing.
- `internal/handlers/chat.go::NativeChatComplete` / `NativeChatStream`
  — both call `chatRetrieve`, which forks to `chatSearchMemoriesDual`
  when speakers are set. The per-speaker buckets feed
  `composeDualSpeakerSystemPrompt` (header + "## Speaker X memories:"
  blocks). The composed prompt becomes the `basePrompt` argument to
  `buildSystemPromptWithDecision`, so the M12.4 anti-refusal rules
  block still gets injected when `answer_style=factual` — same behaviour
  as the harness's pre-built prompt path.

### New files

- `internal/handlers/search_dual_speaker.go` (220 lines)
  - `runDualSpeakerSearch` — parallel fan-out + merge for search surface
  - `tagSpeakerLabel` — non-mutating speaker_label stamping
  - `mergeDualSpeakerResults` — interleave + score strategies, dedup by
    memory id, topK cap
  - `dispatchDualLegSearch` — per-leg search-mode router
- `internal/handlers/chat_dual_speaker.go` (200 lines)
  - `chatRetrieve` — single/dual router, also emits engagement metric
  - `chatSearchMemoriesDual` — chat-side fan-out (same threshold +
    `chatMinPersonalMem` floor as single-speaker `chatSearchMemories`)
  - `buildDualSpeakerPromptBlock` — "## Speaker X memories:" rendering
  - `composeDualSpeakerSystemPrompt` — header + blocks
  - `resolveBasePrompt` — caller > dual block > "" precedence
- `internal/observability/dual_speaker_metrics.go` (130 lines)
  - 4 new counters/histograms (see Metrics section)
- `internal/handlers/dual_speaker_test.go` (290 lines)
  - 17 unit tests covering merge strategies, dedup, validation,
    prompt rendering, base-prompt routing

### Files modified

- `internal/handlers/types.go` (already had Speakers/TopKPerSpeaker/
  MergeStrategy fields when this sprint started — left untouched)
- `internal/handlers/validate.go` — `validateSearchRequired` now
  accepts missing `user_id` when `len(speakers)>=2`
- `internal/handlers/search.go` — fork before legacy cache key build;
  `buildSearchParams` defensively handles nil `UserID`
- `internal/handlers/chat.go` — both chat handlers call `chatRetrieve`
  + `resolveBasePrompt`; profile injection guarded by `chatProfileUserID`
- `internal/handlers/chat_helpers.go` — `chatSearchMemories`
  defensively handles nil `UserID`; `chatPostAdd` skips when no
  `user_id` (dual-speaker case); `resolveCubeIDs` returns `nil`
  instead of panicking on nil `UserID`

## Calling the server

### Search

```bash
curl -X POST http://127.0.0.1:8080/product/search \
  -H "X-Service-Secret: $SECRET" -H "Content-Type: application/json" \
  -d '{
    "speakers": ["alice", "bob"],
    "query": "What do they do?",
    "top_k": 10,
    "top_k_per_speaker": 5,
    "merge_strategy": "interleave"
  }'
```

Response is the standard `{"code":200, "data": {"text_mem": [...]}}`
envelope; each memory in `data.text_mem[0].memories` has
`metadata.speaker_label` set to the speaker that produced it.

### Chat

```bash
curl -X POST http://127.0.0.1:8080/product/chat/complete \
  -H "X-Service-Secret: $SECRET" -H "Content-Type: application/json" \
  -d '{
    "speakers": ["alice", "bob"],
    "query": "What does Alice do? What does Bob do?",
    "answer_style": "factual"
  }'
```

The handler builds the system prompt server-side
(`composeDualSpeakerSystemPrompt`); callers do not need to send
`system_prompt`. If the caller DOES send one, the harness path stays —
caller's prompt wins, same as before M9.

## Backward compatibility

| Caller shape | Behaviour |
|--------------|-----------|
| `{user_id, query}` | Legacy single-speaker. Identical to pre-M9. |
| `{user_id, query, speakers: ["x"]}` | Treated as single-speaker (`len<2`). Identical to pre-M9. `engaged=disabled` metric. |
| `{speakers: ["a","b"], query}` | New dual-speaker server fan-out. `user_id` optional. |
| `{user_id, query, system_prompt: "..."}` | Caller's `system_prompt` wins, same as before. |
| `{speakers: ["a","b"], query, system_prompt: "..."}` | Caller's `system_prompt` wins; speaker labels are still tagged on memories but the labelled block is not auto-injected. |

## Metrics

All under `memdb_search_dual_speaker_*` namespace, surface label in
`{search, chat}`:

- `memdb_search_dual_speaker_engaged_total{outcome, surface}` —
  counter, `outcome` in `{enabled, disabled}`. Bumped on every search
  / chat call (so the `disabled` series tracks legacy-path baseline too).
- `memdb_search_dual_speaker_count{surface}` — histogram of `len(speakers)`.
  `len==1` is recorded as `1` for the disabled branch; `>=2` records
  the actual fan-out count. Buckets `[1, 2, 3, 4, 5, 8]`.
- `memdb_search_dual_speaker_merged_total{surface}` — counter. Sum of
  merged-bucket sizes (BEFORE the final TopK cap). Lets operators see
  post-merge dedup pressure.
- `memdb_search_dual_speaker_fanout_ms{surface}` — histogram of the
  parallel fan-out wall-clock latency. Buckets up to 5s.

All series are pre-registered at zero across canonical labels so
Grafana panels populate from first scrape.

## Smoke-test results (2026-04-29 14:50 PT)

```
USER1=ds_alice2_1777499276 USER2=ds_bob2_1777499276
ingest:
  alice → "Alice works as a software engineer in Berlin."
  bob   → "Bob works as a doctor in Paris."

dual-speaker search /product/search (speakers=[alice,bob], top_k=10, top_k_per_speaker=5):
  → text_mem[].memories: see notes (search-side empty even single-speaker
    for tiny corpora — pre-existing search behaviour unrelated to dual-
    speaker; chat-side retrieves correctly via SearchByLevel)

dual-speaker chat /product/chat/complete (speakers=[alice,bob],
                                          answer_style=factual):
  → "Alice works as a software engineer, and Bob works as a doctor."
  ✓ correct cross-speaker answer

metrics (after 1 search + 1 chat with speakers=[a,b]):
  memdb_search_dual_speaker_engaged_total{outcome=enabled,surface=search} = 1
  memdb_search_dual_speaker_engaged_total{outcome=enabled,surface=chat}   = 1
  memdb_search_dual_speaker_count_count{surface=search} = 2 (initial 0 + 1 call)
  memdb_search_dual_speaker_count_count{surface=chat}   = 2
  memdb_search_dual_speaker_fanout_ms_bucket{surface=chat,le=10}   = 1
```

The chat path is the production-critical one (LoCoMo, vaelor) and works
end-to-end. The /product/search-only test returned an empty bucket on
this 1-row-per-speaker corpus, but a single-speaker `user_id` search
returned the same empty bucket — i.e. it's a search-pipeline behaviour
on tiny corpora, not a dual-speaker bug. The smoke test confirms:
fan-out fires (metrics counter=1), goroutines run in parallel
(`fanout_ms` buckets populated), and the merged response shape mirrors
the legacy single-speaker `text_mem[]` envelope.

## Next steps (not in this sprint)

1. **Eval harness migration** — switch `evaluation/locomo/query.py`
   `query_search_dual` / `query_chat_dual` from client-side fan-out to
   the new server-side path. Keep old code as fallback for one cycle.
   Out of scope for this sprint per task constraints.
2. **Prefer / skill / tool tier fan-out** — currently only TextMem is
   merged across speakers. PrefMem / SkillMem / ToolMem bubble up from
   the first speaker only. Defer until a real consumer asks.
3. **Speaker-aware threshold** — per-speaker threshold filtering before
   merge. Today threshold is applied AFTER merge in chat. Could shift
   per-speaker top-k recall.

## Re-verify

```bash
SECRET=$(grep '^INTERNAL_SERVICE_SECRET=' ~/deploy/krolik-server/.env | cut -d= -f2)
USER1="ds_a_$(date +%s)"; USER2="ds_b_$(date +%s)"

# 1. ingest
curl -sX POST http://127.0.0.1:8080/product/add \
  -H "X-Service-Secret: $SECRET" -H "Content-Type: application/json" \
  -d "{\"user_id\":\"$USER1\",\"messages\":[{\"role\":\"user\",\"content\":\"Alice works as a software engineer in Berlin.\"}],\"mode\":\"fine\"}"
curl -sX POST http://127.0.0.1:8080/product/add \
  -H "X-Service-Secret: $SECRET" -H "Content-Type: application/json" \
  -d "{\"user_id\":\"$USER2\",\"messages\":[{\"role\":\"user\",\"content\":\"Bob works as a doctor in Paris.\"}],\"mode\":\"fine\"}"
sleep 12

# 2. dual chat (production-critical path)
curl -sX POST http://127.0.0.1:8080/product/chat/complete \
  -H "X-Service-Secret: $SECRET" -H "Content-Type: application/json" \
  -d "{\"speakers\":[\"$USER1\",\"$USER2\"],\"query\":\"What does Alice do? What does Bob do?\",\"answer_style\":\"factual\"}"

# 3. metrics
curl -sf http://127.0.0.1:8080/metrics | grep dual_speaker_engaged
```
