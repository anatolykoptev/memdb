# MCP Server (memdb-mcp)

MemDB ships a built-in MCP server that exposes memory operations as standard
[Model Context Protocol](https://modelcontextprotocol.io/) tools. Any MCP-compatible
agent — Claude Code, Cursor, Continue, custom Python agents — can call these tools
without any MemDB-specific SDK.

---

## What is MCP?

The Model Context Protocol is a JSON-RPC-over-HTTP (or stdio) standard that lets AI
agents call typed tools with JSON Schema-validated parameters. Claude Code supports
MCP natively. Register a server once; the tools appear in every session.

---

## Registration

### Claude Code (HTTP, recommended for local MemDB)

```bash
claude mcp add memdb http://127.0.0.1:8001/mcp
```

That's it. Restart Claude Code; the `memdb` server and its tools are available.

Verify registration:

```bash
claude mcp list
# memdb  http://127.0.0.1:8001/mcp  connected
```

### Claude Desktop (stdio proxy)

Build the stdio proxy binary:

```bash
cd memdb-go
CGO_ENABLED=0 go build -o ~/bin/mcp-stdio-proxy ./cmd/mcp-stdio-proxy
```

Copy `examples/mcp/claude-desktop/claude_desktop_config.json.example` into your
Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json`
on macOS) and restart. Full walkthrough:
[examples/mcp/claude-desktop/README.md](../../examples/mcp/claude-desktop/README.md).

---

## Tool catalog

The MCP server exposes 12 tools across four groups.

### Memory search

| Tool | Description | Key parameters |
|---|---|---|
| `search_memories` | Semantic search across one or more cubes | `query` (required), `user_id`, `cube_ids[]`, `profile` (`inject`/`default`/`deep`), `top_k` (default 6), `relativity` (0–1, default 0.85), `dedup` (`mmr`/`sim`/`no`) |

### Memory CRUD

| Tool | Description | Key parameters |
|---|---|---|
| `add_memory` | Add memories from raw text or a document file | `user_id`, `memory_content`, `doc_path`, `mem_cube_id`, `source`, `session_id` |
| `get_memory` | Retrieve a single memory node by ID | `cube_id`, `memory_id`, `user_id` |
| `update_memory` | Overwrite the content of a memory node | `cube_id`, `memory_id`, `memory_content`, `user_id` |
| `delete_memory` | Delete a single memory node (Postgres + Qdrant) | `cube_id`, `memory_id`, `user_id` |
| `delete_all_memories` | Purge all memories in a cube | `cube_id`, `user_id` |

### Cube management

| Tool | Description | Key parameters |
|---|---|---|
| `create_cube` | Create a new memory cube | `cube_name`, `owner_id`, `cube_id` (optional custom ID) |
| `list_cubes` | List cubes accessible to a user | `user_id` |
| `delete_cube` | Delete a cube and all its memories | `cube_id`, `user_id` |
| `get_user_cubes` | Get all cubes owned by or shared with a user | `user_id` |

### User management

| Tool | Description | Key parameters |
|---|---|---|
| `create_user` | Register a user | `user_id`, `role` (`USER`/`ADMIN`), `user_name` |
| `get_user_info` | Get user metadata and accessible cubes | `user_id` |

### Chat (proxy)

| Tool | Description | Key parameters |
|---|---|---|
| `chat` | Send a memory-augmented chat query | `user_id`, `query`, `mem_cube_id`, `system_prompt` |
| `clear_chat_history` | Clear the chat history for a user | `user_id` |

---

## Multi-tenancy via cube_id

Every memory lives in a cube. Use `cube_id` to isolate tenants:

```
cube_id=acme-corp   ← all ACME customer memories
cube_id=beta-inc    ← all Beta Inc customer memories
cube_id=personal    ← your own IDE context
```

A single `user_id` can access multiple cubes. Pass `cube_ids: ["acme-corp", "shared"]`
to `search_memories` to fan out across cubes in one call.

---

## Authentication

The MCP server supports two auth mechanisms:

**Service secret (internal services)**

Pass the secret in the `X-Internal-Service` header. Set `MEMDB_INTERNAL_SERVICE_SECRET`
in `.env` to require it server-side.

**Bearer token**

Standard `Authorization: Bearer <token>` is also accepted for external clients.

For local single-user setups with no inbound traffic, auth can be left unconfigured.

---

## Deployment

The `memdb-mcp` container starts automatically with the default compose:

```bash
docker compose -f docker/docker-compose.yml up -d
# memdb-mcp listens on :8001
```

Ports used:

| Port | Service |
|---|---|
| `8001` | MCP HTTP endpoint (`/mcp`) |
| `8080` | memdb-go REST API (used internally by MCP tools) |

The MCP server proxies `add_memory`, `chat`, and `search_memories` to `memdb-go`
(`/product/add`, `/product/chat/complete`, `/product/search`). All other tools (CRUD,
cubes, users) call Postgres directly — no extra hop.

---

## Example agent usage (Python, MCP client)

```python
import anthropic

client = anthropic.Anthropic()

# Claude Code handles MCP tool calls automatically.
# For custom agents, use the MCP client SDK:
from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client

async with streamablehttp_client("http://127.0.0.1:8001/mcp") as (read, write, _):
    async with ClientSession(read, write) as session:
        await session.initialize()
        result = await session.call_tool(
            "search_memories",
            {"query": "user's payment preferences", "user_id": "alice", "cube_ids": ["acme"]},
        )
        print(result.content)
```
