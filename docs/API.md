# MemDB HTTP API Reference

> **v0.23.0** — base URL: `http://localhost:8080` (default port; override with `MEMDB_GO_PORT`).
> All memory/chat/cube endpoints live under `/product/`. Embeddings live under `/v1/`.
> Machine-readable spec: [memdb-go/api/openapi.yaml](../memdb-go/api/openapi.yaml) — Swagger UI at `/docs`.

---

## Quick Start

```bash
# 1. Start MemDB (requires docker)
git clone https://github.com/anatolykoptev/memdb && cd memdb
cp .env.example .env   # set POSTGRES_PASSWORD at minimum
docker compose -f docker/docker-compose.yml up -d

# 2. Verify it's running
curl http://localhost:8080/health
# {"status":"ok","ready":true}

# 3. Store a memory
curl -s -X POST http://localhost:8080/product/add \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "writable_cube_ids": ["alice-personal"],
    "messages": [
      {"role": "user", "content": "I just moved to Berlin and work as a software engineer."},
      {"role": "assistant", "content": "Got it, I will remember that."}
    ],
    "async_mode": "sync"
  }'

# 4. Search it back
curl -s -X POST http://localhost:8080/product/search \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice", "readable_cube_ids": ["alice-personal"], "query": "where does Alice live?", "top_k": 5}'
```

---

## Authentication

Auth is **disabled by default** (`AUTH_ENABLED=false`). When enabled, every request to
`/product/*` must include one of these two headers. Either is sufficient.

### Bearer token (user-facing)

```
Authorization: Bearer <api_key>
```

The server validates by computing `SHA-256(api_key)` and constant-time comparing it against
`MASTER_KEY_HASH`. Never store the raw key; store the hex digest in `MASTER_KEY_HASH`.

Generate a key pair:
```bash
API_KEY=$(openssl rand -hex 32)
MASTER_KEY_HASH=$(echo -n "$API_KEY" | sha256sum | cut -d' ' -f1)
echo "API_KEY=$API_KEY"
echo "MASTER_KEY_HASH=$MASTER_KEY_HASH"
```

### Service secret (internal calls)

```
X-Service-Secret: <secret>
```

Matches `INTERNAL_SERVICE_SECRET` env var directly (no hashing). Intended for service-to-service
calls where rotating a hash is not practical. The legacy header `X-Internal-Service` is also
accepted for backward compatibility.

### Auth-exempt routes

The following paths bypass auth even when `AUTH_ENABLED=true`:

| Path | Reason |
|------|--------|
| `GET /health` | Container liveness probes |
| `GET /ready` | Container readiness probes |
| `GET /metrics` | Prometheus; bound to `127.0.0.1` in docker-compose |
| `POST /v1/embeddings` | Internal embed pipeline |
| `GET /debug/pprof/*` | Guarded separately by `X-Service-Secret` only |
| `OPTIONS *` | CORS preflight |

### Auth error responses

| Condition | Status | Body |
|-----------|--------|------|
| Missing `Authorization` header | 401 | `{"code":401,"message":"missing Authorization header","data":null}` |
| Wrong header format (not `Bearer …`) | 401 | `{"code":401,"message":"invalid Authorization header format, expected: Bearer <token>","data":null}` |
| Token hash mismatch | 403 | `{"code":403,"message":"invalid API key","data":null}` |

---

## Common Headers

| Header | Required | Value |
|--------|----------|-------|
| `Content-Type` | Yes (POST) | `application/json` |
| `Authorization` | When auth enabled | `Bearer <api_key>` |
| `X-Service-Secret` | Alt to Bearer | raw secret string |
| `Accept` | Chat stream only | `text/event-stream` |

---

## Error Format

All errors follow the same envelope:

```json
{
  "code": 400,
  "message": "user_id is required",
  "data": null
}
```

Validation errors (missing required fields) return `422` with a detail array from the upstream
OpenAPI validator, or `400` with the Go handler's own message. `503` means a required
dependency (postgres, embedder, LLM) is not configured.

---

## Endpoints

---

### Memory CRUD

#### `POST /product/add` — Store memories

Ingests conversation messages (or plain text) and extracts long-term memories.

**Request body:**

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `user_id` | string | Yes | — | The person whose memory is being stored |
| `messages` | array | Yes* | — | Conversation turns (see format below) |
| `writable_cube_ids` | string[] | No | — | Cube(s) to write to. Omit to use the user's default cube |
| `async_mode` | `"async"` \| `"sync"` | No | `"async"` | `async` enqueues via Redis Streams and returns immediately; `sync` blocks until done |
| `mode` | `"fast"` \| `"fine"` \| `"raw"` | No | `null` | Ingest pipeline. See **mode notes** below |
| `window_chars` | int | No | `4096` | Sliding window character budget for fast/async mode. Range: `[128, 16384]` |
| `session_id` | string | No | `"default_session"` | Groups related messages for soft-filtering in search |
| `custom_tags` | string[] | No | — | Tags to attach: `["Travel", "family"]`. Usable as search filters |
| `info` | object | No | — | Arbitrary metadata: `{"source_type":"web","source_url":"https://..."}` |
| `agent_id` | string | No | — | Agent identifier for multi-agent deployments |
| `task_id` | string | No | — | Client-assigned ID for async task monitoring via `/product/scheduler/status` |
| `is_feedback` | bool | No | `false` | Marks as feedback input (uses feedback extraction pipeline) |

**`messages` format** — array of OpenAI-compatible message objects:

```json
[
  {"role": "user", "content": "I prefer vegetarian food.", "chat_time": "2026-04-25T10:00:00Z"},
  {"role": "assistant", "content": "Noted, I'll remember your food preferences."}
]
```

`chat_time` is optional but improves temporal memory extraction (`MEMDB_DATE_AWARE_EXTRACT`, on by default).

**Mode notes:**
- `mode=raw` — Store the conversation window verbatim as a raw LTM node, no LLM extraction. ~30–50 ms p95. Best for high-throughput ingest where recall quality is less critical.
- `mode=fast` + `async_mode=sync` — Sliding-window extraction without LLM dedup. Uses `window_chars` to chunk. p95 ≈ 1.2 s with default window.
- `mode=fine` + `async_mode=sync` — Full LLM fact extraction with dedup. Highest quality. p95 ≈ 5–10 s depending on conversation length.
- Default (`async_mode=async`) — Enqueues to Redis Streams; consumer runs `fine` pipeline in background. Returns immediately with a `task_id` to poll.

**Example — async add (default):**

```bash
curl -s -X POST http://localhost:8080/product/add \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "writable_cube_ids": ["alice-personal"],
    "messages": [
      {"role": "user", "content": "My sister Emma lives in Prague and is a doctor."},
      {"role": "assistant", "content": "I will remember that about Emma."}
    ]
  }'
```

**Example — sync raw add (fast ingest):**

```bash
curl -s -X POST http://localhost:8080/product/add \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "writable_cube_ids": ["alice-personal"],
    "messages": [{"role": "user", "content": "Just watched Dune Part Two, loved it."}],
    "async_mode": "sync",
    "mode": "raw"
  }'
```

**Response:**

```json
{
  "code": 200,
  "message": "Memory added successfully",
  "data": []
}
```

For `async_mode=async`, `data` is an empty array. The actual task status is queryable via
`GET /product/scheduler/status?user_id=alice&task_id=<task_id>`.

---

#### `GET /product/get_memory/{memory_id}` — Get single memory by ID

```bash
curl -s http://localhost:8080/product/get_memory/3fa85f64-5717-4562-b3fc-2c963f66afa6
```

**Response:**

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "memory_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "memory": "Alice's sister Emma lives in Prague and is a doctor.",
    "metadata": {
      "cube_id": "alice-personal",
      "user_id": "alice",
      "memory_type": "LongTermMemory",
      "created_at": "2026-04-25T10:05:00Z",
      "tags": [],
      "score": null
    }
  }
}
```

Returns `404` (wrapped as `{"code":404,"message":"not found","data":null}`) when the ID does not exist.

---

#### `POST /product/get_memory` — List memories with filter and pagination

Fetches all memories for a cube with optional type filtering and pagination.

**Request body:**

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mem_cube_id` | string | Yes | — | Cube to read from |
| `user_id` | string | No | — | Filter by user |
| `include_preference` | bool | No | `true` | Include preference-type memories |
| `include_tool_memory` | bool | No | `true` | Include tool schema/trajectory memories |
| `include_skill_memory` | bool | No | `true` | Include skill memories |
| `filter` | object | No | — | Structured filter (see **Filter syntax** below) |
| `page` | int | No | null | Page number (1-indexed). Omit for full export |
| `page_size` | int | No | null | Items per page. Omit for full export |

**Example:**

```bash
curl -s -X POST http://localhost:8080/product/get_memory \
  -H "Content-Type: application/json" \
  -d '{
    "mem_cube_id": "alice-personal",
    "page": 1,
    "page_size": 20,
    "include_preference": true
  }'
```

---

#### `POST /product/get_memory_by_ids` — Batch fetch by IDs

Body: a JSON array of UUID strings.

```bash
curl -s -X POST http://localhost:8080/product/get_memory_by_ids \
  -H "Content-Type: application/json" \
  -d '["3fa85f64-5717-4562-b3fc-2c963f66afa6", "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"]'
```

---

#### `POST /product/get_all` — Paginated full scan by memory type

Returns all memories of a given type, with optional subgraph search.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `user_id` | string | Yes | User to scan |
| `memory_type` | `"text_mem"` \| `"act_mem"` \| `"param_mem"` \| `"para_mem"` | Yes | Memory type category |
| `mem_cube_ids` | string[] | No | Specific cubes to scan |
| `search_query` | string | No | If provided, returns a subgraph for this query instead of all memories |

```bash
curl -s -X POST http://localhost:8080/product/get_all \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice", "memory_type": "text_mem"}'
```

---

#### `POST /product/delete_memory` — Delete memories

Deletes by explicit IDs, by file IDs, or by filter. At least one of the three must be provided.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `writable_cube_ids` | string[] | No | Cubes to delete from (restricts scope) |
| `memory_ids` | string[] | No | UUIDs to delete |
| `file_ids` | string[] | No | Delete all memories linked to these file IDs |
| `filter` | object | No | Structured filter (see **Filter syntax** below) |

```bash
# Delete specific memories
curl -s -X POST http://localhost:8080/product/delete_memory \
  -H "Content-Type: application/json" \
  -d '{
    "writable_cube_ids": ["alice-personal"],
    "memory_ids": ["3fa85f64-5717-4562-b3fc-2c963f66afa6"]
  }'

# Delete by filter (all memories older than 2025-01-01)
curl -s -X POST http://localhost:8080/product/delete_memory \
  -H "Content-Type: application/json" \
  -d '{
    "writable_cube_ids": ["alice-personal"],
    "filter": {"and": [{"created_at": {"lt": "2025-01-01"}}]}
  }'
```

**Response:**

```json
{"code": 200, "message": "ok", "data": {"deleted": 1}}
```

---

#### `POST /product/delete_all_memories` — Delete all memories for a user

Deletes every memory node belonging to a user across all their cubes. Irreversible.

```bash
curl -s -X POST http://localhost:8080/product/delete_all_memories \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice"}'
```

---

#### `POST /product/update_memory` — Update memory text

Updates the text of a single memory node and re-embeds it. The old embedding is replaced
synchronously. This incurs one embed call (~50–200 ms depending on embedder).

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `memory_id` | string | Yes | UUID of the memory to update |
| `memory` | string | Yes | New text content |

```bash
curl -s -X POST http://localhost:8080/product/update_memory \
  -H "Content-Type: application/json" \
  -d '{
    "memory_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "memory": "Alice's sister Emma lives in Vienna (moved from Prague) and is a doctor."
  }'
```

---

### Filter Syntax

Filters use a nested `and`/`or` object structure. Supported operators: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in`, `nin`.

```json
{
  "and": [
    {"created_at": {"gt": "2026-01-01"}},
    {"or": [
      {"tags": {"in": ["Travel"]}},
      {"user_id": "alice"}
    ]}
  ]
}
```

Match by exact ID:
```json
{"id": "3fa85f64-5717-4562-b3fc-2c963f66afa6"}
```

---

### Search

#### `POST /product/search` — Semantic memory search

> Added: `level` parameter in v0.23.0 (M10).

**Request body:**

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `query` | string | Yes | — | Natural language search query |
| `user_id` | string | Yes | — | User whose memories to search |
| `readable_cube_ids` | string[] | No | — | Cubes to search. Omit to use all user's cubes |
| `mode` | `"fast"` \| `"fine"` \| `"mixture"` | No | `"fast"` | Search pipeline. `fast` = ANN only; `fine` = ANN + rerank; `mixture` = combined |
| `top_k` | int | No | `10` | Number of textual memories to return |
| `pref_top_k` | int | No | `6` | Number of preference memories to return |
| `tool_mem_top_k` | int | No | `6` | Number of tool memories to return |
| `skill_mem_top_k` | int | No | `3` | Number of skill memories to return |
| `relativity` | float | No | `0` | Minimum similarity threshold `[0, 1]`. `0` = disabled |
| `threshold` | float | No | null | Internal cosine threshold override |
| `dedup` | `"no"` \| `"sim"` \| `"mmr"` | No | `"mmr"` | Deduplication strategy. `mmr` = Max Marginal Relevance (default) |
| `include_preference` | bool | No | `true` | Include preference memories in results |
| `search_tool_memory` | bool | No | `true` | Include tool memories in results |
| `include_skill_memory` | bool | No | `true` | Include skill memories in results |
| `filter` | object | No | — | Structured filter (see **Filter syntax**) |
| `num_stages` | int | No | null | Staged retrieval stages: `0` = disabled, `2` = fast expand, `3` = full D5 |
| `llm_rerank` | bool | No | null | Apply LLM reranking post-retrieval |
| `level` | `"l1"` \| `"l2"` \| `"l3"` | No | null | Restrict to a memory tier. Omit for full search |
| `internet_search` | bool | No | `false` | Augment results with live internet search |
| `session_id` | string | No | — | Soft-prioritize memories from this session |

**Level parameter (added v0.23.0 / M10):**
- `l1` — Working memory only (recent context, Redis + Postgres `WorkingMemory` rows)
- `l2` — Episodic memory only (Postgres `EpisodicMemory` rows)  
- `l3` — Full LTM graph (LongTermMemory + UserMemory + EpisodicMemory + D2 graph traversal)
- Omit for complete cross-tier search (backward compatible default)

**Example — basic search:**

```bash
curl -s -X POST http://localhost:8080/product/search \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "readable_cube_ids": ["alice-personal"],
    "query": "family members and their professions",
    "top_k": 10,
    "mode": "fast",
    "dedup": "mmr"
  }'
```

**Example — filtered search with threshold:**

```bash
curl -s -X POST http://localhost:8080/product/search \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "readable_cube_ids": ["alice-personal"],
    "query": "travel plans",
    "top_k": 5,
    "relativity": 0.5,
    "filter": {"and": [{"tags": {"in": ["Travel"]}}]},
    "level": "l3"
  }'
```

**Response:**

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "memories": [
      {
        "id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
        "memory": "Alice's sister Emma lives in Prague and is a doctor.",
        "score": 0.83,
        "metadata": {
          "cube_id": "alice-personal",
          "user_id": "alice",
          "memory_type": "LongTermMemory",
          "created_at": "2026-04-25T10:05:00Z",
          "tags": [],
          "relativity": 0.83
        }
      }
    ],
    "preferences": [],
    "tool_memories": [],
    "skill_memories": []
  }
}
```

---

### Chat

#### `POST /product/chat/complete` — Chat with memory (blocking)

> Added: `level`, `answer_style` parameters in v0.23.0 (M10).

Retrieves relevant memories, injects them into the prompt with an optional user profile section,
and calls the configured LLM. Returns the full response when generation is complete.

**Request body** (`nativeChatRequest`):

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `user_id` | string | Yes | — | User identity |
| `query` | string | Yes | — | User's question or message |
| `history` | array | No | — | Prior turns `[{"role":"user","content":"..."}]` |
| `top_k` | int | No | `10` | Memories to retrieve |
| `threshold` | float | No | `0.5` | Minimum similarity for retrieved memories |
| `system_prompt` | string | No | — | Override the base system prompt. Takes precedence over `answer_style` |
| `model_name_or_path` | string | No | — | Override the LLM model for this request |
| `mode` | string | No | `"fast"` | Memory search mode |
| `session_id` | string | No | — | Session for soft-prioritization |
| `readable_cube_ids` | string[] | No | — | Cubes to read memories from |
| `writable_cube_ids` | string[] | No | — | Cubes to write the new turn to (when `add_message_on_answer=true`) |
| `include_preference` | bool | No | — | Include preference memories in prompt context |
| `pref_top_k` | int | No | — | Preference memories to retrieve |
| `filter` | object | No | — | Filter for memory retrieval |
| `add_message_on_answer` | bool | No | `true` | Store this Q&A turn to memory after answering |
| `internet_search` | bool | No | `false` | Augment context with internet search |
| `answer_style` | `"conversational"` \| `"factual"` | No | `""` (conversational) | Prompt template. `factual` uses a shorter QA prompt — ~52% lower p95 latency |
| `level` | `"l1"` \| `"l2"` \| `"l3"` | No | null | Restrict memory retrieval to a tier |

**Profile injection (v0.23.0 / M10):**  
When `MEMDB_PROFILE_INJECT=true` (default), a structured user profile section is prepended
above the memory section in the system prompt. Profiles are maintained automatically via
the `add_fine` pipeline and the `user_profiles` table. Disable with `MEMDB_PROFILE_INJECT=false`.

**Example — basic chat:**

```bash
curl -s -X POST http://localhost:8080/product/chat/complete \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "readable_cube_ids": ["alice-personal"],
    "query": "What do you know about my family?",
    "top_k": 10,
    "answer_style": "conversational"
  }'
```

**Example — factual QA (benchmark mode, faster):**

```bash
curl -s -X POST http://localhost:8080/product/chat/complete \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "readable_cube_ids": ["alice-personal"],
    "query": "Where does Emma live?",
    "answer_style": "factual",
    "add_message_on_answer": false
  }'
```

**Response:**

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "answer": "Emma lives in Prague and works as a doctor.",
    "references": [
      {"id": "3fa85f64-...", "memory": "Alice's sister Emma lives in Prague and is a doctor.", "score": 0.91}
    ]
  }
}
```

---

#### `POST /product/chat/stream` — Chat with memory (SSE streaming)

Same request body as `/product/chat/complete`. Returns a Server-Sent Events stream.

```bash
curl -s -X POST http://localhost:8080/product/chat/stream \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{
    "user_id": "alice",
    "readable_cube_ids": ["alice-personal"],
    "query": "What do you know about my sister?"
  }'
```

**SSE format:**

```
data: {"delta": "Emma "}

data: {"delta": "lives in "}

data: {"delta": "Prague."}

data: [DONE]
```

Each `data:` line carries a partial token delta. The stream ends with `data: [DONE]`.

---

#### `POST /product/llm/complete` — LLM passthrough (no memory)

Directly proxies to the configured LLM API. No memory retrieval, no storage. OpenAI-compatible
request/response. Use when you need raw LLM access without memory overhead.

```bash
curl -s -X POST http://localhost:8080/product/llm/complete \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Summarize the French Revolution in 2 sentences."}
    ],
    "model": "gemini-2.5-flash",
    "max_tokens": 200
  }'
```

Supports streaming via `"stream": true` in the request body (returns SSE) or
`Accept: text/event-stream` header.

---

### Feedback

#### `POST /product/feedback` — Store user feedback

Processes user feedback against a prior conversation history, extracting corrections and
preference updates.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `user_id` | string | Yes | User identity |
| `history` | array | Yes | Full conversation history up to the feedback point |
| `feedback_content` | string | Yes | The user's corrective feedback text |
| `session_id` | string | No | Session for scoping |
| `task_id` | string | No | Client-assigned task ID for async monitoring |
| `retrieved_memory_ids` | string[] | No | Memory IDs that were shown to the user (improves precision) |
| `async_mode` | `"sync"` \| `"async"` | No | `"async"` |
| `corrected_answer` | bool | No | `false` | Whether to return the corrected answer |
| `writable_cube_ids` | string[] | No | Cubes to write feedback memories to |
| `info` | object | No | Additional metadata |

```bash
curl -s -X POST http://localhost:8080/product/feedback \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "history": [
      {"role": "user", "content": "Where does my sister live?"},
      {"role": "assistant", "content": "Emma lives in Prague."}
    ],
    "feedback_content": "Actually Emma moved to Vienna last year.",
    "async_mode": "sync"
  }'
```

---

### Cubes & Users

Cubes are logical partitions within MemDB. Each user's memories live in one or more cubes.
Multi-cube queries use `readable_cube_ids` / `writable_cube_ids` in search/add/chat.

#### `POST /product/create_cube` — Create or update cube

Idempotent: a second call with the same `cube_id` updates metadata without touching memories.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cube_id` | string | Yes | Unique cube identifier |
| `owner_id` or `user_id` | string | Yes | Owner of this cube |
| `cube_name` | string | No | Human-readable display name |
| `description` | string | No | Free-text description |
| `settings` | object | No | Arbitrary settings JSON |

```bash
curl -s -X POST http://localhost:8080/product/create_cube \
  -H "Content-Type: application/json" \
  -d '{"cube_id": "alice-personal", "owner_id": "alice", "cube_name": "Alice personal memories"}'
```

**Response:**

```json
{"code": 200, "message": "ok", "data": {"cube": {...}, "created": true}}
```

---

#### `POST /product/list_cubes` — List cubes

```bash
# All cubes
curl -s -X POST http://localhost:8080/product/list_cubes \
  -H "Content-Type: application/json" -d '{}'

# Cubes for a specific owner
curl -s -X POST http://localhost:8080/product/list_cubes \
  -H "Content-Type: application/json" \
  -d '{"owner_id": "alice"}'
```

---

#### `POST /product/delete_cube` — Delete cube

Deletes the cube record. Memories inside the cube are **not** automatically deleted — call
`/product/delete_all_memories` first if needed.

```bash
curl -s -X POST http://localhost:8080/product/delete_cube \
  -H "Content-Type: application/json" \
  -d '{"cube_id": "alice-personal"}'
```

---

#### `POST /product/get_user_cubes` — List cubes for a user (alias)

Equivalent to `list_cubes` filtered by `owner_id`. Exists for upstream MemOS compatibility.

```bash
curl -s -X POST http://localhost:8080/product/get_user_cubes \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice"}'
```

---

#### `POST /product/exist_mem_cube_id` — Check cube existence

```bash
curl -s -X POST http://localhost:8080/product/exist_mem_cube_id \
  -H "Content-Type: application/json" \
  -d '{"mem_cube_id": "alice-personal"}'
# {"code":200,"message":"ok","data":{"alice-personal":true}}
```

---

#### `GET /product/users` — List all users

```bash
curl -s http://localhost:8080/product/users
```

Returns distinct `user_id` values present in the database as identity objects.

---

#### `GET /product/users/{user_id}` — Get user info

```bash
curl -s http://localhost:8080/product/users/alice
```

---

#### `POST /product/get_user_info` — Get user info (POST alias)

Upstream MemOS compatibility alias.

```bash
curl -s -X POST http://localhost:8080/product/get_user_info \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice"}'
```

---

#### `POST /product/users/register` — Register user

Pre-registers a user identity. Not required before add/search — those create the user
implicitly. Useful for explicit onboarding flows.

```bash
curl -s -X POST http://localhost:8080/product/users/register \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice"}'
```

---

### Scheduler

The scheduler processes async add/feedback jobs from Redis Streams. These endpoints let
you poll task status or wait for the queue to drain.

#### `GET /product/scheduler/status` — Poll task status

```
GET /product/scheduler/status?user_id=alice&task_id=<task_id>
```

Returns an array of `StatusResponseItem`:

```json
{
  "code": 200,
  "message": "Memory get status successfully",
  "data": [{"task_id": "abc123", "status": "completed"}]
}
```

`status` values: `in_progress`, `completed`, `waiting`, `failed`, `cancelled`.

---

#### `GET /product/scheduler/allstatus` — Full scheduler summary

Returns aggregate task counts across all users and the scheduler queue.

```bash
curl -s http://localhost:8080/product/scheduler/allstatus
```

---

#### `GET /product/scheduler/task_queue_status` — Queue backlog for a user

```
GET /product/scheduler/task_queue_status?user_id=alice
```

---

#### `POST /product/scheduler/wait` — Block until idle

Blocks the HTTP connection until the scheduler queue for `user_name` drains or `timeout_seconds` expires.

```bash
curl -s -X POST "http://localhost:8080/product/scheduler/wait?user_name=alice&timeout_seconds=30"
```

Useful in test pipelines after async ingestion before querying.

---

#### `GET /product/scheduler/wait/stream` — SSE wait

```
GET /product/scheduler/wait/stream?user_name=alice&timeout_seconds=30
```

Streams progress events via SSE while waiting for the scheduler to drain.

---

### Admin

Admin endpoints require the service to be running with a configured reorganizer. Both return
errors if the reorganizer is not wired up.

#### `POST /product/admin/reorg` — Trigger memory reorganizer (D3)

Manually triggers the D3 hierarchical cluster reorganizer for a cube. Returns `202 Accepted`
immediately; the reorganizer runs in a background goroutine with a 10-minute timeout.

**When to use:** After bulk-ingesting memories, to force consolidation without waiting for
the 6-hour background cycle.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cube_id` | string | Yes | Cube to reorganize |
| `ids` | string[] | No | Specific memory IDs to target. Omit for full-cube reorg |

```bash
# Full cube reorganization
curl -s -X POST http://localhost:8080/product/admin/reorg \
  -H "Content-Type: application/json" \
  -d '{"cube_id": "alice-personal"}'

# Targeted reorganization of specific nodes
curl -s -X POST http://localhost:8080/product/admin/reorg \
  -H "Content-Type: application/json" \
  -d '{"cube_id": "alice-personal", "ids": ["3fa85f64-...", "9b1deb4d-..."]}'
```

**Response:**

```json
{
  "status": "accepted",
  "cube_id": "alice-personal",
  "mode": "full",
  "triggered_at": "2026-04-25T10:30:00Z"
}
```

---

#### `POST /product/admin/reprocess` — Re-extract raw memories via fine pipeline

Re-processes raw conversation-window nodes through LLM fact extraction. Converts memories
stored by the old fast-mode path into clean LTM facts. Synchronous and blocking.

**Request body:**

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `cube_id` | string | Yes | — | Cube to reprocess |
| `limit` | int | No | `50` | Max raw nodes to process (capped at `200`) |
| `dry_run` | bool | No | `false` | Report what would be processed without doing it |

```bash
# Dry run first
curl -s -X POST http://localhost:8080/product/admin/reprocess \
  -H "Content-Type: application/json" \
  -d '{"cube_id": "alice-personal", "dry_run": true}'

# Execute
curl -s -X POST http://localhost:8080/product/admin/reprocess \
  -H "Content-Type: application/json" \
  -d '{"cube_id": "alice-personal", "limit": 50}'
```

**Response:**

```json
{
  "code": 200,
  "message": "reprocess complete",
  "data": {
    "total_raw": 120,
    "processed": 50,
    "extracted": 143,
    "deleted": 48,
    "remaining": 72,
    "duration_ms": 18400
  }
}
```

---

### Health & Observability

#### `GET /health` — Liveness check

Auth-exempt. Returns `200` when the HTTP server is running.

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

---

#### `GET /ready` — Readiness check

Auth-exempt. Returns `200` when all required dependencies (Postgres, embedder) are connected.
Returns `503` during startup or when a dependency is down.

```bash
curl http://localhost:8080/ready
# {"status":"ready","checks":{"postgres":"ok","embedder":"ok"}}
```

---

#### `GET /metrics` — Prometheus metrics

Auth-exempt (bound to `127.0.0.1` in docker-compose). Returns Prometheus text format.

```bash
curl http://localhost:8080/metrics
```

Key exported metrics:

| Metric | Description |
|--------|-------------|
| `memdb_add_total` | Total add requests |
| `memdb_search_total` | Total search requests |
| `memdb_chat_prompt_template_used_total{template}` | Chat requests by prompt template |
| `memdb_scheduler_tasks_total{status}` | Scheduler task outcomes |
| `memdb_migration_checksum_drift_total` | SQL migration drift detections |
| Standard Go runtime metrics | `go_goroutines`, `go_memstats_*`, etc. |

---

#### `GET /debug/pprof/*` — pprof profiling

Requires `X-Service-Secret` header. Bearer tokens are explicitly rejected here.

```bash
curl -H "X-Service-Secret: $INTERNAL_SERVICE_SECRET" \
  http://localhost:8080/debug/pprof/goroutine?debug=1
```

---

#### `GET /openapi.json` — OpenAPI spec

Returns the OpenAPI 3.1 spec as JSON.

#### `GET /docs` — Swagger UI

Interactive API explorer.

---

### Embeddings

#### `POST /v1/embeddings` — OpenAI-compatible embeddings

Auth-exempt. Uses the configured local ONNX embedder (or embed-server sidecar).
Supports multi-model via `model` field when `MEMDB_EMBED_URL_CODE` is set.

```bash
curl -s -X POST http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "input": "Alice moved to Berlin and works as a software engineer.",
    "model": "multilingual-e5-large"
  }'
```

**Input** can be a string, an array of strings, or an array of token arrays (standard OpenAI format).

**Response:**

```json
{
  "object": "list",
  "data": [
    {"object": "embedding", "index": 0, "embedding": [0.021, -0.043, ...]}
  ],
  "model": "multilingual-e5-large",
  "usage": {"prompt_tokens": 12, "total_tokens": 12}
}
```

---

## Environment Gates

Environment variables that affect API behavior at runtime. Changing most of these requires a container restart.

| Variable | Default | Effect |
|----------|---------|--------|
| `AUTH_ENABLED` | `false` | Enable Bearer/service-secret auth on all `/product/*` routes |
| `MASTER_KEY_HASH` | `""` | SHA-256 hex digest of the master API key |
| `INTERNAL_SERVICE_SECRET` | `""` | Service-to-service bypass secret |
| `MEMDB_GO_PORT` | `8080` | HTTP listen port |
| `MEMDB_PYTHON_URL` | `http://localhost:8000` | Fallback Python backend for proxied endpoints |
| `ENABLE_CHAT_API` | `false` | Enable `/product/chat/*` endpoints |
| `MEMDB_DEFAULT_ANSWER_STYLE` | `""` (conversational) | Server-wide default for chat prompt template |
| `MEMDB_FACTUAL_CANARY_PCT` | `0` | Percentage of users routed to `factual` prompt canary `[0, 100]` |
| `MEMDB_DATE_AWARE_EXTRACT` | `true` | Inject current date hint into fine-mode extraction prompt |
| `MEMDB_PROFILE_EXTRACT` | `true` | Run user profile extractor after `add_fine` |
| `MEMDB_PROFILE_INJECT` | `true` | Prepend user profile section to chat system prompt |
| `MEMDB_CE_PRECOMPUTE` | `true` | Use pre-computed cross-encoder scores at retrieval (skip live CE call on cache hit) |
| `MEMDB_PAGERANK_ENABLED` | `true` | Enable PageRank-based D1 rerank boost |
| `MEMDB_PAGERANK_INTERVAL` | `6h` | How often to recompute PageRank scores (Go duration string) |
| `MEMDB_PAGERANK_BOOST_WEIGHT` | `0.1` | D1 rerank multiplier weight `[0, 1]`: `score *= 1 + pagerank * weight` |
| `MEMDB_REORG_HIERARCHY` | `false` | Enable D3 tree hierarchy reorg (raw → episodic → semantic promotion) |
| `MEMDB_D1_IMPORTANCE` | `false` | Enable D1 temporal decay + access-frequency importance formula |
| `MEMDB_D2_MAX_HOP` | `2` | Multi-hop graph traversal depth `[1, 5]` |
| `MEMDB_D5_SHORTLIST_SIZE` | `20` | D5 staged retrieval initial shortlist size `[5, 100]` |
| `MEMDB_D5_MAX_INPUT_SIZE` | `50` | D5 maximum candidates passed to reranker `[10, 500]` |
| `MEMDB_SEARCH_COT` | `false` | Enable D11 chain-of-thought query decomposition (also: `MEMDB_COT_DECOMPOSE`) |
| `MEMDB_COT_MAX_SUBQUERIES` | `3` | Max sub-queries from CoT decomposition `[1, 5]` |
| `MEMDB_COT_TIMEOUT_MS` | `2000` | CoT decomposition timeout in ms `[500, 10000]` |
| `MEMDB_BUFFER_ENABLED` | `false` | Enable add buffer zone (batch before LLM extraction) |
| `MEMDB_BUFFER_SIZE` | `5` | Buffer flush threshold (message count) |
| `MEMDB_BUFFER_TTL` | `30s` | Buffer flush time limit |
| `MEMDB_ADD_WORKERS` | `4` | Max concurrent native add requests (semaphore) |
| `MEMDB_ADD_QUEUE_SIZE` | `50` | Max add requests waiting for a worker slot before 503 |
| `MEMDB_CACHE_ENABLED` | `false` | Enable Redis response cache for search |
| `MEMDB_REDIS_URL` | `redis://redis:6379/1` | Redis URL for cache |
| `MEMDB_RATE_LIMIT_ENABLED` | `false` | Enable token-bucket rate limiter |
| `MEMDB_RATE_LIMIT_RPS` | `50` | Allowed requests per second |
| `MEMDB_RATE_LIMIT_BURST` | `100` | Burst capacity |
| `MEMDB_EMBEDDER_TYPE` | `"onnx"` | Embedder backend: `onnx`, `http`, `voyage`, `ollama` |
| `MEMDB_EMBED_URL` | `""` | HTTP embed-server sidecar URL (when `type=http`) |
| `MEMDB_LLM_PROXY_URL` | `https://api.openai.com/v1` | OpenAI-compatible LLM base URL |
| `MEMDB_LLM_MODEL` | `gemini-2.5-flash` | Default LLM model for chat |
| `MEMDB_LLM_SEARCH_MODEL` | `gemini-2.0-flash` | Model for search LLM calls (rerank, CoT) |
| `MEMDB_LLM_EXTRACT_MODEL` | `gemini-2.0-flash-lite` | Model for fine-mode extraction |
| `MEMDB_REORG_LLM_MODEL` | `gemini-2.5-flash-lite` | Model for D3 memory consolidation |
| `CROSS_ENCODER_URL` | `http://embed-server:8082` | Cross-encoder reranker URL (empty = disabled) |
| `CROSS_ENCODER_MODEL` | `gte-multi-rerank` | Cross-encoder model name |
| `CROSS_ENCODER_MAX_DOCS` | `50` | Max documents sent to reranker per request |
| `CROSS_ENCODER_TIMEOUT_MS` | `2000` | Reranker timeout in ms |
| `OTEL_ENABLED` | `false` | Enable OpenTelemetry tracing |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `""` | OTLP collector endpoint |

---

## Performance Notes

Numbers from M7 and M10 measurements (2026-04-25, local docker-compose, Gemini Flash via CLIProxyAPI).

### Latency by endpoint (p50 / p95)

| Endpoint | Condition | p50 | p95 |
|----------|-----------|-----|-----|
| `POST /product/add` | `async_mode=async` (returns immediately) | < 5 ms | < 10 ms |
| `POST /product/add` | `async_mode=sync, mode=fast`, default window (4096) | 0.78 s | 1.21 s |
| `POST /product/add` | `async_mode=sync, mode=raw` | ~30 ms | ~50 ms |
| `POST /product/add` | `async_mode=sync, mode=fast, window=512` (24 windows) | 13.6 s | 20.0 s |
| `POST /product/chat/complete` | `answer_style=conversational` | 10.2 s | 14.7 s |
| `POST /product/chat/complete` | `answer_style=factual` | 4.3 s | 7.0 s |
| `POST /product/search` | `mode=fast`, pre-indexed cube | < 100 ms | < 200 ms |

**Why factual is faster:** The factual QA prompt is ~700 bytes vs ~3500 bytes for conversational,
reducing LLM input tokens by ~80%. LLM call dominates chat latency. Use `answer_style=factual`
for benchmark-style QA workloads; use `conversational` for production chat.

**window_chars trade-off:** Each additional window = one extra embed + dedup + insert cycle.
At window=512 with a 1710-char conversation, 24 cycles push p95 from 1.2 s to 20 s (+1551%).
Keep `window_chars >= 1024` or use the default (4096) for latency-sensitive paths.

### mode=raw vs mode=fine

| | `mode=raw` | `mode=fast` | `mode=fine` |
|-|------------|-------------|-------------|
| LLM call | None | None (extraction via sliding window) | Yes (ExtractAndDedup) |
| Memory quality | Raw transcript | Windowed chunks | Extracted facts, deduped |
| p95 latency | ~30–50 ms | ~1.2 s | ~5–10 s |
| Best for | High-throughput ingestion | Balanced | Maximum recall quality |

---

## Rate Limits

No hard rate limits are enforced by default. Rate limiting is available via token bucket
(`MEMDB_RATE_LIMIT_ENABLED=true`, `MEMDB_RATE_LIMIT_RPS=50`, `MEMDB_RATE_LIMIT_BURST=100`).

Concurrency for the add pipeline is bounded by `MEMDB_ADD_WORKERS` (default 4) and
`MEMDB_ADD_QUEUE_SIZE` (default 50). Requests beyond the queue limit return `503 Service Unavailable`.

The embed-server sidecar and LLM proxy may impose their own upstream rate limits. LLM errors
on quota exhaustion are surfaced as `502`/`503` from the handler.

---

## Versioning & Breaking Changes

- **0.x series** — API is not frozen. Minor additions are non-breaking; removals may occur on minor versions with notice in [CHANGELOG.md](../CHANGELOG.md).
- **1.0** — API will be frozen. Breaking changes require a major version bump and deprecation notice.
- Schema additions (new optional fields) are always non-breaking.
- The `async_mode=async` default for `/product/add` is a stable behavioral contract.
- Deprecated fields (`mem_cube_id` singular, `moscube`, `memory_content`, `doc_path`, `source`) are retained for backward compatibility but should not be used in new integrations.

See [CHANGELOG.md](../CHANGELOG.md) for version-by-version changes.

---

## Client SDKs & Integration

- **Go example:** [examples/go/quickstart](../examples/go/quickstart) — full add + search + chat loop
- **Python example:** [examples/python/quickstart](../examples/python/quickstart)
- **MCP (Claude Desktop / Claude Code):** [examples/mcp/claude-desktop](../examples/mcp/claude-desktop) — built-in MCP server runs alongside the HTTP API on the same binary
- **OpenAPI codegen:** The spec at `memdb-go/api/openapi.yaml` is compatible with openapi-generator, oapi-codegen, or any OpenAPI 3.1 toolchain.

---

## See Also

- [OpenAPI spec](../memdb-go/api/openapi.yaml) — machine-readable, source of truth for all schema definitions
- [Swagger UI](http://localhost:8080/docs) — interactive endpoint explorer (when running)
- [docs/memdb-go/handlers.md](memdb-go/handlers.md) — internal architecture (not API docs)
- [docs/memdb-go/search.md](memdb-go/search.md) — D1–D11 retrieval pipeline internals
- [CHANGELOG.md](../CHANGELOG.md) — release history
- [ROADMAP.md](../ROADMAP.md) — upcoming features
