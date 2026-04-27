# MemDB v0.23.0 + memdb-claude-memory-tool v0.1.0 — Launch Checklist

Two-week rollout plan. Each item is concrete enough to execute or delegate without re-planning.

## Week 0 — today (prep, no public posts yet)

- [x] Run `competitive_analysis` (go-startup MCP) → `docs/marketing/competitive-comparison.md`
- [x] Draft Show HN post → `docs/marketing/show-hn-post.md`
- [x] Generate objection rebuttals via `narrative_coach` → `docs/marketing/objection-rebuttals.md`
- [x] This checklist → `docs/marketing/launch-checklist.md`
- [ ] Verify [`memdb-claude-memory-tool` README quickstart](https://github.com/anatolykoptev/memdb-claude-memory-tool) works copy-paste from a clean venv against `localhost:8080`
- [ ] Add benchmark badge + chart PNG to MemDB top-level `README.md` (chart: bar plot of LoCoMo LLM Judge across 5 systems, MemDB highlighted)
- [ ] Generate the chart with `evaluation/locomo/results/` data (matplotlib or plotly, commit PNG only — no notebook)
- [ ] PyPI publish `memdb-claude-memory-tool==0.1.0`: `python -m build && twine upload dist/*` (manual; need PYPI_API_TOKEN locally)
- [ ] Tag `memdb` repo `v0.23.0` if not already tagged; verify GoReleaser action ran and binaries are on the GitHub release page
- [ ] Smoke-test the three Claude surfaces in a fresh container: plugin auto-injection visible, MCP server returns memories on `recall`, adapter responds to `memory_20250818` tool call

## Week 1 — public launch

### Day 1 (Tuesday) — Show HN
- [ ] **08:30 UTC** post `docs/marketing/show-hn-post.md` to Hacker News (Show HN tag, link to repo, no marketing voice)
- [ ] Stand by in thread for first 4 hours — paste rebuttals from `objection-rebuttals.md` as objections appear, never copy-paste verbatim, always add the specific user's context
- [ ] Pin GitHub issue: "Show HN feedback thread" linking back to the HN post
- [ ] Post short Twitter / X thread (5 tweets): hook from `elevator_pitch`, benchmark table, install snippet, adapter snippet, repo link
- [ ] Submit to Lobsters under `release` and `ai` tags
- [ ] Post to `r/LocalLLaMA` — angle: "Self-hostable agent memory, no Python, MIT, beats Mem0 by 5.6 pp on LoCoMo"

### Day 2 — Reddit + Discord saturation
- [ ] Post to `r/MachineLearning` ([P] tag, project post, focus on benchmark methodology not marketing)
- [ ] Post to `r/ClaudeAI` — angle: "Three Claude integrations (plugin / MCP / memory_20250818 adapter)"
- [ ] Post to `r/selfhosted` — angle: docker-compose, no Python runtime
- [ ] Post in MCP Discord (Anthropic-run + community ones) — focus: MCP server is in-tree, not an afterthought
- [ ] Post in `Latent Space` Discord, `LangChain` Discord — feedback solicitation, not a pitch

### Day 3 — direct outreach
- [ ] Email / DM Memobase team — peer comparison, ask if our methodology matches theirs exactly, offer joint blog
- [ ] Email / DM Zep team (Daniel Chalef) — same
- [ ] Email Letta team — invite them to publish their LoCoMo number so we can add them to the matrix
- [ ] Open issue in `anthropics/anthropic-cookbook` — propose the adapter as the canonical example for `memory_20250818` external storage

### Day 4 — content
- [ ] Publish blog post on personal site / Substack: "How we hit 72.5% on LoCoMo with a Go binary" — methodology-first, no pitch in first 80% of post
- [ ] Submit blog post to HN as a follow-up (different submitter ideally; don't double-submit Show HN content)
- [ ] Cross-post blog to dev.to and lobste.rs
- [ ] Record 5-min screencast: install → ingest → recall → switch to MCP host → drop adapter into Claude API call. Upload to YouTube + embed in README.

### Day 5 — first retrospective
- [ ] Tally HN points, comments, GitHub stars delta, repo unique cloners (insights tab)
- [ ] Triage all GitHub issues filed during the week — reply within 24h, label `launch-week`
- [ ] Decide whether to push the chart-and-blog combo as a Tuesday week-2 second wave, or sit and let traffic compound

### Day 6-7 — weekend
- [ ] Lightweight: respond to remaining issues / PRs, do not start new threads
- [ ] Draft week-2 plan based on what landed

## Week 2 (Days 8-14) — depth + ecosystem

### Day 8 (Tuesday) — second wave if signal is strong
- [ ] If week-1 HN cleared 100 points: publish "Architecture deep-dive" blog (Postgres + AGE + pgvector + qdrant retrieval fusion). Submit to HN.
- [ ] If week-1 HN was quiet: revise hook based on objection patterns observed, re-submit blog only (not Show HN)

### Day 9 — partner integrations
- [ ] Open PR to `langchain` repo: `MemDBMemory` retriever (Python)
- [ ] Open PR to `llamaindex` repo: same
- [ ] Open PR to `mastra` (TypeScript agent framework): MemDB store adapter
- [ ] Open PR to `agentscope`: MemDB binding

### Day 10 — Anthropic ecosystem
- [ ] Submit to Anthropic's MCP server registry
- [ ] Submit to `awesome-mcp-servers`
- [ ] Submit to `awesome-claude-code` (plugin section)
- [ ] If adapter is downloading well on PyPI — pitch Anthropic DevRel for a tutorial co-write

### Day 11 — analyst / press
- [ ] Email TheNewStack, InfoQ, The Register — angle: "Open-source competitor to Mem0/Zep with reproducible benchmark"
- [ ] Email Latent Space podcast (swyx) — pitch for a future episode

### Day 12 — second screencast / talk material
- [ ] Record "Migrating from filesystem `memory_20250818` to MemDB" screencast (10 min)
- [ ] Submit talk proposal to next AI Engineer Summit / KubeCon AI day

### Day 13 — community
- [ ] First contributor sync (Discord voice, 30 min) — open invite via GitHub Discussions
- [ ] Tag good-first-issues, label `help-wanted`, link from README

### Day 14 — retrospective + M11 kickoff
- [ ] Publish two-week launch retrospective (numbers + what worked + what flopped) — short blog post, link from README
- [ ] Kick off M11: D2 multi-hop default depth bump, judge re-tune, retrieval re-weighting (target: close Memobase gap)

## Success metrics per channel

| Channel              | Metric                               | Threshold (good) | Threshold (great) |
|----------------------|--------------------------------------|-----------------:|------------------:|
| Hacker News (Show HN)| points / front-page time             | 50 pts / 2h      | 200 pts / front page |
| Hacker News comments | substantive technical comments       | 20               | 60                |
| GitHub stars (week)  | stars added in 7 days                | 100              | 500               |
| GitHub clones        | unique cloners (Insights → Traffic)  | 200              | 1000              |
| PyPI downloads       | `memdb-claude-memory-tool` weekly    | 100              | 1000              |
| Repo issues          | new issues filed (signal of use)     | 5 quality        | 20 quality        |
| Reddit r/LocalLLaMA  | upvotes / comments                   | 50 / 10          | 300 / 50          |
| Reddit r/ML [P]      | upvotes                              | 30               | 100               |
| Discord pickups      | mentions in MCP / LangChain Discord  | 3                | 10                |
| Inbound DMs          | "we want to use this in production"  | 2                | 10                |
| Memobase team reply  | acknowledgement + methodology check  | reply received   | joint blog agreed |

## Anti-goals (do not do)

- No paid ads in week 1-2 — we want organic signal first.
- No "MemDB is better than X" headline phrasing anywhere; always relative ("between MemOS and Zep") with the table.
- No deleting or hiding negative HN comments; respond once, then let them stand.
- No `gh pr merge` from launch-related PRs by anyone but the controller (per repo `CLAUDE.md`).
- No Instagram (banned in RU per user prefs); no Perplexity references; LinkedIn fine but low priority.
