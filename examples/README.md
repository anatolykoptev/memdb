# MemDB Examples

All examples assume MemDB is running locally:

```bash
cd ~/deploy/krolik-server && docker compose up -d
```

The Go API gateway listens on `http://localhost:8080`.

## Quickstarts

| Directory | Language | What it does |
|---|---|---|
| [`python/quickstart/`](python/quickstart/) | Python 3 | Adds 3 memories and searches them via `requests` |
| [`go/quickstart/`](go/quickstart/) | Go (stdlib only) | Same flow using `net/http` + `encoding/json` |
| [`mcp/claude-desktop/`](mcp/claude-desktop/) | Config + docs | Registers MemDB as an MCP server in Claude Desktop |

## Integration paths

End-to-end runnable samples for the three primary integration paths:

| Directory | Path | What it does |
|---|---|---|
| [`python_chat/`](python_chat/) | Claude API + adapter | Python chat with persistent memory across two `tool_runner` sessions |
| [`go_client/`](go_client/) | Pure Go HTTP | Stdlib-only client: add + search via `/product/*` |
| [`mcp_setup/`](mcp_setup/) | MCP server | Registration for Claude Code, Claude Desktop, Cursor + `verify.sh` |
