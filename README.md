<div align="center">

# MemDB

**Self-hosted long-term memory database for AI agents.**
**One docker-compose. Pure Go.**

[![License](https://img.shields.io/badge/License-Apache_2.0-green.svg?logo=apache)](https://opensource.org/license/apache-2-0/)
[![Version](https://img.shields.io/badge/version-0.22.0-blue.svg)](https://github.com/anatolykoptev/memdb/releases)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8.svg?logo=go)](https://go.dev/)
[![GitHub stars](https://img.shields.io/github/stars/anatolykoptev/memdb?style=social)](https://github.com/anatolykoptev/memdb/stargazers)
[![Discord](https://img.shields.io/badge/Discord-join%20chat-7289DA.svg?logo=discord)](https://discord.gg/8vhbTZgf)

<img src="docs/assets/architecture.svg" alt="MemDB architecture: agent → memdb-go → Postgres (pgvector + Apache AGE) + Redis + Qdrant + embed-server, plus memdb-mcp" width="100%" />

<!-- TODO demo: record with asciinema + agg → docs/assets/demo.gif (recipe in docs/assets/README.md) -->
<!-- <img src="docs/assets/demo.gif" alt="MemDB 30-second demo: docker compose up, add memory, search memory" width="100%" /> -->

[**Quick Start**](#quick-start-5-minutes) ·
[**Use Cases**](#use-cases) ·
[**Comparison**](#why-memdb) ·
[**Documentation**](docs/) ·
[**Roadmap**](#roadmap) ·
[**Discord**](https://discord.gg/8vhbTZgf)

</div>

---

## What is MemDB?

MemDB stores, retrieves, and manages long-term memory for AI agents. It runs as a single
`docker compose up` and exposes a REST API plus a built-in MCP server, so Claude-style
agents (Telegram bots, IDE copilots, support agents, personal assistants) can recall facts,
preferences, and prior conversations across sessions.

<!-- TODO benchmark-comparison.svg: drop bar chart here once M9 Stage 3 re-run lands -->
<!-- <p align="center"><img src="docs/assets/benchmark-comparison.svg" alt="LLM-Judge: MemDB vs Mem0 vs Letta vs Zep vs Memobase" width="80%" /></p> -->

---

## Why MemDB

Honest comparison with comparable open-source memory systems. `?` marks unverified numbers
— please open a PR with a citation if you have current data.

| | **MemDB** | Mem0 | Letta | Zep | Memobase |
|---|---|---|---|---|---|
| Self-hostable | **✅ Yes** (pure Go binary) | ✅ Yes (Python) <!-- TODO verify --> | ✅ Yes (Python) <!-- TODO verify --> | ✅ Yes <!-- TODO verify --> | ✅ Yes <!-- TODO verify --> |
| Single static binary | **✅ Yes** | ❌ No | ❌ No | ❌ No | ❌ No |
| LoCoMo LLM-Judge | **⚠️ TBD** (M9 measurement in flight) | ~62% `?` | ~58% `?` | ~70% `?` | 75.78% (excl. cat-5) |
| pgvector + AGE graph | **✅ Yes** | ⚠️ Partial `?` | ❌ No | ⚠️ Yes (Neo4j) `?` | ⚠️ Partial `?` |
| MCP server included | **✅ Yes** | ❌ No `?` | ❌ No `?` | ❌ No `?` | ❌ No `?` |
| Local embeddings | **✅ ONNX sidecar** | ❌ No `?` | ❌ No `?` | ❌ No `?` | ❌ No `?` |
| License | **Apache 2.0** | Apache 2.0 `?` | Apache 2.0 `?` | Apache 2.0 `?` | Apache 2.0 `?` |

The `?` marks honest uncertainty, not disparagement. Memobase 75.78% is published in their
LoCoMo harness, excluding adversarial category 5 — see
[MILESTONES.md](evaluation/locomo/MILESTONES.md#two-track-reporting-convention-m9-stream-3)
for why we report two tracks. For the long version — origin story, academic foundation,
MemOS-vs-MemDB divergence — see [docs/overview.md](docs/overview.md) (EN) or
[docs/overview-ru.md](docs/overview-ru.md) (RU).

---

## Use Cases

<table>
<tr>
<td width="50%" valign="top">

### 🤖 Telegram / Discord bot
Remembers user prefs and prior context across sessions. No more "tell me about yourself" cold starts.
[See example →](examples/go/quickstart)

</td>
<td width="50%" valign="top">

### 💻 IDE copilot
Persistent context about the user's stack, naming conventions, and recurring bug patterns. Recall on file open.
[See example →](examples/python/quickstart)

</td>
</tr>
<tr>
<td width="50%" valign="top">

### 🎧 Customer support agent
Recalls a customer's prior issues and account context. They never re-explain. Scope per org with `cube_id`, per customer with `user_id`.
[See example →](examples/mcp/claude-desktop)

</td>
<td width="50%" valign="top">

### 🧠 Personal assistant
"What did I order on Amazon last March?" — long-horizon recall across email, chat, and tool history. Not "I don't have access".
[See example →](examples/python/quickstart)

</td>
</tr>
</table>

<!-- TODO telegram-bot-demo.png: drop bot screenshot here once recorded (see docs/assets/README.md) -->
<!-- <p align="center"><img src="docs/assets/telegram-bot-demo.png" alt="Telegram bot remembering a user preference across two chats a day apart" width="80%" /></p> -->

Plus **agentic workflows** — persistent skill / trajectory memory: the agent remembers which tools succeeded for which task category and uses that history to plan future runs.

---

## When MemDB is **NOT** the right fit

Trust through limits — pick something else if:

- **You want parametric memory.** MemDB stores explicit memories in Postgres, not weights.
  For baking knowledge into the model itself, use LoRA / QLoRA (axolotl, unsloth).
- **You need multi-modal image / audio memory today.** On the roadmap
  ([docs/backlog/features.md](docs/backlog/features.md)), not shipped. Today: LanceDB or
  a custom CLIP + Qdrant stack.
- **You want a managed cloud.** MemDB is self-hosted only — no `app.memdb.ai` plan yet.
  Mem0 Cloud / Zep Cloud are valid alternatives.
- **You need < 50 ms p99 retrieval at million-memory scale.** The full D1–D11 + rerank
  cascade targets quality, not latency extremes. Pure ANN (Qdrant, Milvus standalone) is
  lower-latency.
- **You only need session-scoped memory.** LangChain `ConversationBufferMemory` is simpler.
  MemDB earns its weight starting from "remember across days / weeks / users".

---

## Quick Start (5 minutes)

```bash
git clone https://github.com/anatolykoptev/memdb && cd memdb
cp .env.example .env
# edit .env: set MEMDB_LLM_API_KEY (any OpenAI-compatible endpoint works)
#            set POSTGRES_PASSWORD (no default — required)
docker compose -f docker/docker-compose.yml up -d
curl http://localhost:8080/health
# {"status":"ok"}
```

Add a memory, then search it back:

```bash
curl -s -X POST http://localhost:8080/product/add -H "Content-Type: application/json" -d '{
  "user_id": "alice", "writable_cube_ids": ["my-cube"],
  "messages": [
    {"role": "user", "content": "I love hiking and prefer concise answers."},
    {"role": "assistant", "content": "Noted."}
  ],
  "async_mode": "sync"
}'

curl -s -X POST http://localhost:8080/product/search -H "Content-Type: application/json" -d '{
  "user_id": "alice", "readable_cube_ids": ["my-cube"],
  "query": "outdoor activities", "top_k": 5, "mode": "fast"
}'
```

Expected response (truncated):

```json
{"memories": [{"id": "...", "memory": "Alice loves hiking and prefers concise answers.",
  "score": 0.78, "metadata": {"cube_id": "my-cube", "created_at": "2026-04-25T..."}}]}
```

Optional: enable the local ONNX embed-server sidecar (no third-party embedding API):

```bash
docker compose -f docker/docker-compose.yml --profile embed up -d
```

Then in `.env`: `MEMDB_EMBEDDER_TYPE=http` and `MEMDB_EMBED_URL=http://embed-server:8080`.

Full API reference: [docs/openapi.json](docs/openapi.json). Runnable examples:
[examples/go/quickstart](examples/go/quickstart), [examples/python/quickstart](examples/python/quickstart),
[examples/mcp/claude-desktop](examples/mcp/claude-desktop).

---

## Architecture

Default deployment is **two containers** (Postgres + memdb-go); enable the embed sidecar
to make it three. There is no Python in the production hot path — `memdb-go` is the sole
service. Postgres covers both vector similarity and graph traversal, eliminating Neo4j /
standalone Qdrant from the required dependency list. The full container diagram is the
hero image at the top of this README. Migration history:
[ROADMAP-GO-MIGRATION.md](ROADMAP-GO-MIGRATION.md) (Phase 5 Python shutdown completed).

---

## Features

**Storage**
- Postgres 17 with [pgvector](https://github.com/pgvector/pgvector) for 1024-dim semantic search and [Apache AGE](https://age.apache.org/) for graph traversal — single primary store, no separate vector or graph DB to operate.
- Optional Redis hot cache for working memory; optional Qdrant for sparse + dense hybrid retrieval at scale.
- Versioned SQL migrations with checksum drift detection (`memdb.migration.checksum_drift` metric).

**Retrieval — D1 through D11**
- D1: temporal decay + access-frequency rerank
- D2: multi-hop graph expansion via AGE / `memory_edges` recursive CTE
- D3: hierarchical cluster reorganizer
- D4: query rewriting
- D5: staged retrieval (shortlist → rerank → expand)
- D10: post-retrieval enhancement
- D11: chain-of-thought query decomposition
- Plus structural-edge ingest, dual-speaker harness, factual answer-style mode

**Operations**
- Single Go binary — no interpreter, no compile chain in production
- Built-in MCP server + stdio proxy for Claude Desktop / Claude Code
- OpenAPI 3 spec ([docs/openapi.json](docs/openapi.json))
- Prometheus metrics on `/metrics`
- Fail-closed safety nets — write failures surface as HTTP errors, never silent drops
- Cohere-compatible reranker plug-in (works with Cohere, Jina, Voyage, Mixedbread,
  HuggingFace TEI, or your own embed-server)

---

## Benchmarks

MemDB tracks LoCoMo (Long Conversation Memory) scores per release; full per-milestone
deltas live in [evaluation/locomo/MILESTONES.md](evaluation/locomo/MILESTONES.md).

Highlights:
- **M7 Stage 2 (conv-26 full, 199 QAs):** F1 **0.238**, hit@k 0.769 — first MemOS-tier result on a full single conversation.
- **M9 (current):** ports the Memobase LLM-Judge metric for direct comparison to public leaderboards (Mem0, Zep, LangMem use the same binary judge). Headline LLM-Judge number on the full 10-conversation corpus: *measurement in progress, coming soon.* Stage 3 is being re-run after an OOM in the initial sweep — see MILESTONES.md.

Run the harness yourself:

```bash
export MEMDB_SERVICE_SECRET=$(docker exec memdb-go env | grep INTERNAL_SERVICE_SECRET | cut -d= -f2)
LOCOMO_SKIP_CHAT=1 OUT_SUFFIX=local bash evaluation/locomo/run.sh
```

---

## Roadmap

Active backlogs (closed `ROADMAP-GO-MIGRATION.md` kept as historical record):

- [docs/backlog/search.md](docs/backlog/search.md) — retrieval quality (target: LoCoMo > 75)
- [docs/backlog/add-pipeline.md](docs/backlog/add-pipeline.md) — ingest pipeline excellence
- [docs/backlog/features.md](docs/backlog/features.md) — features beyond upstream Python (image memory, MemCube cross-sharing, …)

---

## Versioning

MemDB is `0.x.y` until we commit to API stability. Expect minor breaking changes between
`0.y` releases — called out in [CHANGELOG.md](CHANGELOG.md) with migration notes. `0.22.0`
is the first public launch tag and resets the version line from the pre-public `2.x`
internal sequence to a `0.x` series that signals the API contract is not yet frozen.

---

## Configuration

Key environment variables (full list in `.env.example`):

| Variable | Default | Description |
|---|---|---|
| `MEMDB_LLM_PROXY_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `MEMDB_LLM_API_KEY` | — | API key for the LLM provider |
| `MEMDB_LLM_MODEL` | `gpt-4o-mini` | Model name |
| `MEMDB_EMBEDDER_TYPE` | `http` | `http` (embed-server), `ollama`, or `onnx` |
| `MEMDB_EMBED_URL` | — | Base URL for embed-server (when type=http) |
| `POSTGRES_PASSWORD` | — | Required; no default |
| `MEMDB_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CROSS_ENCODER_URL` | — | Cohere-compatible reranker base URL. Empty disables rerank. |
| `CROSS_ENCODER_MODEL` | `gte-multi-rerank` | Model name passed to the reranker. |
| `CROSS_ENCODER_API_KEY` | — | Bearer token for hosted rerankers (Cohere/Jina/Voyage). |

See [docs/llm-providers.md](docs/llm-providers.md) for provider-specific configuration
(Ollama, OpenRouter, Gemini, LiteLLM) and reranker setup.

---

## Claude Desktop Integration (MCP)

```bash
cd memdb-go
CGO_ENABLED=0 go build -o ~/bin/mcp-stdio-proxy ./cmd/mcp-stdio-proxy
```

Then copy `examples/mcp/claude-desktop/claude_desktop_config.json.example` into your
Claude Desktop config and restart. Walkthrough:
[examples/mcp/claude-desktop/README.md](examples/mcp/claude-desktop/README.md).

---

## Contributing

Pull requests, issues, and design discussion are welcome.

- [CONTRIBUTING.md](CONTRIBUTING.md) — dev setup, branch naming, PR checklist
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- [SECURITY.md](SECURITY.md) — vulnerability disclosure
- [GitHub Discussions](https://github.com/anatolykoptev/memdb/discussions) — questions and design ideas
- [Discord](https://discord.gg/8vhbTZgf) — chat with maintainers and other users

<!-- TODO star-history.svg: embed star-history.com chart for anatolykoptev/memdb after launch -->
<!-- <p align="center"><a href="https://star-history.com/#anatolykoptev/memdb&Date"><img src="docs/assets/star-history.svg" alt="MemDB star history" width="80%" /></a></p> -->

---

## Acknowledgments

MemDB is a hard fork of [MemOS](https://github.com/MemTensor/MemOS) by MemTensor. The
original research paper — *MemOS: A Memory OS for AI System*
([arXiv:2507.03724](https://arxiv.org/abs/2507.03724)) — describes the cube-based memory
design and Memory-Augmented Generation (MAG) concept this codebase is built on.

If you use MemDB in research, please cite the original MemOS papers:

```bibtex
@article{li2025memos_long,
  title={MemOS: A Memory OS for AI System},
  author={Li, Zhiyu and Song, Shichao and Xi, Chenyang and Wang, Hanyu and others},
  journal={arXiv preprint arXiv:2507.03724},
  year={2025},
  url={https://arxiv.org/abs/2507.03724}
}

@article{li2025memos_short,
  title={MemOS: An Operating System for Memory-Augmented Generation (MAG) in Large Language Models},
  author={Li, Zhiyu and Song, Shichao and Wang, Hanyu and others},
  journal={arXiv preprint arXiv:2505.22101},
  year={2025},
  url={https://arxiv.org/abs/2505.22101}
}
```

---

## License

Apache 2.0 — see [LICENSE](LICENSE).

---

<div align="center">

**[⭐ Star us on GitHub](https://github.com/anatolykoptev/memdb)** ·
**[💬 Join Discord](https://discord.gg/8vhbTZgf)** ·
**[📖 Docs](docs/)** ·
**[🗺️ Roadmap](#roadmap)**

Built with care in Go. Apache 2.0. v0.22.0.

</div>
