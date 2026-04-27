# Pure Go HTTP Client for MemDB

A tiny example of talking to MemDB from Go using only the standard library
(`net/http`, `encoding/json`). No SDK, no third-party dependencies.

## What it does

1. `add()` — POSTs a short conversation to `/product/add` (sync mode) so the
   memory is durable before the next call returns.
2. `search()` — POSTs a semantic query to `/product/search` and prints the
   top results.

This mirrors the contract documented in [docs/API.md](../../docs/API.md).

## Prerequisites

- Go 1.21+
- MemDB running locally:
  ```bash
  cd ~/deploy/krolik-server && docker compose up -d
  ```

## Run

```bash
go run .
```

Or build a static binary:

```bash
go build -o memdb-client .
./memdb-client
```

> **Monorepo users:** if a parent directory has a `go.work` file, prefix
> with `GOWORK=off`: `GOWORK=off go run .`

## Configure

| Variable | Default | Purpose |
|---|---|---|
| `MEMDB_URL` | `http://localhost:8080` | Base URL of the MemDB REST API |
| `MEMDB_API_KEY` | empty | Bearer token (only when `AUTH_ENABLED=true`) |
| `MEMDB_SERVICE_SECRET` | empty | Alternative `X-Service-Secret` header |
| `MEMDB_USER_ID` | `go-client-demo` | User identifier |
| `MEMDB_CUBE_ID` | `go-client-demo` | Cube identifier |

## Expected output

```
MemDB Go client → http://localhost:8080

[add]    stored conversation about hiking
[search] query: "outdoor activities"
  1. score=0.92  User enjoys mountain hiking on weekends.
done.
```

If `[search]` prints zero results, the embedder is still indexing — re-run
after a second or two. (`async_mode=sync` blocks on the LLM extract step but
the embed pipeline is briefly eventually-consistent.)
