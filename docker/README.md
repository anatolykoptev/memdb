# MemDB Docker — Go Stack Self-Hosting

## Quick start

```bash
# 1. Copy and edit the env file
cp .env.example .env
#    Open .env and set:
#      POSTGRES_PASSWORD — any strong password
#      MEMDB_LLM_API_KEY — your OpenAI (or compatible) API key

# 2. Start the stack
docker compose -f docker/docker-compose.yml up -d

# 3. Verify
curl http://localhost:8080/health
```

Within ~60 seconds memdb-go should respond with `{"status":"ok"}`.

## Services

| Service | Image / Build | Port | Notes |
|---------|--------------|------|-------|
| `postgres` | `pgvector/pgvector:pg17` | `127.0.0.1:5432` | pgvector + Apache AGE pre-installed |
| `memdb-go` | built from `memdb-go/Dockerfile` | `0.0.0.0:8080` | REST + MCP API |
| `embed-server` | `ghcr.io/anatolykoptev/embed-server:latest` | `127.0.0.1:8081` | optional, `--profile embed` |

Postgres is bound to `127.0.0.1:5432` (loopback only). Do not change this to `0.0.0.0` in production without a firewall rule.

## Embedding options

**Option A — embed-server sidecar (recommended for local GPU/CPU):**
```bash
docker compose -f docker/docker-compose.yml --profile embed up -d
```
Set in `.env`:
```
MEMDB_EMBEDDER_TYPE=http
MEMDB_EMBED_URL=http://embed-server:8080
```

**Option B — hosted API (VoyageAI):**
```
MEMDB_EMBEDDER_TYPE=voyage
VOYAGE_API_KEY=pa-...
```

**Option C — Ollama:**
```
MEMDB_EMBEDDER_TYPE=ollama
MEMDB_OLLAMA_URL=http://host.docker.internal:11434
```

## LLM providers

The default is OpenAI-compatible. Any proxy that speaks the OpenAI chat/completions API works:

| Provider | MEMDB_LLM_PROXY_URL |
|----------|-------------------|
| OpenAI | `https://api.openai.com/v1` |
| OpenRouter | `https://openrouter.ai/api/v1` |
| LiteLLM | `http://litellm:4000/v1` |
| Ollama | `http://host.docker.internal:11434/v1` |

Set `MEMDB_LLM_MODEL` to the model name expected by your provider.

## Stopping / cleanup

```bash
# Stop and remove containers (data volumes preserved)
docker compose -f docker/docker-compose.yml down

# Stop and delete all data (irreversible)
docker compose -f docker/docker-compose.yml down -v
```
