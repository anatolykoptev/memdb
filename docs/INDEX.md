# Documentation index

| Document | Description |
|---|---|
| [README](../README.md) | What MemDB is, quick start, Helm install, configuration, benchmarks and roadmap summary |
| [CLAUDE](../CLAUDE.md) | Rules for coding agents: eval approval policy, add-mode contract, layout, gate and invariants |
| [CHANGELOG](../CHANGELOG.md) | Release history of the service, synced from release-please |
| [CONTRIBUTING](../CONTRIBUTING.md) | Development setup, the CI gate, code style, commit conventions and pull-request process |
| [SECURITY](../SECURITY.md) | How to report a vulnerability privately |
| [CODE_OF_CONDUCT](../CODE_OF_CONDUCT.md) | Contributor Covenant Code of Conduct |
| [ROADMAP](../ROADMAP.md) | Shipped work, active workstreams, next milestones and what is deliberately out of scope |
| [LICENSE](../LICENSE) | Apache License 2.0 |
| [API](API.md) | HTTP API reference with curl examples, the auth flow, environment gates and performance notes |
| [README](README.md) | Short list of the documents kept next to the code, predating this index |
| [ROADMAP-GO-MIGRATION](ROADMAP-GO-MIGRATION.md) | History and status of the migration from the Python backend to the pure-Go service, completed 2026-04-26 |
| [architecture/README](architecture/README.md) | How the LikeC4 architecture diagrams and the service catalog descriptor are built and published |
| [architecture/reward-loop](architecture/reward-loop.md) | Design of the closed loop that captures user corrections, curates them and injects them into extraction |
| [assets/README](assets/README.md) | Inventory and recipes for the visual assets shown in the top-level README |
| [backlog/add-pipeline](backlog/add-pipeline.md) | Open improvement items for the add (ingest) pipeline |
| [backlog/features](backlog/features.md) | Backlog of planned features such as image memory and cube sharing |
| [backlog/search](backlog/search.md) | Backlog of retrieval-quality work: deep search, rerank strategies, multi-vector search |
| [competitive/2026-03-add-pipeline-vs-rivals](competitive/2026-03-add-pipeline-vs-rivals.md) | March 2026 comparison of MemDB's add pipeline with competing memory systems |
| [competitive/2026-04-26-memobase-deep-dive](competitive/2026-04-26-memobase-deep-dive.md) | Analysis of how Memobase reached its published LoCoMo score and what MemDB can borrow |
| [competitive/2026-04-26-memory-frameworks-survey](competitive/2026-04-26-memory-frameworks-survey.md) | Survey of open-source memory frameworks with the techniques worth porting |
| [competitive/2026-04-26-memos-upstream-analysis](competitive/2026-04-26-memos-upstream-analysis.md) | Comparison of MemDB with its upstream MemOS as of April 2026 |
| [competitive/2026-04-26-port-target-expired-at](competitive/2026-04-26-port-target-expired-at.md) | Port proposal for soft-delete with temporal invalidation through an expired_at field |
| [competitive/2026-04-26-port-target-profile-context-injection](competitive/2026-04-26-port-target-profile-context-injection.md) | Port proposal for injecting the user profile into the extraction prompt |
| [competitive/2026-04-26-port-target-vec-cot](competitive/2026-04-26-port-target-vec-cot.md) | Port proposal for multi-vector chain-of-thought search |
| [competitive/2026-04-search-pipeline-vs-rivals](competitive/2026-04-search-pipeline-vs-rivals.md) | April 2026 comparison of MemDB's search pipeline with competing systems |
| [d3-gate-audit](d3-gate-audit.md) | Audit of the gates in the D3 hierarchical tree reorganizer |
| [design/2026-04-25-sliding-window-decision](design/2026-04-25-sliding-window-decision.md) | Decision to make the sliding-window size configurable per request as an additive opt-in |
| [design/2026-04-26-cot-decomposition](design/2026-04-26-cot-decomposition.md) | Design of chain-of-thought query decomposition for multi-hop and temporal questions |
| [design/2026-04-26-d2-multihop-diagnosis](design/2026-04-26-d2-multihop-diagnosis.md) | Diagnosis and fix of the D2 multi-hop retrieval failure |
| [design/2026-04-26-structural-edges-at-ingest](design/2026-04-26-structural-edges-at-ingest.md) | Design for creating structural graph edges at ingest time |
| [eval/2026-04-26-m8-cat5-adversarial-analysis](eval/2026-04-26-m8-cat5-adversarial-analysis.md) | Diagnosis of the adversarial LoCoMo category and the recommended handling |
| [eval/2026-04-29-f3-f11-state](eval/2026-04-29-f3-f11-state.md) | Production verification of the event extractor and bi-temporal edges on 2026-04-29 |
| [eval/2026-04-29-m16-ab-tuning-sweep](eval/2026-04-29-m16-ab-tuning-sweep.md) | A/B tuning sweep of retrieval knobs run before a full-corpus benchmark |
| [eval/2026-04-29-m9-dual-speaker-server](eval/2026-04-29-m9-dual-speaker-server.md) | Server-side dual-speaker retrieval added for the LoCoMo harness |
| [integrations/README](integrations/README.md) | Overview of the three Claude integration surfaces and when to use each |
| [integrations/claude-api-memory-tool](integrations/claude-api-memory-tool.md) | Python adapter that backs Anthropic's memory tool with MemDB |
| [integrations/claude-code-mcp](integrations/claude-code-mcp.md) | How to register and use the MemDB MCP server from Claude Code and other MCP clients |
| [integrations/claude-code-plugin](integrations/claude-code-plugin.md) | The Claude Code plugin that injects and extracts memory through hooks |
| [internal-dev](internal-dev.md) | Notes on the container topology, ports and internals for people working on the service |
| [llm-providers](llm-providers.md) | Configuring LLM providers (OpenAI, Ollama, Gemini, LiteLLM) and the reranker |
| [marketing/competitive-comparison](marketing/competitive-comparison.md) | Competitive comparison table used for the v0.23.0 launch |
| [marketing/launch-article](marketing/launch-article.md) | Launch article on the LoCoMo result and the cost of agent memory |
| [marketing/launch-checklist](marketing/launch-checklist.md) | Two-week launch rollout plan with concrete tasks |
| [marketing/objection-rebuttals](marketing/objection-rebuttals.md) | Prepared answers to likely objections on Hacker News, Reddit and Discord |
| [marketing/show-hn-post](marketing/show-hn-post.md) | Draft of the Show HN post for the launch |
| [marketing/twitter-thread](marketing/twitter-thread.md) | Draft of the launch thread for the v0.23.0 release |
| [memdb-go/README](memdb-go/README.md) | Module map of the Go service (Russian) |
| [memdb-go/compare](memdb-go/compare.md) | Assessment of the Go service against competing projects, February 2026 (Russian) |
| [memdb-go/db](memdb-go/db.md) | The database layer: connections, queries and migrations (Russian) |
| [memdb-go/embedder](memdb-go/embedder.md) | The embedder backends and how vectors are produced (Russian) |
| [memdb-go/handlers](memdb-go/handlers.md) | The HTTP handlers and their routes (Russian) |
| [memdb-go/llm](memdb-go/llm.md) | Fact extraction and deduplication with the LLM (Russian) |
| [memdb-go/mcp](memdb-go/mcp.md) | The built-in Model Context Protocol server (Russian) |
| [memdb-go/scheduler](memdb-go/scheduler.md) | The background scheduler and its tasks (Russian) |
| [memdb-go/search](memdb-go/search.md) | The search pipeline stages (Russian) |
| [memdb-go/server](memdb-go/server.md) | The HTTP server and its middleware (Russian) |
| [overview-ru](overview-ru.md) | Long-form overview of MemDB for readers of Russian |
| [overview](overview.md) | Long-form overview: origin, academic foundation and how MemDB differs from MemOS |
| [perf/2026-04-25-m7-latency-report](perf/2026-04-25-m7-latency-report.md) | Latency results of the M7 sprint: embed batching and factual chat |
| [perf/2026-04-26-factual-canary-design](perf/2026-04-26-factual-canary-design.md) | Design and runbook for the factual answer-style canary |
| [perf/2026-04-26-stage3-memory-profile](perf/2026-04-26-stage3-memory-profile.md) | Playbook for profiling the out-of-memory crash seen in the stage 3 ingest |
| [postmortems/2026-04-29-silent-fast-dedup-cross-session](postmortems/2026-04-29-silent-fast-dedup-cross-session.md) | Postmortem of fast-mode dedup silently dropping sessions during ingest |
| [process/2026-04-25-compound-sprint-orchestration-pattern](process/2026-04-25-compound-sprint-orchestration-pattern.md) | Retrospective on how the M7 sprint was orchestrated across parallel streams |
| [release-notes/v0.22.0](release-notes/v0.22.0.md) | Release notes for the first public release |
| [release-notes/v0.23.0](release-notes/v0.23.0.md) | Release notes for v0.23.0: user profiles, performance and a security audit |
| [scheduler-skills-hot-reload](scheduler-skills-hot-reload.md) | Operator guide to reloading scheduler prompts from a directory without a rebuild |
| [testing/2026-04-25-m7-regression-report](testing/2026-04-25-m7-regression-report.md) | M7 regression report, with no regressions confirmed |
| [tools/devto-publish](tools/devto-publish.md) | Usage of the dev.to publishing script |
| [versioning](versioning.md) | Versioning policy and why the public series restarted at 0.22.0 |
