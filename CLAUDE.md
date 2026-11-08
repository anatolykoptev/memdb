# MemDB — Project Rules

Notes for coding agents working in MemDB, a self-hosted long-term memory service for AI agents
written in Go. Every document is indexed in [docs/INDEX.md](docs/INDEX.md).

## Layout

The service is the Go module in `memdb-go/` (`cmd/server`, `cmd/mcp-server`, `cmd/mcp-stdio-proxy`;
packages under `internal/`: `handlers`, `search`, `scheduler`, `db`, `llm`, `embedder`, `config`).
Dependencies are vendored in `memdb-go/vendor/`; never edit them. `docker/` holds the Compose
stack, `deploy/helm/` the chart, `evaluation/` the LoCoMo harness, `examples/` runnable clients,
`docs/` the documents. The `Makefile` is at the repository root and drives `memdb-go`.

## Gate

CI (`.github/workflows/preflight.yml`, job `make preflight`; `ci.yml` adds build and test, a
golangci-lint job, a live-Postgres job and a secret scan) runs, from the repository root:

```sh
make preflight   # go vet, go build, go test, and a gofmt check in memdb-go (GOWORK=off)
make lint        # golangci-lint
```

A pull request that changes only Markdown, `docs/`, the licence or the issue and PR templates skips
the heavy jobs through the `docs-scope` job; that block is canonical across the fleet and is
compared byte for byte, so do not edit it here. Tests tagged `livepg` need Postgres with pgvector
and Apache AGE and the `MEMDB_LIVE_PG_DSN` variable.

## Invariants that are easy to break

- **Versions come from release-please**, which reads Conventional Commits; the package is
  `memdb-go` (tags look like `memdb-go/v0.23.6`). The commit-lint workflow requires a lowercase
  subject. Never edit `.release-please-manifest.json` or tag by hand.
- **Files target 200 lines, hard limit 300.** Split instead of growing a file.
- **No panics in request handlers.** Write failures surface as HTTP errors, never silent drops.
- **This repository is public.** No hostnames, operator paths, account names, tokens or personal
  data in any tracked file, evaluation data and test fixtures included.

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
