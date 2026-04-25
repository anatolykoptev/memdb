# MemOS Upstream — Comparative Analysis (April 2026)

> Snapshot. MemOS = our hard-fork parent project ([MemTensor/MemOS](https://github.com/MemTensor/MemOS)).
> Latest upstream commit at analysis time: v2.0.14 ("Stardust"), 2026-04-23.
> Latest MemDB commit: v0.22.0, 2026-04-26.
>
> Source: `mcp__go-code__explore` on both repos.

## Headline numbers

| Metric | MemDB (us) | MemOS upstream | Delta |
|---|---|---|---|
| Files | 525 | 1,394 | -62% |
| Lines | 93,151 | 292,619 | -68% |
| Dominant language | Go 70% | TypeScript 45% + Python 39% | different stack |
| **Health score** | **74 / B** | 56 / D | +30% (cleaner) |
| **Dead code symbols** | **1** | 297 | -296 |
| External deps | 61 | 912 | -93% surface |
| **Vulnerable deps** | **1** | 34 | -33 |
| Dep freshness | **75%** | 23% | +52pp |
| Semantic communities | 898 | 3,249 | tighter scope |
| Latest release | v0.22.0 (2026-04-26) | v2.0.14 (2026-04-23) | both active |

## Where we are stronger

1. **Pure-Go backend** — single binary, ~15-30ms latency vs upstream Python ~200ms / TS ~100ms.
2. **Code health (74/B vs 56/D)** — disciplined linting, near-zero dead code, no import cycles.
3. **Security baseline** — 1 vulnerable dep vs their 34. We bump dependencies regularly.
4. **Apache AGE on Postgres** — single graph + vector engine. Upstream uses Neo4j + Milvus = three separate datastores to operate.
5. **MCP server included** — `memdb-mcp` ships with the gateway. Upstream requires a separate plugin.
6. **Memobase port (M9)** — LLM Judge measurement comparable with public leaderboard. Upstream has not adopted this.
7. **Auto-changelog cycle** — release-drafter + goreleaser + changelog-sync.yml validated end-to-end on v0.22.0.
8. **Deploy simplicity** — one `docker compose up`. Upstream has desktop app + browser plugin + cloud + helm; high entry cost.

## Where MemOS upstream is stronger (selective takeaways for us)

### 1. L1 / L2 / L3 memory layer hierarchy

Files: `apps/memos-local-plugin/core/memory/{l1,l2,l3}/`

Formal three-layer academic taxonomy: instant (L1), working (L2), long-term (L3). Matches their paper [arxiv 2507.03724].

**For us**: we have the same functional split (working memory in Redis VSET, episodic in Postgres, long-term LTM) but no `level` API contract. Adding `level: l1|l2|l3` as a query parameter would help users coming from MemOS docs without internal restructuring.

**Effort**: S (1-2 days, pure API skin).

### 2. Skill system as user-facing concept

Files: `apps/openwork-memos-integration/.../skills/{ask-user-question,dev-browser,file-permission,safe-file-deletion}`

Pluggable agent skills with manifest. Each skill is a self-contained capability the agent can invoke.

**For us**: our MCP tools are functionally equivalent but framed as "tools" not "skills". Considered SOFT differentiator — names matter for adoption when comparing to plugin ecosystems.

**Effort**: not actionable as backend change — closer to UX/positioning.

### 3. Reward / feedback closed loop

Files: `apps/memos-local-plugin/core/reward/`

Feedback-driven adjustment of memory importance, retrieval weights, and extract-prompt examples. Closes the RL-like loop on user corrections.

**For us**: `mem_feedback` handler exists but only logs feedback events; doesn't feed back into ranking weights or extract-prompt fine-tuning. Real closed loop is M11+ candidate.

**Effort**: M (1-2 weeks).

### 4. Helm charts for Kubernetes

Files: `deploy/helm/templates/`

Standard chart for K8s deployment. Required for enterprise self-host evaluation.

**For us**: only docker-compose. Easy add — no code changes, reuses image we already build via goreleaser.

**Effort**: S (1-2 days).

### 5. Multi-language i18n in admin UI

Files: `apps/memos-local-plugin/web/src/stores/i18n.ts` (546 t() invocations)

If/when MemDB adds a web admin UI, i18n from day one is cheap; retrofitting is expensive.

**For us**: no admin UI exists. Out of current scope. Note for future GUI work.

### 6. OpenClaw browser-automation integration

Files: `apps/MemOS-Cloud-OpenClaw-Plugin/`, `apps/memos-local-openclaw/`

Capture/embed/recall pipeline for web pages browsed by an agent.

**For us**: out of scope. Browser automation is a separate product surface; we are the memory layer it would call into, not the browser itself.

## Where MemOS upstream has problems we should learn from

### Sprawl

- **1,394 files, 297 dead-code symbols, 34 vulnerable deps**
- Latest commit: 1,514 files changed in one push — release dump, not a PR.
- Three desktop apps + cloud plugin + CLI + helm + paper-academic refs in one monorepo.
- Health 56/D reflects unmanaged growth, not bad initial design.

**For us**: keep the discipline. Per-PR scope, dozor path-filter / debounce, dead-code reviews, regular dep bumps. The 30-point Health gap (74 vs 56) is what reviewers and contributors notice on first read.

### Stack fragmentation

TypeScript + Python + Electron + helm + cloud — every layer is a different operational surface. Upstream is becoming a platform, not a library. Operationally that is hard to maintain and harder to evaluate.

**For us**: pure Go gateway + thin Rust embed-server sidecar + Postgres = three things to operate. Stay there.

## Strategic positioning

| Dimension | MemDB (us) | MemOS upstream |
|---|---|---|
| Product shape | infrastructure component / library | end-user productivity suite |
| Audience | developers building AI agents | end users + dev |
| Deploy | one docker-compose | desktop app + browser plugin + cloud + helm |
| Quality north-star | LLM Judge / F1 / latency | DAU + accumulated user memory |
| Strategy | "best memory backend, period" | "MemOS is the memory OS platform" |

**Our niche**: backend-first, the "Cloudflare KV / Workers" of AI agent memory — simple, fast, self-hostable. Their niche: full memory-OS platform.

**Implication for our roadmap**: take the three small/medium technical takeaways above (L1/L2/L3 API, helm, reward loop). Skip the desktop app, browser plugin, and monorepo sprawl. Ship the focused product.

## Recommendations summary (transferred to ROADMAP.md M10/M11 candidates)

| Item | Size | When |
|---|---|---|
| L1/L2/L3 API contract | S | M10 (next sprint) |
| Helm chart | S | M10 (next sprint) |
| Reward / feedback closed loop | M | M11 |

What we explicitly do not take: Electron desktop app, OpenClaw browser integration, kitchen-sink monorepo structure. Different product, different focus.
