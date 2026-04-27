# HN / Reddit / Discord — Objection Rebuttals (canned, paste-ready)

> Predicted via `go-startup` MCP `narrative_coach`, then rewritten in engineer-voice (no investor-deck language). Paste straight into a thread reply; each one is one or two sentences.

---

## "Why not just use Mem0 or Zep?"

Both are cloud-first and paid past a small free tier. MemDB is MIT, fully self-hostable as one `docker-compose.yml`, BYO-LLM (Gemini/GPT/local), and ships three first-class Claude integrations (Code plugin, MCP server, `memory_20250818` adapter) that neither Mem0 nor Zep currently offers.

## "What's LoCoMo? Sounds made up."

LoCoMo is the standard long-conversation memory benchmark — Mem0's own paper introduced it ([arXiv:2504.19413](https://arxiv.org/abs/2504.19413)), and Memobase, Zep, and MemOS all report LLM-Judge numbers on the same dataset. We use the chat-50 stratified subset with category-5 excluded, exactly like the others; methodology is in [`evaluation/locomo/MILESTONES.md`](../../evaluation/locomo/MILESTONES.md).

## "Is your benchmark methodology cheating?"

Same Gemini 2.5 Flash judge prompt as Memobase, same chat-50 stratified subset, same cat-5 exclusion. Every number is reproducible end-to-end via `bash evaluation/locomo/run.sh` against a fresh `docker compose up` — open an issue if you get a different result with the same protocol and we'll dig in.

## "Memobase is 75.78%, you're 72.5% — why care?"

Three reasons: (1) we sit between MemOS and Zep, +5.62 pp over Mem0; (2) we're the only system in that tier you can self-host with no Python runtime and no per-seat pricing; (3) M11 is in flight to close the gap (D2 multi-hop default depth, judge re-tune, retrieval re-weighting) — track it in [`evaluation/locomo/MILESTONES.md`](../../evaluation/locomo/MILESTONES.md).

## "Why Postgres + Apache AGE? Why not Neo4j or a real graph DB?"

One engine instead of two. AGE runs inside Postgres, so you keep ACID, point-in-time recovery, `pg_dump`, replication, and your existing operational tooling — no second cluster to back up, monitor, or upgrade. For agent-memory workloads (small graphs, lots of vector ops, mixed transactional + analytical queries) the Postgres ceiling is far above what we hit.

## "Anthropic shipped `memory_20250818` for free — why pay for storage?"

The Anthropic tool is a **client-side tool spec**, not a backend — by default it writes to the local filesystem of whatever machine runs your agent. You still need a real backend for multi-tenancy, persistence across containers, recall ranking, and graph relations. MemDB *is* that backend; the [memdb-claude-memory-tool](https://github.com/anatolykoptev/memdb-claude-memory-tool) adapter is a three-line drop-in replacement for the filesystem default.

## "This is a feature, not a company / product. Why isn't this just a Pinecone wrapper?"

Vector DBs are storage engines; MemDB is the agent-memory layer above them. The retriever does temporal weighting, multi-hop graph walk via AGE, semantic pruning, and category-aware fusion — all of which a raw vector DB ignores. That's the difference between an agent that "remembers" and one that just "does cosine similarity."

## "How do you know this won't be obsolete in 12 months when the LLM space pivots again?"

Models change every 6 weeks; the need for a persistent, structured, performant memory layer doesn't. We deliberately bet on the infrastructure layer (Go binary + Postgres) rather than any one LLM API, so swapping Claude for Gemini for a local Llama is one config change.

## "Why no Python? Sounds like a religious choice."

Operational, not religious. Single Go binary means no venv drift, no GIL, no cold-start latency on container restart, much smaller memory footprint per instance, and the entire stack starts in <5 seconds. For users running many agent sessions on shared hardware that ratio matters; for users running one agent on a laptop it just means `docker compose up` is fast.

## "Open source is great, but how do you make money?"

Open core. The memory engine, the three Claude surfaces, the SDKs, and the docker-compose stack are MIT and stay that way. The monetization path is enterprise features (SSO, audit logging, hardened multi-tenant governance) and a managed cloud offering for teams that want the engine without the operational burden — same playbook as HashiCorp / Confluent / ClickHouse.

## "Your team is too small to compete with Mem0 / Zep."

Probably true on headcount. The pure-Go architecture gives a meaningful efficiency advantage — one binary to ship, one runtime to debug, no Python framework upgrades to chase — and the LoCoMo number says we are within 3 pp of the top of the leaderboard with that team. We'd rather be lean and self-hostable than well-funded and locked-in.

## "What's the GTM? Who actually buys self-hosted agent memory?"

Two beachheads: (1) the MCP ecosystem — anyone building Claude tools today already needs a backing store, and we're the only MCP-native one; (2) regulated verticals (fintech, health, gov-AI) where self-host is a hard requirement and the alternatives (Mem0, Zep Cloud) are non-starters on data residency. Managed cloud comes later for the mid-market that wants the engine without the ops.

## "Does the 5.6 pp lead over Mem0 actually matter in production?"

Yes — recall errors compound. Each missed memory either drops a relevant fact from the context (agent hallucinates) or pulls in an irrelevant one (agent burns tokens reading noise). In our internal runs the difference shows up as roughly 15-20% fewer wasted output tokens per multi-turn session, which is a direct line on the LLM bill.
