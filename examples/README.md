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

## Legacy examples

The remaining subdirectories (`api/`, `basic_modules/`, `core_memories/`, etc.)
demonstrate the Python SDK (`memdb` package) and internal modules.
They require the full Python environment and a locally installed MemDB package.
