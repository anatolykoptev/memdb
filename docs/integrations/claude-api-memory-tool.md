# Claude API Memory Tool Adapter (memdb-claude-memory-tool)

`memdb-claude-memory-tool` is a Python package that implements Anthropic's
`BetaAbstractMemoryTool` interface using MemDB as the storage backend. It is a
drop-in replacement for `BetaLocalFilesystemMemoryTool` — swap one line of code and
your Claude API agent gains persistent, multi-tenant, semantically searchable memory.

Source: [github.com/anatolykoptev/memdb-claude-memory-tool](https://github.com/anatolykoptev/memdb-claude-memory-tool)  
Version: v0.1.0 (initial release)

---

## Background: what is memory_20250818?

Anthropic added `memory_20250818` as a beta tool type in the Claude API (SDK v0.52+).
When you pass `tools=[{"type": "memory_20250818", ...}]`, the model emits
`memory_save` / `memory_recall` / `memory_delete` tool calls that a client-side
`BetaAbstractMemoryTool` implementation must handle.

Anthropic ships one reference implementation: `BetaLocalFilesystemMemoryTool`, which
reads and writes JSON files on disk. It is useful for demos but unsuitable for
production: it is single-process, single-user, and has no search capability beyond
exact key lookup.

MemDB provides the production backend.

---

## Why MemDB backend > local filesystem

| Capability | `BetaLocalFilesystemMemoryTool` | `MemDBMemoryTool` (this package) |
|---|---|---|
| Persistence | Files on disk (lost on container restart if not mounted) | PostgreSQL — durable, transactional |
| Multi-tenancy | No — one flat JSON file | Yes — `cube_id` per user, team, or tenant |
| Semantic search | No — exact key lookup only | pgvector MMR + optional cross-encoder rerank |
| Graph relations | No | Apache AGE — connected knowledge graph |
| GDPR / delete | Manual file deletion | `delete_memory` API, `delete_all_memories` for purge |
| Multi-agent sharing | No | Multiple agents share one cube simultaneously |
| Self-hosted jurisdiction | Single machine | Any host running Postgres |
| Key/value `get_by_key` | Yes | Yes (Phase 1 endpoint: `get_memory_by_key`) |
| Prefix scan | No | Yes (`list_memories_by_prefix`) |

---

## Install

```bash
pip install memdb-claude-memory
```

Or pin to the initial release from source:

```bash
pip install git+https://github.com/anatolykoptev/memdb-claude-memory-tool@v0.1.0
```

---

## Quickstart

```python
import anthropic
from memdb_claude_memory import MemDBMemoryTool

client = anthropic.Anthropic()

memory_tool = MemDBMemoryTool(
    memdb_url="http://127.0.0.1:8080",
    cube_id="my-agent",
    user_id="alice",
    service_secret="your-internal-secret",  # optional
)

response = client.beta.messages.create(
    model="claude-opus-4-6",
    max_tokens=1024,
    tools=[memory_tool.to_tool_definition()],
    betas=["memory_20250818"],
    messages=[{"role": "user", "content": "My favorite color is blue. Remember it."}],
)

# Process tool calls from the response
output = memory_tool.process_tool_calls(response.content)
print(output)
```

The model emits a `memory_save` tool call. `process_tool_calls` intercepts it,
POSTs to `/product/add`, and returns the result for you to include in the next turn.

---

## Multi-tenant pattern

One `MemDBMemoryTool` instance per user. Instantiate fresh per request or cache
instances keyed by `user_id`:

```python
def get_memory_tool(user_id: str) -> MemDBMemoryTool:
    return MemDBMemoryTool(
        memdb_url=os.environ["MEMDB_URL"],
        cube_id=f"customer-{user_id}",
        user_id=user_id,
        service_secret=os.environ.get("MEMDB_SECRET"),
    )

# In your request handler:
tool = get_memory_tool(request.user_id)
response = client.beta.messages.create(
    ...,
    tools=[tool.to_tool_definition()],
)
tool.process_tool_calls(response.content)
```

Each `cube_id` is isolated — no data leaks between tenants.

---

## API reference

### `MemDBMemoryTool`

```python
MemDBMemoryTool(
    memdb_url: str,          # MemDB REST API base URL, e.g. "http://127.0.0.1:8080"
    cube_id: str,            # Memory cube ID for this agent/user/tenant
    user_id: str,            # User identifier for memory scoping
    service_secret: str = "",  # X-Internal-Service header value (optional)
    timeout: int = 30,       # HTTP request timeout in seconds
)
```

**Methods:**

| Method | Description |
|---|---|
| `to_tool_definition()` | Returns the `{"type": "memory_20250818", ...}` dict to pass in `tools=` |
| `process_tool_calls(content)` | Handles all memory tool calls in a response `content` block; returns list of tool results |

### Backend endpoints called

| Operation | Endpoint | Notes |
|---|---|---|
| `memory_save` | `POST /product/add` | Adds memory via extraction pipeline |
| `memory_recall` by key | `GET /product/memory/by-key/{key}` | Phase 1 endpoint |
| `memory_recall` by prefix | `GET /product/memory/by-prefix/{prefix}` | Phase 1 endpoint |
| `memory_delete` | `DELETE /product/memory/{id}` | Direct node delete |
| `memory_list` | `GET /product/memory/by-prefix/` | Lists all keys in cube |

---

## Performance

Typical latencies on a local MemDB with ONNX embed-server:

| Operation | Typical latency |
|---|---|
| `memory_save` (extraction) | 200–800 ms (LLM extraction pipeline) |
| `memory_recall` by exact key | 5–20 ms |
| `memory_recall` by prefix | 10–30 ms |
| `memory_delete` | 10–30 ms |

The extraction pipeline (`/product/add`) calls the configured LLM to distill and
structure memories. Latency scales with the LLM provider's TTFT. For fastest saves,
use `async_mode=async` (fire-and-forget; the tool waits only for the queue ack).

---

## Known limitations (v0.1.0)

- **Rename is not atomic.** `memory_rename` (if exposed by a future `memory_20250818`
  beta version) would require a read + delete + write cycle — not a single SQL operation.
- **`str_replace` is not atomic.** The `memory_str_replace` pattern (read → modify → write)
  is two separate HTTP calls. A concurrent write between them wins last-write.
- **No streaming.** `process_tool_calls` blocks until each tool call completes. Use
  `timeout` to bound long-running saves.
- **Phase 1 endpoints only.** `get_memory_by_key` and `list_memories_by_prefix` are
  Phase 1 REST endpoints. Full semantic search is available via the MCP server
  `search_memories` tool but not exposed through the `memory_20250818` tool protocol.

---

## See also

- [MCP server docs](claude-code-mcp.md) — for direct tool access without the
  `memory_20250818` protocol
- [Anthropic SDK docs](https://docs.anthropic.com/en/docs/build-with-claude/memory)
  — `memory_20250818` beta reference
- [MemDB API reference](../API.md) — underlying REST endpoints
