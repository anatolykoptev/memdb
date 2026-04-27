# Claude Code Plugin (memdb-memory)

The `memdb-memory` plugin makes Claude Code sessions memory-aware with no user
action required. It intercepts four lifecycle hooks to inject relevant past context
before every prompt, and to flush new context to MemDB after every turn.

Source: [github.com/anatolykoptev/claude-code-memdb-plugin](https://github.com/anatolykoptev/claude-code-memdb-plugin)

---

## What it does

### Four hooks

| Hook | Event | What it does | Latency |
|---|---|---|---|
| `memdb-healthcheck` | `SessionStart` | Tests MemDB connectivity; shows connection status or warns that memory is disabled for this session | <1 s |
| `memdb-inject` | `UserPromptSubmit` | Searches MemDB for the top 5 most relevant memories; injects them as `<user_memory_context>` in `additionalContext` before Claude sees the prompt | <500 ms (30 s timeout, fails silently) |
| `memdb-precompact` | `PreCompact` | Reads the last 50 messages from the conversation transcript (JSONL) and sends them to MemDB for server-side extraction before context is compacted | <30 s (never blocks compaction) |
| `memdb-stop` | `Stop` | Sends the transcript delta to MemDB after meaningful turns (gated on signal words: decisions, fixes, etc.) | fire-and-forget |

### One slash command

| Command | Description |
|---|---|
| `/memory-search <query>` | Manually search MemDB memories with an explicit query |

### One skill

`memory-context` — guides Claude on how to interpret injected memory and answer
memory-related questions.

---

## Prerequisites

- MemDB running and accessible (default: `http://127.0.0.1:8080`). See the
  [Quick Start](../../README.md#quick-start-5-minutes).
- Claude Code IDE installed.
- Node.js 18+ (for the `fetch` API used by hooks).

---

## Install

The plugin lives in a separate repository. Clone it and install locally:

```bash
git clone https://github.com/anatolykoptev/claude-code-memdb-plugin
claude plugins install ./claude-code-memdb-plugin
```

Or install directly from the MemDB local marketplace if you have it registered:

```bash
claude plugin install memdb-memory@memdb-local
```

---

## Setup

After install, run the interactive setup script once:

```bash
bash "$(claude plugins dir)/memdb-memory/setup.sh"
```

The script prompts for your MemDB URL, user ID, cube ID, and optional service secret.
It tests connectivity, then writes `~/.config/claude-code-memdb/config.env` (chmod 600).

---

## Configuration

The plugin reads config in this precedence order (highest wins):

1. Environment variables set before starting Claude Code.
2. `~/.config/claude-code-memdb/config.env` written by `setup.sh`.

| Variable | Default | Description |
|---|---|---|
| `MEMDB_API_URL` | `http://127.0.0.1:8080` | MemDB API base URL |
| `MEMDB_USER_ID` | `memos` | User identifier for memory scoping |
| `MEMDB_CUBE_ID` | `memos` | Memory cube identifier |
| `INTERNAL_SERVICE_SECRET` | *(empty)* | Sent as `X-Internal-Service` header for auth |

**Config file format** (`~/.config/claude-code-memdb/config.env`):

```env
MEMDB_API_URL=http://127.0.0.1:8080
MEMDB_USER_ID=alice
MEMDB_CUBE_ID=work
INTERNAL_SERVICE_SECRET=your-secret
```

---

## Hook reference

### memdb-healthcheck (SessionStart)

```
Session start → Load config → GET /health → Inject status message
```

Sends a `GET /health` request with a 3-second timeout. On success, adds a one-line
status message so you know memory is active. On failure, warns that memory features
are disabled for this session — Claude continues normally without memory.

### memdb-inject (UserPromptSubmit)

```
User prompt → extract query → POST /product/search (top 5, mmr, 0.85 threshold) → inject as additionalContext
```

Short/casual prompts are skipped automatically (`hi`, `ok`, `yes`, etc. under ~20 chars).
For substantive prompts it fetches the 5 most relevant memories using MMR deduplication
and a 0.85 relativity threshold, then injects them as a `<user_memory_context>` XML block
that Claude sees in context. The hook never throws — any error is silently discarded so
the prompt is never blocked.

### memdb-precompact (PreCompact)

```
Transcript (JSONL) → read last 50 messages → POST /product/add (extraction mode) → MemDB stores distilled facts
```

Before Claude Code compacts the context window, this hook sends the conversation to
MemDB for server-side LLM extraction. Important facts, decisions, and preferences
survive the compaction. The hook never blocks the compaction even if MemDB is slow or
unreachable.

### memdb-stop (Stop)

```
Transcript delta → heuristic gate (signal words) → POST /product/add → fire-and-forget
```

Signal words that trigger a save: `decided`, `fixed`, `implemented`, `added`, `changed`,
`prefer`, `always`, `never`, `use`, `don't`, `important`. Fire-and-forget — the response
is never awaited.

---

## Troubleshooting

**MemDB unreachable at session start**

The healthcheck will warn, and memory features will be skipped for the session. Check:
```bash
curl http://127.0.0.1:8080/health
# expected: {"status":"ok"}
```
If down, start MemDB: `docker compose -f docker/docker-compose.yml up -d`.

**Auth fails (403 on /product/search)**

If MemDB requires a service secret, set `INTERNAL_SERVICE_SECRET` in the config file
or environment. The hook sends it as the `X-Internal-Service` header.

**No context injected despite MemDB being up**

Possible causes:
- The prompt was classified as casual (too short or matched a skip pattern).
- No memories exist yet — add some with `/memory-search` or let `memdb-stop` capture
  a few turns first.
- The relativity threshold (0.85) is too strict for your memories. This is not currently
  configurable per-user without editing the hook script directly.

**Slow prompts**

The `memdb-inject` hook has a 30-second timeout but typically completes in under 500 ms
on a local MemDB. If you see consistent delays, check the embed-server sidecar:
```bash
docker compose -f docker/docker-compose.yml ps embed-server
```
