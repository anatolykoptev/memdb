# MemDB — AI Notes

## Architecture

Pure-Go stack, all in Docker (Python container removed 2026-04-26, Phase 5 ✅):

| Container | Role | Port |
|-----------|------|------|
| `memdb-go` | Go API gateway: auth, ONNX embedder, search, REST API | 8080 |
| `memdb-mcp` | Go MCP server (stdio + streamable-http) | 8001 |
| `embed-server` | Rust ONNX embed sidecar (multilingual-e5-large + jina-code-v2) | 8082 |

Supporting: `postgres+AGE` (5432), `qdrant` (6333), `redis` (6379), `cliproxyapi` (8317), `go-search` (8890). _RabbitMQ removed in Phase 3.3 — Redis Streams used for scheduler queue._

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

MCP tools (all native Go after Phase 5):
`search_memories`, `get_memory`, `update_memory`, `delete_memory`, `delete_all_memories`,
`get_user_info`, `create_user`, `add_memory`, `chat`, `create_cube`, `register_cube`,
`unregister_cube`, `share_cube`, `dump_cube`, `control_memory_scheduler`

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

### Deploy: push to `main` → dozor auto-rebuilds

**Do NOT run `docker compose build` manually.** Push commits to `main` on GitHub — the dozor webhook (`deploy.krolik.run/deploy/github`) rebuilds `memdb-go` and `memdb-mcp` automatically via `~/.dozor/deploy-repos.yaml`. Serial queue — no concurrent builds.

```bash
git push origin main   # triggers dozor rebuild for both memdb-go + memdb-mcp
```

Watch dozor logs: `journalctl --user -u dozor -f | grep memdb`.

Manual `docker compose build` is only allowed for: hot-fix without push, build-flag debugging, or when dozor is down. In those cases use:
```bash
cd ~/deploy/krolik-server && docker compose build memdb-go memdb-mcp && \
  docker compose up -d --no-deps --force-recreate memdb-go memdb-mcp
```

## Key Env Vars (memdb-go / memdb-mcp)

| Var | Description |
|-----|-------------|
| `MEMDB_POSTGRES_URL` | `postgresql://user:pass@host:5432/db` |
| `MEMDB_GO_URL` | URL of memdb-go (used by mcp-server for search proxy) |
| `INTERNAL_SERVICE_SECRET` | Service-to-service auth header value |
| `MEMDB_EMBEDDER_TYPE` | `onnx` (default) or `voyage` |
| `MEMDB_ONNX_MODEL_DIR` | Path to multilingual-e5-large ONNX files |
| `CLI_PROXY_API_KEY` | Key for CLIProxyAPI (Gemini proxy) |
| `AUTH_ENABLED` | Enable Bearer token auth |
| `MASTER_KEY_HASH` | SHA-256 of master API key |

## ONNX Model Optimization

Models in `~/deploy/krolik-server/models/` are **graph-optimized** (O3 fusion: SkipLayerNormalization, Gelu). This gave ~300x speedup on ARM Neoverse-N1.

**When adding or updating ONNX models, ALWAYS optimize before deploying:**

```bash
python3 -c "
from onnxruntime.transformers.optimizer import optimize_model
m = optimize_model('model_quantized.onnx', model_type='bert', num_heads=NUM_HEADS, hidden_size=HIDDEN_SIZE)
m.save_model_to_file('model_optimized.onnx')
"
# num_heads/hidden_size: e5-large=16/1024, jina-code-v2=12/768, e5-small=12/384
```

Without optimization, inference takes ~47s/request instead of ~0.15s on ARM (no AVX).

## Go Migration Status

Phase 5 ✅ complete 2026-04-26 — Python container removed. See `docs/ROADMAP-GO-MIGRATION.md` for full history.

All endpoints and MCP tools are native Go. Python code in `src/` is retained for reference only (multi-modal parsers, markitdown, deepsearch_agent) — not executed in production.

## Module

`github.com/MemDBai/MemDB/memdb-go` · Go 1.26 · MCP SDK: `github.com/modelcontextprotocol/go-sdk`
