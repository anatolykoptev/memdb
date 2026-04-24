# Sliding-Window Size — Configurable per Request (M7 Stream C)

**Date:** 2026-04-25 · **Status:** Decided · **Decision:** Option A (additive opt-in param)

## Status quo

`windowChars = 4096` is a package constant in `memdb-go/internal/handlers/add.go:31`.
Consumed by `extractFastMemories` in `add_windowing.go`, which is called from
`add_async.go:48` (async path) and `add_fast.go:36` (sync `mode=fast`). Every
sliding window glues messages until ~4096 chars are accumulated, with an 800-char
overlap between consecutive windows.

## Why it matters now

M6 prompt-ablation found that the 4096-char window collapses 58 LoCoMo session
messages into ~3 coarse memories. Question-form embeddings then miss atomic
facts because each chunk is too dense. M7 Stream B routes the harness through
`mode=raw` (one memory per message), but production traffic still defaults to
`mode=fast`/buffer, so window granularity remains a real-world tuning lever.

## Who depends on 4096 today (quick survey)

- **vaelor** (`pkg/memory/memdb.go:125,165`): `Store` and `addMemory` both send
  `mode=fast` with no `window_chars`. Implicit reliance on the default.
- **go-nerv**: no `/product/add` calls found (uses MemDB only via search).
- **oxpulse-chat**: no `/product/add` calls found.

→ Only one client (vaelor) currently writes to `/product/add`, and it sends
`mode=fast` with no window override. Any default change would touch that client.

## Three options

**A — additive opt-in (`window_chars` request param, default unchanged 4096).**
Zero break risk. Lets us A/B per request to measure production impact before
moving the default. Cost: one nullable field on the request struct + 4 lines of
plumbing.

**B — default-shift (lower the constant, e.g. 2048).** Existing clients with
implicit reliance (vaelor) silently get smaller windows. Higher break risk; can
only be considered after measurement evidence lands.

**C — mode-pair (new `"fast-qa"` mode with `windowChars=512`).** Adds API
surface (a fourth mode value), zero risk to existing clients. Plumbing is twice
the size of Option A and complicates `addMode` metric labelling.

## Decision

**Option A.** Rationale:

1. M6/M7 evidence is harness-only so far. Production impact unknown.
2. Per-request opt-in is the minimum surface area that lets us measure before
   committing.
3. vaelor (the only producer of `/product/add` traffic today) is unaffected —
   it ignores `window_chars`, gets the unchanged 4096 default.
4. Revisit Option B in M8 once we have measured production data from
   vaelor/harness running with smaller windows.

## Out of scope (this PR)

- Changing the default (Option B, deferred to M8).
- Adding `fast-qa` mode (Option C, deferred — same data needed first).
- Making `overlapChars` configurable (proportional to default; defer).
- Refactoring `extractFastMemories` beyond adding the param.
