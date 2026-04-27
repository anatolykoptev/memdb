# Twitter/X Launch Thread (MemDB v0.23.0)

> Schedule: post during HN Show HN window (Tue/Wed ~9am UTC). 5-tweet thread.
> Author: developer-tone, no corporate-speak. Numbers honest with sources linked.

## Tweet 1 — Hook (≤280 chars including link)

I built an open-source agent memory layer that scores 72.5% on LoCoMo — between Mem0 (66.88%) and MemOS (73.31%) — ships as a single `docker compose up`, runs no Python, and has three first-class Claude integrations.

Thread →

https://github.com/anatolykoptev/memdb

---

## Tweet 2 — The benchmark, with table screenshot

Same Memobase methodology: LoCoMo chat-50 stratified, LLM Judge via Gemini 2.5 Flash, category-5 (unanswerable) excluded. All numbers self-reported by each team.

```
Rank  System            LLM Judge   License
1     Memobase          75.78       Commercial cloud
2     Zep               75.14       Apache-2 OSS + Cloud
3     MemOS             73.31       Apache-2
4     MemDB v0.23.0     72.50       Apache-2
5     Mem0              66.88       Commercial cloud
```

MemDB is +5.62 pp ahead of Mem0 and the only top-4 system you can run without paying anyone or running Python.

Methodology: https://github.com/anatolykoptev/memdb/blob/main/evaluation/locomo/MILESTONES.md

---

## Tweet 3 — The differentiation (3 Claude surfaces)

No other memory system ships all three Claude integration surfaces:

1. **Claude Code plugin** — auto-injects relevant memories before each turn. Zero config.
2. **MCP server** — any MCP host (Claude Desktop / Code / your own). Ships in the same `docker compose up`.
3. **`memory_20250818` adapter** — drop-in replacement for Anthropic's built-in memory tool. Three lines of Python:

```
pip install memdb-claude-memory-tool
```

```python
from memdb_claude_memory_tool import MemDBMemoryTool
tool = MemDBMemoryTool(base_url="http://localhost:8080", cube_id="user_42")
client.messages.create(model="claude-sonnet-4-6", tools=[tool.as_tool_spec()], ...)
```

Adapter repo: https://github.com/anatolykoptev/memdb-claude-memory-tool

---

## Tweet 4 — Self-host story

Why self-host matters in 2025:

- Data residency — your memories, your Postgres
- No per-query billing that scales with agent activity
- BYO-LLM: Gemini, GPT, Claude, or any local OpenAI-compatible endpoint
- Single statically-linked Go binary. No Python venv, no pip freeze, no dependency drift

```bash
git clone https://github.com/anatolykoptev/memdb && cd memdb
docker compose up -d
curl http://localhost:8080/health
# {"status":"ok","version":"0.23.0"}
```

Apache-2.0. Helm chart included for the k8s crowd.

---

## Tweet 5 — Call to action

Three asks:

1. Try it → https://github.com/anatolykoptev/memdb (5-min quickstart in README)
2. Issues / PRs welcome — especially on the adapter: https://github.com/anatolykoptev/memdb-claude-memory-tool
3. Weigh in: **should D2 multi-hop default to depth=3** (better recall, ~30% slower) or stay at depth=2? Closing the Memobase gap in M11 depends on this tradeoff.

Tagging @AnthropicAI — three Claude integration surfaces that no other memory system ships.

If you build agents, MemDB is the memory layer that doesn't ask you to choose between self-host and quality.

---

## Asset list (what needs to be uploaded)

- **Tweet 2 screenshot**: render the ASCII leaderboard table as a clean PNG (dark background, monospace font). Use any terminal screenshot tool or carbon.now.sh.
- **(Optional) GIF**: 30-sec demo — `docker compose up` → `curl /health` → `curl /v1/cubes/demo/memories` (POST) → `curl /v1/cubes/demo/recall?q=...`. Record with asciinema + agg → `docs/assets/demo.gif`.
- **(Optional) Architecture diagram**: already exists at `docs/assets/architecture.svg` — verify it renders correctly before uploading as a static PNG for tweet 4.

---

## Posting checklist

- [ ] Post HN Show HN FIRST — wait 45 minutes, then post tweet 1
- [ ] Schedule via Twitter native scheduler (not third-party — better algorithmic reach)
- [ ] Tue or Wed, 9am UTC
- [ ] Verify all links resolve before scheduling: repo, adapter repo, MILESTONES.md
- [ ] Upload leaderboard screenshot for tweet 2 before scheduling thread
- [ ] Reply to your own thread with the HN link once the Show HN post is live
- [ ] When HN score hits 30+ pts: reply to tweet 5 with HN comment URL
- [ ] Do NOT engage with bait replies in first 4 hours — let organic engagement build

---

## Anti-patterns (do NOT do)

- No "Excited to announce..." opener
- No "Game-changer" / "Revolutionary" claims
- No "Launching today!!" exclamation marks
- No corporate hashtags (#AI #LLM #Innovation)
- No unlinked numbers — 72.5% without MILESTONES.md link = "made up" in dev eyes
- No first tweet starting with a number sign (Twitter buries replies-that-start-with-@)

---

## Tone reference

HashiCorp / DHH / antirez: honest tradeoffs, sharp facts, technical specifics, zero hype.
The framing that works: "here's the problem, here's the number, here's how to reproduce it."
