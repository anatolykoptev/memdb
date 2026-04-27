# MCP Setup — Claude Code, Claude Desktop, Cursor

MemDB exposes a Model Context Protocol (MCP) server at `http://127.0.0.1:8001/mcp`
when the docker-compose stack is running. MCP is a JSON-RPC standard that lets
AI agents call typed tools across processes; once registered, MemDB's memory
tools appear in every conversation in your editor / desktop client.

This directory has copy-paste-ready config for the three most common clients.

## Prerequisites

MemDB stack running locally:

```bash
cd ~/deploy/krolik-server && docker compose up -d
curl http://127.0.0.1:8001/health   # {"status":"ok"}
```

---

## Claude Code (HTTP — recommended)

```bash
claude mcp add memdb-mcp http://127.0.0.1:8001/mcp
```

Verify:

```bash
claude mcp list
# memdb-mcp  http://127.0.0.1:8001/mcp  connected
```

Restart any open Claude Code sessions to pick up the new tools.

---

## Claude Desktop (stdio bridge)

Claude Desktop currently launches MCP servers as subprocesses over stdio.
Use the `mcp-stdio-proxy` binary to bridge stdio → HTTP.

Build the proxy once:

```bash
cd ~/src/MemDB/memdb-go
CGO_ENABLED=0 go build -o ~/bin/mcp-stdio-proxy ./cmd/mcp-stdio-proxy
```

Edit your config file:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

Merge the `mcpServers` section from
[`claude_desktop_config.example.json`](claude_desktop_config.example.json)
into your existing config, then restart Claude Desktop.

---

## Cursor

Cursor reads MCP server definitions from `~/.cursor/mcp.json`. Copy
[`cursor_mcp.example.json`](cursor_mcp.example.json) to that location (or
merge the `mcpServers` block into your existing one) and restart Cursor.

---

## Tools available

The MemDB MCP server exposes 12 tools across four groups. Full reference:
[docs/integrations/claude-code-mcp.md](../../docs/integrations/claude-code-mcp.md).

| Group | Tool |
|---|---|
| Search | `search_memories` |
| Memory CRUD | `add_memory`, `get_memory`, `update_memory`, `delete_memory`, `delete_all_memories` |
| Cubes | `create_cube`, `list_cubes`, `delete_cube`, `get_user_cubes` |
| Users | `create_user`, `get_user_info` |
| Chat | `chat`, `clear_chat_history` |

---

## End-to-end smoke test

After registering the server, open a new conversation in your editor / desktop
client and try:

> Session 1: "Remember that my favorite editor is Helix. Save that to memory."
>
> Session 2 (new conversation): "What is my favorite editor? Search my memory."

Claude should call `add_memory` in session 1 and `search_memories` in
session 2, then answer "Helix" sourced from MemDB.

Verify the round-trip from the shell at any time:

```bash
bash verify.sh
```

This pings `/health` and lists the registered MCP tools via the `tools/list`
JSON-RPC method.
