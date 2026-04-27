# Claude Integrations

MemDB provides three integration surfaces for Anthropic Claude:

| Integration | Audience | Surface | Use when |
|---|---|---|---|
| **[Claude Code plugin](claude-code-plugin.md)** | Claude Code IDE users | Hooks (auto inject + extract) + slash commands | You use Claude Code daily and want sessions to remember context automatically |
| **[MCP server](claude-code-mcp.md)** | MCP-compatible agents (Claude Code, Cursor, Continue, etc.) | MCP tools (search, add, get, manage cubes) | You build agents or use tools that speak the Model Context Protocol |
| **[Claude API memory tool](claude-api-memory-tool.md)** | Production Claude API agents (Python) | `BetaAbstractMemoryTool` implementation for Anthropic's `memory_20250818` | You use the Claude API directly with `tools=[memory_20250818]` |

All three share one MemDB backend — the same Postgres + AGE graph + vector store powers your
IDE memory, your MCP agents, and your production API agents simultaneously.

---

## Why three surfaces?

Anthropic's Claude ecosystem exposes memory at three different layers:

1. **IDE-level hooks** — Claude Code fires lifecycle events (SessionStart, UserPromptSubmit,
   PreCompact, Stop) that plugins can intercept. The memdb-memory plugin rides these to
   silently inject past context before every prompt and flush new context after every turn.
   No user action required.

2. **Model Context Protocol** — A standard JSON-RPC-over-HTTP protocol supported by Claude
   Code, Cursor, Continue, and any MCP-compatible agent framework. The built-in `memdb-mcp`
   container exposes 12 tools (search, add, CRUD, cube management, user management) that any
   MCP client can call directly.

3. **Claude API tools** — Anthropic's SDK v0.52+ ships `memory_20250818`, a beta tool type
   that lets the model read and write persistent memory as a first-class tool call. Anthropic
   provides the protocol but not the storage backend. `memdb-claude-memory-tool` is a
   drop-in `BetaAbstractMemoryTool` implementation that wires that protocol to MemDB.

---

## The Anthropic memory_20250818 opportunity

Anthropic shipped `memory_20250818` as an abstract API: the SDK defines the interface
(`BetaAbstractMemoryTool`), but every team must implement their own storage backend. The
built-in reference implementation writes to the local filesystem — single-process, single-user,
no semantic search, no GDPR controls.

MemDB is the self-hosted backend that makes that abstract API production-ready:

| Capability | `BetaLocalFilesystemMemoryTool` | MemDB backend |
|---|---|---|
| Persistence | Files on disk | PostgreSQL — survives container restarts |
| Multi-tenancy | No (one flat dir) | Yes — `cube_id` per user, team, or tenant |
| Semantic search | No | pgvector MMR + cross-encoder rerank |
| Graph relations | No | Apache AGE — connected knowledge graph |
| GDPR / delete | Manual file deletion | `delete_memory` / `delete_all_memories` API |
| Self-hosted jurisdiction | Single-machine only | Any Postgres-capable host |
| Multi-agent sharing | No | Multiple agents share one cube |

Because `memdb-claude-memory-tool` implements the same `BetaAbstractMemoryTool` interface,
swapping from local filesystem to MemDB is a one-line change — no prompt engineering, no
API rewrites.

---

## Choosing an integration

```
Using Claude Code IDE?
  └─► Want automatic, zero-config memory?    → Claude Code plugin
  └─► Want to call memory tools explicitly?  → MCP server (add via claude mcp add)

Building an agent with the Claude API (Python)?
  └─► Using tools=[memory_20250818]?         → Claude API memory tool adapter
  └─► Building a custom agent / framework?   → MCP server (HTTP endpoint at :8001/mcp)
```

You can use all three simultaneously — they write to the same store.
