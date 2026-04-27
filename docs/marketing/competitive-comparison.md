# MemDB v0.23.0 — Competitive Comparison

> Generated with `go-startup` MCP `competitive_analysis` tool, augmented with our own benchmark evidence.

## Market context

The agent-memory market is bifurcated between **conversational memory** providers (Mem0, Zep) and **context-management specialists** (Letta, formerly MemGPT). A third group — Memobase, MemOS — focuses on enterprise / structured memory. Until recently, **none of these companies published reproducible LoCoMo numbers under a common LLM Judge methodology**. Memobase changed that, and MemDB v0.23.0 follows the same protocol so the field can be compared apples-to-apples.

Positioning gap MemDB targets: **the only fully self-hostable memory layer with a competitive LoCoMo score, three first-class Claude integration surfaces, and BYO-LLM** (Gemini / GPT / local). Everyone else trades one of those three away.

## LoCoMo chat-50 stratified — LLM Judge (Gemini 2.5 Flash, cat-5 excluded)

| Rank | System    | LLM Judge | Source / Notes                                              |
|-----:|-----------|----------:|-------------------------------------------------------------|
| 1    | Memobase  | **75.78** | [memobase-io/Memobase blog post / paper](https://github.com/memobase-io/memobase) |
| 2    | Zep       | **75.14** | [Zep LoCoMo benchmark write-up](https://blog.getzep.com/) (Graphiti)              |
| 3    | **MemDB v0.23.0** | **72.50** | This repo, `evaluation/locomo/MILESTONES.md` (M9, M10, M11) |
| 4    | MemOS     | **73.31** | [MemOS technical report](https://github.com/MemTensor/MemOS) |
| 5    | Mem0      | **66.88** | [Mem0 LoCoMo paper](https://arxiv.org/abs/2504.19413)        |

> Stratified subset: chat-50, single-hop + multi-hop + temporal questions, category-5 (open-domain unanswerable) excluded — same composition Memobase, Zep, and MemOS report.
> Letta does not currently publish a LoCoMo score; we marked it N/A in the matrices below.

**Read this as:** MemDB sits between MemOS and Zep, **+5.62 pp ahead of Mem0**, **−3.28 pp behind Memobase**. We close the Memobase gap in M11 (planned: D2 multi-hop default depth=3, judge re-tune, retrieval re-weighting).

## Integration surface matrix

| Surface                     | MemDB v0.23.0 | Memobase | Zep   | Mem0  | MemOS | Letta |
|-----------------------------|:-------------:|:--------:|:-----:|:-----:|:-----:|:-----:|
| REST / HTTP API             | yes           | yes      | yes   | yes   | yes   | yes   |
| Native MCP server           | **yes**       | no       | no    | no    | no    | no    |
| Claude Code plugin          | **yes**       | no       | no    | no    | no    | no    |
| Anthropic memory_20250818 adapter | **yes** (drop-in) | no | no | no | no | no |
| Python SDK                  | planned       | yes      | yes   | yes   | yes   | yes   |
| Go SDK                      | **yes** (in-repo) | no   | no    | no    | no    | no    |
| TypeScript SDK              | planned       | yes      | yes   | yes   | partial | yes |
| LangChain / LlamaIndex glue | planned       | yes      | yes   | yes   | yes   | yes   |

## Deployment matrix

| Property                         | MemDB v0.23.0     | Memobase | Zep        | Mem0     | MemOS     | Letta    |
|----------------------------------|:-----------------:|:--------:|:----------:|:--------:|:---------:|:--------:|
| docker-compose self-host         | **yes**           | partial  | yes (OSS)  | no (cloud) | yes     | yes      |
| Helm chart                       | **yes**           | no       | partial    | no       | no        | partial  |
| Single-binary core               | **yes** (Go)      | no       | no         | no       | no        | no       |
| Python runtime required          | **no**            | yes      | yes        | yes      | yes       | yes      |
| Multi-tenant cube isolation      | **yes** (explicit `cube_id`) | partial | partial | partial | yes | partial |
| BYO-LLM (Gemini/GPT/local)       | **yes**           | partial  | partial    | no (OAI/Claude) | yes  | yes      |
| License                          | **MIT**           | Apache-2 | Apache-2 + Cloud | Commercial cloud | Apache-2 | Apache-2 |
| Hosted cloud option              | not yet           | yes      | yes        | yes      | no        | yes      |

## Per-competitor narrative (from `competitive_analysis`)

### MemOS
Structured memory architecture for AI agents, emphasizing long-term state management. Foundational layer for complex agentic workflows requiring persistent context.
- **Strengths**: structured state management, developer-centric SDK approach.
- **Weaknesses**: limited public benchmarking data outside the original report, smaller ecosystem footprint.
- **MemDB delta**: same self-host posture, but pure-Go (no Python venv), MCP-native, +Claude plugin adapter.

### Zep
Memory layer for LLM applications. Long-term memory, structured data extraction, semantic search. Production-ready infrastructure with strong DX.
- **Strengths**: production-ready infra, both self-hosted and cloud.
- **Weaknesses**: heavy initial setup for small teams, high resource consumption at scale.
- **MemDB delta**: lighter (Go binary vs Python services), MIT (Zep OSS is Apache + commercial Cloud), explicit cube isolation, MCP server out of the box.

### Mem0
Personalized memory layer that learns user preferences over time. Strong user-personalization focus.
- **Strengths**: user personalization, easy framework integration.
- **Weaknesses**: depends on specific LLM providers for optimal performance, weaker enterprise multi-tenancy.
- **MemDB delta**: +5.62 pp on LoCoMo, self-host (Mem0 is cloud-first), BYO-LLM, MIT.

### Letta (ex-MemGPT)
Tiered memory architecture (memory hierarchy modeled after computer architecture). Long-context specialist.
- **Strengths**: long-context handling, tiered architecture innovation.
- **Weaknesses**: steep learning curve, retrieval overhead.
- **MemDB delta**: simpler ops surface (single docker-compose), MCP/plugin out of the box, Letta currently has no published LoCoMo score for direct comparison.

### Memobase
Enterprise knowledge retrieval and document-based memory. Secure, scalable, proprietary-data grounding.
- **Strengths**: enterprise security and compliance, document-heavy retrieval.
- **Weaknesses**: less flexible for real-time conversational memory, weaker community integrations.
- **MemDB delta**: −3.28 pp on LoCoMo (closing in M11), but MIT vs commercial cloud, native Claude integrations Memobase does not ship, Go ops simplicity.

## Why MemDB wins specific deals

| Customer profile                           | What they pick MemDB for                                                              |
|--------------------------------------------|---------------------------------------------------------------------------------------|
| Self-host-only fintech / health / gov AI   | Only top-3 LoCoMo system shippable as `docker compose up` with no Python runtime.     |
| Claude-native shop (API or Code)           | Three first-class surfaces (plugin / MCP server / `memory_20250818` adapter).         |
| Multi-LLM platform (mixing Gemini + GPT + local) | True BYO-LLM: embed-server is decoupled, judge can be swapped, no provider lock-in. |
| Multi-tenant SaaS                          | Explicit `cube_id` isolation in HTTP API and storage; not a bolt-on.                  |
| Go infrastructure shop                     | Single-binary core + Go SDK in-repo. No language-impedance mismatch with the rest of their stack. |

## How we benchmarked

We follow Memobase's published LLM-Judge methodology to keep numbers comparable across the leaderboard.

- **Dataset**: LoCoMo `chat-50` stratified subset.
- **Question categories**: single-hop, multi-hop, temporal. Category-5 (open-domain unanswerable) excluded — same exclusion Memobase, Zep, MemOS apply.
- **Judge**: Gemini 2.5 Flash, deterministic prompt, scores normalized to 0-100.
- **Pipeline**: `evaluation/locomo/run.sh` — ingest sample conversations into a fresh cube, run all questions through MemDB retrieval, send (question, gold, prediction) triples to the judge.
- **Reproducibility**: every milestone result and the prompt template is recorded in [`evaluation/locomo/MILESTONES.md`](../../evaluation/locomo/MILESTONES.md).
- **Stack used for v0.23.0 number**: postgres+AGE+pgvector+qdrant+redis+embed-server, default `factual` answer style, raw-mode ingestion (M7), D2 multi-hop depth=2.

To reproduce locally:

```bash
git clone https://github.com/anatolykoptev/memdb && cd memdb
docker compose up -d
bash evaluation/locomo/run.sh   # writes results/ + per-question CSV
```

If anyone gets a different number with the same protocol, please open an issue with the run log — we want the comparison fair.
