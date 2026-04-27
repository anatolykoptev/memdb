# Show HN: MemDB – open-source agent memory, 72.5% LoCoMo F1 (between MemOS and Zep)

Most AI agents today suffer from fragmented, unreliable memory. MemDB is an open-source, self-hostable memory layer with a pure-Go stack — Postgres + Apache AGE graph + pgvector + qdrant + redis + an embed-server sidecar — shipped as one `docker-compose.yml`. It is a drop-in replacement for Anthropic's `memory_20250818` tool, with native MCP and Claude Code plugin surfaces alongside the HTTP API.

## Why I built it

Every production agent I shipped in the last year hit the same wall: persistent memory across sessions. The existing options each force a trade-off:

- **Mem0, Zep Cloud, Memobase** — managed, paid, no self-host (or partial OSS that lags the cloud product). Vendor lock-in, US-only data residency, billing scales with agent count.
- **Letta / MemGPT** — great architecture, Python-heavy ops, no MCP, no LoCoMo number to compare against.
- **MemOS** — closest match philosophically, but heavy Python stack, no Claude integrations.
- **Roll your own with a vector DB** — 500+ lines of glue code per service to handle temporal logic, summarization, multi-hop graph walk, and tenant isolation.

MemDB exists to be the option that does not ask you to give up self-host, ops simplicity, BYO-LLM, **or** competitive recall accuracy.

## What's in it

- **Pure-Go core** — single statically-linked binary, no Python runtime. `docker compose up` and you're live.
- **Hybrid retrieval** — Postgres + AGE for graph walk, pgvector for dense recall, qdrant for high-throughput ANN, redis for hot-path caching. The retriever picks the right combination per question.
- **Three Claude integration surfaces**, all maintained in-tree:
  - **Claude Code plugin** — auto-injects relevant memories into your prompt before each turn.
  - **MCP server** — speaks Model Context Protocol, plugs into Claude Desktop / Code / any MCP host.
  - **API memory tool adapter** ([memdb-claude-memory-tool v0.1.0](https://github.com/anatolykoptev/memdb-claude-memory-tool)) — drop-in for Anthropic's `memory_20250818`. Replace the filesystem default with a multi-tenant memory cube in three lines.
- **BYO-LLM** — works with Gemini, GPT, Claude, or any local model via OpenAI-compatible endpoint. The embedder is also swappable (default: ONNX `multilingual-e5-large`, 1024-dim).
- **Multi-tenant** — explicit `cube_id` on every record. Tenant isolation is a first-class concept, not a bolt-on namespace.
- **Memobase-comparable LLM Judge measurement** — same Gemini 2.5 Flash judge prompt, same chat-50 stratified subset, same cat-5 exclusion. Methodology in `evaluation/locomo/MILESTONES.md`.

## Benchmark — LoCoMo chat-50 stratified, LLM Judge

| Rank | System          | LLM Judge | License |
|-----:|-----------------|----------:|---------|
| 1    | Memobase        | 75.78     | Commercial cloud |
| 2    | Zep             | 75.14     | Apache-2 OSS + Cloud |
| 3    | **MemDB v0.23.0** | **72.50** | **MIT** |
| 4    | MemOS           | 73.31     | Apache-2 |
| 5    | Mem0            | 66.88     | Commercial cloud |

Between MemOS and Zep. **+5.62 pp ahead of Mem0**, **−3.28 pp behind Memobase** (closing in M11). And — to my knowledge — the only system in the top tier you can run end-to-end without paying anyone or running Python.

Full methodology: [`evaluation/locomo/MILESTONES.md`](../../evaluation/locomo/MILESTONES.md). Full comparison matrix: [`docs/marketing/competitive-comparison.md`](competitive-comparison.md).

## Try it

```bash
git clone https://github.com/anatolykoptev/memdb && cd memdb
docker compose up -d
curl http://localhost:8080/health
# {"status":"ok","version":"0.23.0"}
```

Add a memory:

```bash
curl -X POST http://localhost:8080/v1/cubes/demo/memories \
  -H 'Content-Type: application/json' \
  -d '{"text":"User prefers concise answers in Russian.","speaker":"system"}'
```

Recall:

```bash
curl "http://localhost:8080/v1/cubes/demo/recall?q=language%20preference"
```

Drop into Claude API as the memory backend:

```python
# pip install memdb-claude-memory-tool
from memdb_claude_memory_tool import MemDBMemoryTool
tool = MemDBMemoryTool(base_url="http://localhost:8080", cube_id="user_42")
client.messages.create(
    model="claude-sonnet-4-6",
    tools=[tool.as_tool_spec()],   # speaks memory_20250818
    ...
)
```

## What I'd love feedback on

- Whether **D2 multi-hop should default to depth=2** (faster, current default) **or depth=3** (better recall, ~30% slower). Closing the Memobase gap likely requires depth=3 default — but I don't want to surprise people on the latency curve.
- **Adapter API ergonomics** for Claude API users — `memory_20250818` is new and the wire-format is still evolving. Issues / PRs against [memdb-claude-memory-tool](https://github.com/anatolykoptev/memdb-claude-memory-tool) very welcome.
- Anything in this post that read as **marketing rather than engineering** — would prefer the latter, please call it out.

Repo: https://github.com/anatolykoptev/memdb
Docs: [`docs/API.md`](../API.md) (full HTTP reference), [`docs/integrations/`](../integrations/)
Adapter: https://github.com/anatolykoptev/memdb-claude-memory-tool
License: MIT
