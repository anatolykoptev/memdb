# Python Chat with Persistent Memory

End-to-end example: Claude API + `memdb-claude-memory-tool` adapter.

Demonstrates that memory **persists across separate conversations**: in turn 1 we
tell Claude something; in turn 2 (a brand-new `tool_runner` invocation with no
shared message history) Claude recalls it via the MemDB-backed memory tool.

## What it does

1. Connectivity check against `MEMDB_URL/health`.
2. Creates a `MemDBMemoryTool` instance scoped to one `cube_id` / `user_id`.
3. Session 1 — sends "Remember that I prefer TypeScript over Python." Claude
   issues `memory_save` tool calls; the adapter forwards them to MemDB.
4. Session 2 — fresh `tool_runner` call (no in-memory chat history): asks
   "What language do I prefer?". Claude issues `memory_recall`; MemDB returns
   the stored preference; Claude answers from it.

## Prerequisites

- Python 3.10+
- MemDB running locally (`http://localhost:8080`):
  ```bash
  cd ~/deploy/krolik-server && docker compose up -d
  ```
- An Anthropic API key.

## Configure

```bash
cp .env.example .env
# edit .env — set ANTHROPIC_API_KEY at minimum
export $(grep -v '^#' .env | xargs)
```

## Install and run

```bash
pip install -r requirements.txt
python3 chat_with_memory.py
```

## Expected output

```
MemDB reachable at http://localhost:8080

Session 1 (store preference):
Claude: I've stored your preference for TypeScript over Python.

Session 2 (fresh thread, recall):
Claude: You prefer TypeScript over Python.
```

The second answer comes from MemDB — there is no shared context between the
two `tool_runner` calls.

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | required | Anthropic API key for Claude |
| `MEMDB_URL` | `http://localhost:8080` | MemDB REST API base URL |
| `CUBE_ID` | `python-chat-demo` | Memory cube identifier |
| `USER_ID` | `python-chat-demo` | User identifier inside the cube |
| `MEMDB_SERVICE_SECRET` | empty | `X-Internal-Service` header value (only needed if MemDB has `AUTH_ENABLED=true`) |

## See also

- [docs/integrations/claude-api-memory-tool.md](../../docs/integrations/claude-api-memory-tool.md)
- [`memdb-claude-memory-tool` repo](https://github.com/anatolykoptev/memdb-claude-memory-tool)
