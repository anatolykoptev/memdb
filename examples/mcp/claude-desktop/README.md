# MemDB — Claude Desktop Integration (MCP)

Connect MemDB to Claude Desktop so Claude can store and retrieve memories
across conversations using the Model Context Protocol (MCP).

## How it works

MemDB ships a Go MCP server (`memdb-mcp`) that speaks JSON-RPC over stdio.
Claude Desktop launches it as a subprocess and calls its tools directly.

```
Claude Desktop
    │  JSON-RPC (stdio)
    ▼
mcp-server --stdio
    │  HTTP
    ▼
memdb-go :8080   ←→   Postgres + Qdrant
```

## Prerequisites

1. Docker Compose stack running:
   ```bash
   cd ~/deploy/krolik-server && docker compose up -d
   ```
   This starts `memdb-go` (:8080) and `memdb-mcp` (:8001).

2. The `mcp-stdio-proxy` binary on your PATH (bridges Claude's stdio to
   the HTTP MCP server — no Postgres driver needed on the host):
   ```bash
   # Build once (no CGO required)
   cd ~/src/MemDB/memdb-go
   CGO_ENABLED=0 go build -o ~/bin/mcp-stdio-proxy ./cmd/mcp-stdio-proxy
   ```
   Or grab a release binary from the GitHub releases page.

## Configuration

Edit (or create) the Claude Desktop config file:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Linux**: `~/.config/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

Paste the contents of `claude_desktop_config.json.example` into the
`mcpServers` section of your existing config, or use the file as-is if
you don't have one yet.

Then restart Claude Desktop.

## Verify

Open a new conversation in Claude Desktop and ask:

> "Search my memories for programming preferences."

Claude should call the `search_memories` MCP tool. If MemDB is empty you
will see an empty result — that's correct.

## Available MCP tools

| Tool | Description |
|---|---|
| `search_memories` | Semantic search over stored memories |
| `get_memory` | Fetch a single memory by ID |
| `update_memory` | Edit memory text |
| `delete_memory` | Delete a single memory |
| `delete_all_memories` | Wipe an entire cube |
| `add_memory` | Store new memory (proxied to Python pipeline) |
| `chat` | Ask a question with memory-augmented context |
| `create_user` | Register a new user |
| `get_user_info` | Fetch user metadata |
| `create_cube` | Create a memory cube |

## Troubleshooting

**Claude Desktop shows "server disconnected"**
- Confirm `mcp-stdio-proxy` is on PATH: `which mcp-stdio-proxy`
- Confirm MemDB stack is running: `curl http://localhost:8001/health`

**Tools appear but return errors**
- Check `SERVICE_SECRET` matches the value in your `.env`
- View MCP server logs: `docker logs krolik-server-memdb-mcp-1 -f`

## Alternative: direct stdio mode (no proxy)

If you build `mcp-server` locally and have a Postgres client lib available,
you can connect directly without the proxy:

```json
{
  "mcpServers": {
    "memdb": {
      "command": "/path/to/mcp-server",
      "args": ["--stdio"],
      "env": {
        "MEMDB_GO_URL": "http://localhost:8080",
        "SERVICE_SECRET": "your-secret"
      }
    }
  }
}
```
