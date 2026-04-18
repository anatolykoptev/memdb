# MemDB — Memory Database for AI Agents

[![License](https://img.shields.io/badge/License-Apache_2.0-green.svg?logo=apache)](https://opensource.org/license/apache-2-0/)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8.svg?logo=go)](https://go.dev/)
[![arXiv](https://img.shields.io/badge/arXiv-2507.03724-b31b1b.svg)](https://arxiv.org/abs/2507.03724)
[![Discord](https://img.shields.io/badge/Discord-join%20chat-7289DA.svg?logo=discord)](https://discord.gg/Txbx3gebZR)
[![GitHub Discussions](https://img.shields.io/badge/GitHub-Discussions-181717.svg?logo=github)](https://github.com/anatolykoptev/memdb/discussions)

MemDB is a self-hosted memory database for AI agents. It stores, retrieves, and manages long-term memory — structured as a graph, searchable by vector similarity — and exposes it through a REST API and an MCP server for Claude-style agents.

It runs as a single `docker compose up`: Go API service + Postgres (pgvector + Apache AGE). No external graph database, no proprietary cloud, no Python runtime required in production.

> **MemDB is a hard fork of [MemOS](https://github.com/MemTensor/MemOS).**
> Research and original architecture: © MemTensor. This fork (Apache 2.0) repackages and rebuilds MemOS for independent self-hosting — primarily as a Go service. Full credits and license notes in [Acknowledgments](#acknowledgments).

---

## Why MemDB

- **Go-primary**: the core service (`memdb-go`) is written in Go — single static binary, low memory footprint, straightforward deployment.
- **Single compose stack**: Postgres 17 with pgvector + Apache AGE covers both vector similarity search and graph traversal. No Qdrant, no Neo4j, no Redis required by default.
- **OpenAI-compatible LLM layer**: any provider that speaks `/v1/chat/completions` works — OpenAI, Ollama, LiteLLM, OpenRouter, or a custom proxy. See [docs/llm-providers.md](docs/llm-providers.md).
- **MCP native**: ships `memdb-mcp` + `mcp-stdio-proxy` for zero-config Claude Desktop / Claude Code integration.
- **Local embeddings**: ONNX-based embedder (`multilingual-e5-large`, 1024 dim) runs in-process or as an optional `embed-server` sidecar — no third-party embedding API required.
- **Apache 2.0**: fully open, no usage-based licensing.

---

## Quick Start

```bash
git clone https://github.com/anatolykoptev/memdb
cd memdb
cp .env.example .env
# edit .env: set MEMDB_LLM_API_KEY (OpenAI key or any OpenAI-compatible)
docker compose -f docker/docker-compose.yml up -d
curl http://localhost:8080/health
```

Optional: enable the local embed-server sidecar for ONNX embeddings (no external API):

```bash
docker compose -f docker/docker-compose.yml --profile embed up -d
```

Then set `MEMDB_EMBEDDER_TYPE=http` and `MEMDB_EMBED_URL=http://embed-server:8080` in your `.env`.

---

## Core Concepts

- **Memory** — a single item stored about a user: a fact, preference, episode, tool trace, or skill.
- **Cube** — a namespace for memories. Each cube is isolated; cubes can be scoped per agent, project, or team.
- **User** — an identity within a cube. Memories are stored and retrieved per user.

---

## API Usage

**Add a memory:**

```bash
curl -s -X POST http://localhost:8080/product/add \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "writable_cube_ids": ["my-cube"],
    "messages": [
      {"role": "user", "content": "I prefer concise answers."},
      {"role": "assistant", "content": "Noted."}
    ],
    "async_mode": "sync"
  }'
```

**Search memories:**

```bash
curl -s -X POST http://localhost:8080/product/search \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "alice",
    "readable_cube_ids": ["my-cube"],
    "query": "communication preferences",
    "top_k": 5,
    "mode": "fast"
  }'
```

Full API reference: [docs/openapi.json](docs/openapi.json). Go and Python quickstart examples: [examples/](examples/).

---

## Claude Desktop Integration (MCP)

MemDB ships a Go MCP server. Claude Desktop connects to it over stdio using `mcp-stdio-proxy`.

1. Build the proxy binary once:
   ```bash
   cd memdb-go
   CGO_ENABLED=0 go build -o ~/bin/mcp-stdio-proxy ./cmd/mcp-stdio-proxy
   ```

2. Copy the example config into Claude Desktop's `claude_desktop_config.json`:
   ```
   examples/mcp/claude-desktop/claude_desktop_config.json.example
   ```

3. Restart Claude Desktop. Ask Claude: _"Search my memories for programming preferences."_

See [examples/mcp/claude-desktop/README.md](examples/mcp/claude-desktop/README.md) for the full walkthrough and available MCP tools.

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

See [docs/llm-providers.md](docs/llm-providers.md) for provider-specific configuration (Ollama, OpenRouter, Gemini, LiteLLM).

---

## Architecture

```
Claude Desktop / Claude Code / your app
        │  REST or MCP (stdio)
        ▼
   memdb-go :8080
   ┌─────────────────────────────────────┐
   │  handlers  │  search  │  scheduler  │
   │  embedder  │  graph   │  MCP server │
   └─────────────────────────────────────┘
        │
        ▼
   Postgres 17
   ├── pgvector  (1024-dim semantic search)
   └── Apache AGE (graph traversal)

   (optional)
   embed-server :8081
   └── ONNX multilingual-e5-large + jina-code-v2
```

`memdb-go` is the primary service: REST handlers, MCP server, semantic search, async memory scheduler, and an internal ONNX embedder that can be swapped for an HTTP sidecar or Ollama. Postgres covers both vector and graph storage, eliminating the need for separate databases.

**Python layer:** `src/` contains the original Python pipeline from the upstream fork. It remains for legacy compatibility during transition. New features target the Go service. See [ROADMAP-GO-MIGRATION.md](ROADMAP-GO-MIGRATION.md) for migration status.

---

## Comparison

Feature comparison based on publicly available information. Performance numbers are not included — run your own benchmarks.

| | MemDB | Mem0 | Zep | MemGPT |
|---|---|---|---|---|
| Self-hosted | Yes (Go + Postgres) | OSS server + paid cloud | Yes (Python) | Yes (Python) |
| License | Apache 2.0 | Apache 2.0 | Apache 2.0 | Apache 2.0 |
| Primary language | Go | Python | Python | Python |
| Vector search | pgvector | — | pgvector | vector store |
| Graph memory | Apache AGE | No | Neo4j | No |
| MCP native | Yes | No | No | No |
| Local embeddings | ONNX sidecar | No | No | No |
| OpenAI-compatible LLM | Yes | Yes | Yes | Yes |

---

## Roadmap

Near-term priorities:

- Complete Python pipeline deprecation — Go-only memory ingestion path (see [ROADMAP-GO-MIGRATION.md](ROADMAP-GO-MIGRATION.md))
- Image memory support — ONNX CLIP embeddings, image + text co-retrieval (see [ROADMAP-FEATURES.md](ROADMAP-FEATURES.md))
- MemCube cross-sharing — access control between cubes
- Enhanced search pipeline — reranking, hybrid search improvements (see [ROADMAP-SEARCH.md](ROADMAP-SEARCH.md))
- Benchmarks on this fork — re-run LoCoMo / LongMemEval against the Go service

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Issues and pull requests are welcome on [GitHub](https://github.com/anatolykoptev/memdb).

---

## Acknowledgments

MemDB is a hard fork of [MemOS](https://github.com/MemTensor/MemOS) by MemTensor.

The original research paper — "MemOS: A Memory OS for AI System" ([arXiv:2507.03724](https://arxiv.org/abs/2507.03724)) — describes the architecture, Memory-Augmented Generation (MAG) concept, and cube-based memory design that this codebase is built on. The MemOS team's work is the foundation of this project.

If you use MemDB in research, please cite the original MemOS papers:

```bibtex
@article{li2025memos_long,
  title={MemOS: A Memory OS for AI System},
  author={Li, Zhiyu and Song, Shichao and Xi, Chenyang and Wang, Hanyu and Tang, Chen and Niu, Simin and Chen, Ding and Yang, Jiawei and Li, Chunyu and Yu, Qingchen and Zhao, Jihao and Wang, Yezhaohui and Liu, Peng and Lin, Zehao and Wang, Pengyuan and Huo, Jiahao and Chen, Tianyi and Chen, Kai and Li, Kehang and Tao, Zhen and Ren, Junpeng and Lai, Huayi and Wu, Hao and Tang, Bo and Wang, Zhenren and Fan, Zhaoxin and Zhang, Ningyu and Zhang, Linfeng and Yan, Junchi and Yang, Mingchuan and Xu, Tong and Xu, Wei and Chen, Huajun and Wang, Haofeng and Yang, Hongkang and Zhang, Wentao and Xu, Zhi-Qin John and Chen, Siheng and Xiong, Feiyu},
  journal={arXiv preprint arXiv:2507.03724},
  year={2025},
  url={https://arxiv.org/abs/2507.03724}
}

@article{li2025memos_short,
  title={MemOS: An Operating System for Memory-Augmented Generation (MAG) in Large Language Models},
  author={Li, Zhiyu and Song, Shichao and Wang, Hanyu and Niu, Simin and Chen, Ding and Yang, Jiawei and Xi, Chenyang and Lai, Huayi and Zhao, Jihao and Wang, Yezhaohui and others},
  journal={arXiv preprint arXiv:2505.22101},
  year={2025},
  url={https://arxiv.org/abs/2505.22101}
}
```

---

## License

Apache 2.0. See [LICENSE](LICENSE).
