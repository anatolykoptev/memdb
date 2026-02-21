# MemDB — AI Notes

## Architecture

Two Go binaries + one Python legacy service, all in Docker:

| Container | Role | Port | Status |
|-----------|------|------|--------|
| `memdb-go` | Go API gateway: auth, ONNX embedder, search, REST API | 8080 | ✅ Go |
| `memdb-mcp` | Go MCP server (stdio + streamable-http) | 8001 | ✅ Go |
| `memdb-api` | Python FastAPI: add pipeline, LLM extraction, scheduler | 8000 | ⚠️ Legacy |

Supporting: `postgres+AGE` (5432), `qdrant` (6333), `redis` (6379), `rabbitmq` (5672), `cliproxyapi` (8317), `go-search` (8890).

Postgres is **not exposed** to host — only accessible inside Docker network.

## Code Layout

```
memdb-go/           ← all active Go code
  cmd/
    mcp-server/     ← MCP server binary (--stdio flag or HTTP mode)
    mcp-stdio-proxy/← stdio→HTTP bridge (for Claude Code integration)
    server/         ← main API gateway binary
  internal/
    config/         ← env-based Config struct (all env vars documented here)
    handlers/       ← REST handlers: add, search, memory, users, embeddings, llm
    mcptools/       ← MCP tools: search (native), memory/users (native Postgres), proxy tools (→ Python)
    search/         ← search pipeline: embed → Qdrant+Postgres → merge → rerank → dedup
    embedder/       ← ONNX (multilingual-e5-large, dim=1024) + VoyageAI fallback
    db/             ← pgx Postgres, Qdrant gRPC, Redis clients
    server/         ← HTTP server, auth middleware, rate limiting, OTel
src/                ← Python legacy (MemDB package), being phased out
```

## MCP Server

`memdb-mcp` supports two transports:
- **HTTP** (default): streamable-http on `:8001/mcp`
- **STDIO**: `memdb-mcp --stdio` — logs go to stderr, JSON-RPC on stdout

For Claude Code integration, use **stdio proxy** (avoids Postgres dependency on host):
```
/home/krolik/bin/mcp-stdio-proxy --url http://127.0.0.1:8001/mcp
```

MCP tools split:
- **Native Go** (Postgres direct): `search_memories`, `get_memory`, `update_memory`, `delete_memory`, `delete_all_memories`, `get_user_info`, `create_user`
- **Proxied → Python**: `add_memory`, `chat`, `create_cube`, `register_cube`, `unregister_cube`, `share_cube`, `dump_cube`, `control_memory_scheduler`

## Build

```bash
# Build MCP server
cd memdb-go && CGO_ENABLED=0 go build -o mcp-server ./cmd/mcp-server

# Build API gateway (requires CGO for ONNX)
cd memdb-go && go build -o memdb-go ./cmd/server

# Build stdio proxy (no CGO)
cd memdb-go && CGO_ENABLED=0 go build -o /home/krolik/bin/mcp-stdio-proxy ./cmd/mcp-stdio-proxy

# Run tests
cd memdb-go && go test ./internal/...
```

Docker (from `/home/krolik/krolik-server`):
```bash
docker compose build memdb-mcp && docker compose up -d memdb-mcp
docker compose build memdb-go && docker compose up -d memdb-go
```

## Key Env Vars (memdb-go / memdb-mcp)

| Var | Description |
|-----|-------------|
| `MEMDB_POSTGRES_URL` | `postgresql://user:pass@host:5432/db` |
| `MEMDB_GO_URL` | URL of memdb-go (used by mcp-server for search proxy) |
| `MEMDB_PYTHON_URL` | URL of memdb-api Python backend |
| `INTERNAL_SERVICE_SECRET` | Service-to-service auth header value |
| `MEMDB_EMBEDDER_TYPE` | `onnx` (default) or `voyage` |
| `MEMDB_ONNX_MODEL_DIR` | Path to multilingual-e5-large ONNX files |
| `CLI_PROXY_API_KEY` | Key for CLIProxyAPI (Gemini proxy) |
| `AUTH_ENABLED` | Enable Bearer token auth |
| `MASTER_KEY_HASH` | SHA-256 of master API key |

## Go Migration Status

See `ROADMAP-GO-MIGRATION.md` for full plan. Summary:
- **Done**: auth, ONNX embedder, search pipeline, REST CRUD, MCP server, stdio transport
- **In progress (Phase 1)**: Go-native `add` pipeline (LLM extraction, dedup, graph insert) — currently proxied to Python
- **Remaining**: memory reorganizer, skill/tool memory, Python deprecation

## Module

`github.com/MemDBai/MemDB/memdb-go` · Go 1.26 · MCP SDK: `github.com/modelcontextprotocol/go-sdk`
