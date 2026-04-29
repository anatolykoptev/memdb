# MemDB — Project Rules

## Eval policy

**Full-corpus LoCoMo runs (`LOCOMO_FULL=1`, all 10 conversations / 1986 QAs) require EXPLICIT user approval each time.** Never launch a full-corpus run autonomously — including in `<<autonomous-loop-dynamic>>` mode, after sweep "verdicts", post-fix verification, or any other context. A full run takes ~4 hours and consumes LLM budget; the user gates it.

Allowed without explicit approval:
- `LOCOMO_FULL=0` chat-50 stratified runs (50 QAs, ~5–7 min)
- A/B sweeps over chat-50 (multiple variants, ~30–60 min)
- Single-row smoke tests via direct `/product/add` + SQL probe
- `score.py` re-scoring of an already-produced predictions JSON

Forbidden without explicit approval (must ask, must wait):
- `LOCOMO_FULL=1 ./run.sh` in any form
- Re-ingesting the full 10-conv corpus
- Mass re-extraction/backfill jobs

If a fix or sweep result strongly suggests a full run is the right next step, say so in plain text and wait. Do not pre-emptively start it "in the background to save time."

## Add-mode contract

| Mode | Use case | LLM |
|------|----------|-----|
| **raw** | Pre-structured payloads (JSON, logs, per-message traces) — preserve verbatim | NEVER |
| **fast** | Conversation batches, windowed chunks, no graph | NEVER |
| **fine** | New turns, full graph (entities, atomic facts, edges) | sync |

`raw` = no-LLM zone (`a82c99f7`, 2026-04-10). Need atomic / graph enrichment? Switch to `fine` (or `fast`). Don't add background fillers / async extractors / "lazy fine on raw" — that breaks the contract. Opt-in via per-request flag only, never default-on.
