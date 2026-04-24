# Compound Sprint Orchestration Pattern (M7 retro, 2026-04-25)

This is a process doc, not a code change. It mirrors the controller-side skill at
`~/.claude/skills/compound-sprint-orchestration/SKILL.md` so future engineers
working on MemDB (or any multi-stream sprint) can find the pattern via grep
inside the repo, without needing access to the controller's home directory.

## Context

On 2026-04-25, MemDB shipped M7 — a 7-stream compound sprint that lifted LoCoMo
F1 from 0.053 (baseline) to 0.238 (+349%, MemOS-tier) in a single session.
Implementation was 100% subagent-driven; the controller wrote no code.

The orchestration pattern below is what made that possible. It is the
controller's day-long bookkeeping that sits on top of the per-task
`subagent-driven-development` skill.

## Core philosophy

- **Controller writes no code.** Only plans, briefs, verification, and merge
  commits. Every implementation goes through a subagent.
- **Two-stage review per stream.** Spec compliance first (did they build what
  was asked?), code quality second (is the build well-built?). Per-task
  workflow is documented in `superpowers:subagent-driven-development`.
- **Verify before trust.** Every "DONE" report from a subagent is a summary,
  not truth. Before merge: `gh pr diff <N> --name-only` + `gh pr checks <N>` +
  read the actual diff.
- **Disjoint file sets enable parallelism.** Overlapping file sets force
  serialization OR worktree isolation.
- **Controller-only merges.** Subagents stop at `gh pr create`. They never
  `gh pr merge` (confirmed incident 2026-04-24: 7 UU conflicts in disjoint
  files because one parallel agent merged its PR mid-edit of another).

## When to use this pattern

- Plan from `superpowers:writing-plans` has 5+ workstreams
- Streams mix concerns: code + measurement + docs + infra
- Deadline is the current session
- Fan-out is mandatory because serial does not fit the time budget

Do NOT use when:
- Linear single-stream task → `subagent-driven-development` directly
- Long multi-week project → `executing-plans` with human checkpoints
- 2-3 independent small tasks → `dispatching-parallel-agents` alone
- No written plan yet → start with `writing-plans`

## The pattern

```
read-plan
   │
   ▼
extract-tasks ──► todowrite (all streams)
   │
   ▼
disjoint-file-analysis ──► cluster streams (parallel-safe vs serial)
   │
   ▼
┌─ cluster-1 (parallel) ─┐
│  dispatch-implementers │   ◄── worktree isolation if backend
│         │              │
│         ▼              │
│  per-stream:           │
│   spec-review          │
│   code-quality-review  │
│   verify-pr            │
│   merge                │
└────────┬───────────────┘
         │
         ▼
[next cluster] ... loop until all done
         │
         ▼
final-integration-review ──► measurement (if applicable)
         │
         ▼
backlog (`docs/backlog/<date>-followups.md`)
         │
         ▼
memory-bank (surprises, not the diff)
```

## Roles

| Role | Model | Job |
|------|-------|-----|
| TL (Controller) | Opus | Read plan, brief subagents, two-stage review, verify-merge, write backlog. NEVER writes code. |
| Implementer | Opus arch / Sonnet routine | One workstream end-to-end. Stops at `gh pr create`. |
| Spec Reviewer | Sonnet | Read PR diff vs spec; flag missing / extra / misunderstanding. |
| Code Quality Reviewer | Opus | Strengths / Issues (Critical / Important / Minor) / Assessment. |
| Measurement Runner | Sonnet | Re-run benchmarks after each merge; report delta vs baseline. |

## Disjoint file analysis

Before fanning out a cluster:

1. List each stream's expected file set (the plan should already name them)
2. Compute pairwise intersection
3. If any pair overlaps:
   - Serialize the pair (A merges → B starts), OR
   - Use `isolation: "worktree"` on one of them

For uncertain blast radius, use `mcp__go-code__impact_analysis <symbol>` —
returns the true file set the implementer will need to touch (not just the
file with the symbol definition).

Document the cluster decision in the plan itself so the schedule is auditable.

## Worktree isolation

The Agent tool supports `isolation: "worktree"`. Required for:
- Any backend-touching parallel implementer where serialization would blow
  the time budget
- Any pair where impact_analysis shows overlapping files you cannot avoid

Project rule (from `~/CLAUDE.md`): two implementer subagents on the same
checkout simultaneously is forbidden. Worktree isolation is the official
escape hatch. Otherwise strict serial per
`feedback_backend_subagents_serial.md`.

Caveat: long-running background processes started inside a worktree die when
the worktree is reaped. Run measurement / benchmark scripts from the
controller's main checkout instead.

## Verification ritual (before each merge)

1. `gh pr diff <N> --name-only` — confirm scope matches the cluster's
   expected file set (no surprise files)
2. `gh pr checks <N>` — all GREEN
3. `gh pr view <N> --json mergeStateStatus -q '.mergeStateStatus'` — must
   be `CLEAN`, not `UNSTABLE` or `DIRTY`
4. Read the actual diff (`gh pr diff <N>`) — not just the implementer's
   summary

If step 4 surfaces something the spec reviewer missed (it happens),
re-dispatch the implementer with the specific concern. Do not patch it
yourself — context pollution.

## Recovery patterns

- **Subagent reports DONE but CI fails** → re-dispatch the implementer with
  the failure log attached. Do not merge a red PR; do not patch CI from the
  controller.
- **Background process dies mid-sprint** → measurement / benchmark runs live
  in the controller's main session, not inside a worktree subagent.
- **Spec reviewer flags BLOCKER** → re-dispatch the implementer with the
  specific gap; do not merge regardless of how clean the code-quality review
  looks. Spec compliance gates everything.
- **PR title fails commit-lint** → use
  `gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -f title=<lowercase>`.
  `gh pr edit --title` can fail silently when commit-lint hooks reject it.
- **Two clusters' PRs merge in the wrong order and break integration** →
  revert the second-merged PR, fix forward in a follow-up branch. Do not
  force-push.

## Backlog discipline

Single-day sprints are time-boxed. Bar to keep a finding IN scope: "can ship
today AND was in the original plan." Everything else goes to
`docs/backlog/<date>-followups.md`:

- "We noticed X also needs Y" → backlog
- "While reviewing, found Z is also broken" → backlog
- "It would be cleaner if we also W" → backlog

The backlog file is the deal you make with your future self so the sprint
can actually finish. Without it, the controller's session bloats with
half-done side quests and the original plan never closes.

## Memory banking

After the sprint closes, bank the surprises — not the diff itself, but the
WHY behind unexpected outcomes. Examples from M7:

- "Compound improvements were multiplicative + threshold-gated, not additive"
- "Factual prompt was also faster than the verbose one — quality and cost
  moved together"
- "Vaelor uses MemDB only as a store, not as the chat backend"

These accumulate over months and start informing future plans before they
get written. Code diffs live in git; only lessons go in memory.

## Anti-patterns

- "I'll just fix this small thing manually" → context pollution; dispatch
  every fix.
- "Two parallel implementers on the same checkout" → git race per
  `feedback_backend_subagents_serial.md`. Worktree-isolate or serialize.
- "Skip spec review since it's small" → drift accumulates silently across
  streams. Review every time.
- "Merge before measurement" → if the metric regresses, you have nothing to
  roll back to. Measure on the PR branch, then merge.
- "Don't worry about backlog, I'll remember" → you won't. M7 produced 9
  follow-ups; without the file they are lost.
- "Subagent merged its own PR, what's the harm?" → mid-sprint merges from a
  subagent stomp other agents' bases mid-edit. Controller-only merges,
  period.

## Skills this composes

- `superpowers:subagent-driven-development` — the per-stream loop
- `superpowers:dispatching-parallel-agents` — when to fan out vs serialize
- `superpowers:requesting-code-review` /
  `superpowers:receiving-code-review` — review etiquette
- `superpowers:verification-before-completion` — never claim done without
  evidence
- `superpowers:writing-plans` — produces the plan this skill executes
- `superpowers:finishing-a-development-branch` — per-stream close-out

## M7 retro (concrete example)

Sprint goal: lift MemDB LoCoMo F1 from 0.053 baseline to MemOS-tier.

Plan: 7 streams across 3 repos (`MemDB`, `vaelor`, `go-search`).

Outcome: F1 0.053 → 0.238 (+349%), 7 PRs merged, 0 regressions, single
session.

Cluster schedule:

- **Cluster 1** (parallel, disjoint files): factual-prompt (vaelor),
  window-chars (MemDB), threshold-tuning (MemDB ranker)
- **Cluster 2** (serial after C1): chat-eval-script (depends on threshold),
  full-locomo-run (depends on chat-eval)
- **Cluster 3** (parallel, disjoint): docs + memory bank

Key moments the pattern paid off:

1. **window_chars** had two competing designs in the plan (Option A: fixed
   window; Option B: adaptive). Controller wrote a 1-page design note inline
   in the plan instead of dispatching, picked Option A, then dispatched.
   Saved a re-dispatch loop.
2. **Stream B (threshold tuning)** code review caught a bug in the
   chat-eval threshold defaults that would have invalidated all the C2
   measurement runs. Without the controller actually reading the diff
   (verification ritual step 4), the spec reviewer's GREEN would have been
   trusted and the whole afternoon's measurement would have been garbage.
3. **Factual-prompt stream** turned out 2× faster than the verbose baseline
   as a bonus — banked as memory. Future plans now check latency-vs-quality
   jointly rather than assuming a tradeoff.
4. **Backlog file** captured 9 follow-ups: per-domain thresholds, embedder
   quantization, chat-eval English variant, etc. Without it, three of those
   would have started as side quests inside M7 and the sprint would have
   slipped to two days.
