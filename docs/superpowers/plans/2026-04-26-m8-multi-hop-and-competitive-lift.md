# M8 — Multi-Hop & Competitive Lift Sprint

> **Mission:** push MemDB LoCoMo aggregate F1 from current 0.238 (Stage 2, conv-26 single) into the **0.30+ tier across all 10 conversations** by closing the multi-hop bottleneck (cat-2 F1 = 0.091) and porting the highest-leverage competitor patterns. Settle Stage 3 generalisation question. Stay disciplined: every claim measured, every measurement reproducible.
>
> **Framing:** we are senior researchers + senior engineers shipping the best self-hosted memory system on the market. MemOS sets the public bar; we beat it on retrieval already (Phase D), now we beat it on chat-extraction quality and on multi-hop reasoning. Resources: unlimited specialist team via subagent dispatch, go-code/go-search MCP for everything.
>
> **Controller (TL):** coordinator + dispatcher + merger. Writes no code. Code comes from specialists.
>
> **Foundation:** M7 (2026-04-25) delivered F1 0.053 → 0.238 (+349%) and validated compound-multiplicative + threshold-gated hypothesis. M7 also surfaced one big remaining gap (cat-2 multi-hop) and one infra gap (Stage 3 OOM). M8 takes both.

---

## 0. Team composition

| Agent | Role | Model | Primary tools | Boundary |
|-------|------|-------|---------------|----------|
| **TL** | Controller | Opus 4.7 (1M ctx) | Plan, Agent, Bash, gh | All coordination + merges |
| **INFRA** | Measurement infrastructure engineer | Sonnet | go-code, Bash, Docker | Container memory tuning, health-check, recovery script hardening |
| **MEASURE** | Eval / LoCoMo harness engineer | Sonnet | go-code, Python, Bash | `evaluation/locomo/**`, scoring, MILESTONES |
| **GRAPH** | Graph retrieval researcher | Opus | go-code (impact_analysis, code_graph, call_trace) | `internal/search/`, D2 multi-hop, AGE recursive CTE |
| **COT** | CoT query decomposition implementer | Opus | go-code, go-search (research MemOS impl) | `internal/search/decomposer.go` (new), prompt design |
| **COMPETE** | Competitor research analyst | Sonnet | **go-search** (research, repo_search, web_url_read), **go-code** (code_compare, semantic_search on cloned repos) | Survey MemOS, Mem0, Graphiti, Letta, LangMem, Zep, Cognee, Memobase. Output: ranked port-targets with actual code references |
| **PRODUCT** | Product canary engineer | Sonnet | Bash, OTel | `factual` A/B canary on `memos` cube |
| **CLEANUP** | Refactor + dead-code engineer | Sonnet | go-code (impact_analysis, dead_code) | X-Service-Secret extraction, GOMEMLIMIT env wiring |
| **REVIEW (×2)** | Spec compliance + code quality reviewers | Sonnet (spec) / Opus (quality) | go-code | Two-stage review per PR — never skip |

> TL is the only one with merge rights to `main`. Subagents stop at `gh pr create`.

---

## 1. Strategic frame

### What we know (post-M7)
- Compound is **multiplicative + threshold-gated**. Quality lift requires evidence density.
- factual prompt = **quality + speed bonus** (+F1, -52% p95 chat).
- Per-message `mode=raw` ingest dramatically lifts retrieval recall (0.000 → 0.769) once corpus is rich enough.
- `window_chars` configurable (production-safe at any size after embed batching).
- pprof now available for perf debugging.
- **Auto-changelog system live** (release-drafter + goreleaser + changelog-sync.yml) — every release self-documents.

### What we don't know
- Does Stage 2's F1=0.238 generalise to the full 10-conv 1986-QA dataset? (Stage 3 OOM blocked answer.)
- Why does cat-2 multi-hop stay flat at F1=0.091 even with all M7 fixes?
- Does any competitor's published technique (CoT decomposition, expired_at temporal model, trustcall PATCH) move our F1 if ported?
- Is `factual` safe as production default for `memos` cube clients?

### What we will prove this sprint
1. Stage 3 retrieval-only on 10 convs reaches hit@k ≥ 0.45 (vs Stage 2's 0.769 conv-26-only) with stable infra → generalisation confirmed
2. Stage 3 stratified chat-50 reaches F1 ≥ 0.18 → MemOS-tier holds across the dataset
3. cat-2 multi-hop F1 reaches ≥ 0.18 (2× current) via D2 instrumentation + targeted fix
4. CoT query decomposition (ported from MemOS pattern) lifts cat-1 + cat-3 by ≥ 30% relative
5. Competitor analysis surfaces ≥ 3 concrete port-targets (with code refs) for M9
6. `factual` A/B on real prod traffic shows acceptance-rate parity with conversational at -50% latency
7. Recovery scripts no longer die silently (heartbeat + trap ERR + per-phase checkpoint)

---

## 2. High-level orchestration

```dot
digraph m8 {
  rankdir=TB
  TL [shape=doublecircle, label="TL reads plan, dispatches"]
  INFRA [label="S1 INFRA\nGOMEMLIMIT + recovery hardening", shape=box]
  COMPETE [label="S2 COMPETE\ngo-search + go-code on competitor repos", shape=box]
  GRAPH [label="S3 GRAPH\nD2 multi-hop instrumentation + fix", shape=box]
  COT [label="S4 COT\nport CoT query decomposition", shape=box]
  CLEANUP [label="S5 CLEANUP\nX-Service-Secret extract + factual canary infra", shape=box]
  MEASURE [label="S6 MEASURE\nStage 3 v2 retrieval + chat-50 + per-conv breakdown", shape=box]
  CAT5 [label="S7 CAT5\ncat-5 adversarial regression diagnosis", shape=box]
  PRODUCT [label="S8 PRODUCT\nfactual canary on memos cube", shape=box]
  FINAL [label="MILESTONES + CHANGELOG sync + memory bank", shape=doublecircle]

  TL -> INFRA
  TL -> COMPETE
  TL -> CLEANUP
  INFRA -> MEASURE [label="GOMEMLIMIT live"]
  COMPETE -> COT [label="MemOS CoT impl ref"]
  COMPETE -> GRAPH [label="MemOS CoT may inform D2"]
  GRAPH -> MEASURE [label="post-fix re-measure"]
  COT -> MEASURE [label="post-port re-measure"]
  CAT5 -> MEASURE
  CLEANUP -> PRODUCT [label="canary needs feature-flag infra"]
  PRODUCT -> MEASURE [label="canary metric in MILESTONES"]
  MEASURE -> FINAL
}
```

**Phase 1 (parallel-safe, dispatched together):** INFRA, COMPETE, CLEANUP. Disjoint file sets, worktree-isolation.

**Phase 2 (after COMPETE delivers refs + INFRA delivers stable infra):** GRAPH, COT, CAT5 in parallel. Worktree-isolation.

**Phase 3 (after GRAPH+COT+CAT5 merged):** MEASURE re-runs Stage 3, scores, writes per-conv breakdown.

**Phase 4 (after CLEANUP delivers infra):** PRODUCT canary on `memos` cube.

**Phase 5:** TL writes MILESTONES, triggers changelog-sync via release publish, banks surprises in memory.

---

## 3. Tooling mandates (apply to EVERY subagent)

### go-code is the default code-analysis tool

Every implementer + reviewer MUST start with go-code MCP, not Grep/Glob/Read. PreToolUse hook auto-injects loading reminder into Agent prompts.

| Task | Tool | Rationale |
|------|------|-----------|
| "Where is symbol X used?" | `mcp__go-code__understand` (include_callers=true) or `impact_analysis` | Symbol-aware vs grep text |
| "Find code that does Y semantically" | `semantic_search` | Hybrid RRF + 1-hop graph expansion |
| "How does this call chain work?" | `call_trace` direction=callees/callers | Type-aware Go interface dispatch |
| "Repo-wide structural questions" | `code_graph` (Cypher) or `dep_graph` | AGE knowledge graph |
| "First contact with unfamiliar repo" | `explore` | Fast, no LLM |
| "Quality assessment" | `code_health` | Static analysis with CVE check |
| "Cross-repo comparison" | `code_compare` | Architecture + quality verdicts |
| "Discover repos for technique X" | `repo_search` | Web + GitHub + READMEs |
| "Read external URL" | `mcp__go-search__web_url_read` | NEVER WebFetch unless go-search down |
| "Research a topic" | `mcp__go-search__research` | Unified + deduped + ranked |

### go-search for competitor / external research

`mcp__go_search__research`, `repo_search`, `web_url_read`, `github_code_search` — these own external information gathering. Never use Perplexity. Never use Grep/Glob to "search for ideas".

---

## 4. Stream 1 — INFRA: GOMEMLIMIT + recovery hardening

### Context
M7 Stage 3 OOM crashed memdb-go during full ingest of 10 convs. Recovery script died silently after Phase 3B v2 (set -euo pipefail killed without trace). Lost hours.

### Goals
- Container survives full LoCoMo 10-conv ingest without OOM
- Recovery scripts emit heartbeat + checkpoint files; failures visible
- `pprof` profile captured during full ingest for memory-allocation hotspots

### Files (disjoint from other streams)
- `~/deploy/krolik-server/docker-compose.yml` (or `compose.override.yml`) — `mem_limit` raise, `GOMEMLIMIT` env
- `memdb-go/cmd/server/main.go` — read `GOMEMLIMIT` if not set, log effective value at startup
- `evaluation/locomo/scripts/recovery_template.sh` — NEW, reusable recovery harness with `trap ERR` + heartbeat + per-phase checkpoint files
- `docs/perf/2026-04-26-stage3-memory-profile.md` — pprof heap snapshot analysis

### Use go-code
- `mcp__go-code__semantic_search` repo=MemDB query="memory allocation hotspots in fast-add or raw-add pipeline" → ranks where memory pressure lives
- `mcp__go-code__code_health` focus=memdb-go to spot leak-prone patterns
- `mcp__go-code__understand nativeRawAddForCube` for the raw-mode ingest body

### DoD
- [ ] Container runs full 10-conv raw ingest without OOM (verified via pprof heap profile snapshot)
- [ ] Recovery harness has heartbeat (writes `/tmp/$NAME.heartbeat` every 60s) + `trap ERR` + per-phase `/tmp/$NAME.phase-N.done` markers
- [ ] PR `chore/infra-gomemlimit-recovery-hardening` opened
- [ ] `feedback_set_e_recovery_scripts.md` learning is referenced in the recovery template's docstring

### Branch: `chore/infra-gomemlimit-recovery-hardening`
### Model: Sonnet
### Effort: 4-6h

---

## 5. Stream 2 — COMPETE: competitor analysis via go-search + go-code

### Context
We need to know what published patterns from MemOS / Mem0 / Graphiti / Letta / LangMem / Zep / Cognee / Memobase can lift MemDB. M7's measurement put us at MemOS-tier on Stage 2; M8 tries to **beat** them.

### Goals
- Survey 8 competing memory frameworks
- For each, identify ≥ 1 concrete pattern (with code refs) that could improve MemDB
- Rank port-targets by **expected F1 lift / engineering cost**
- Output a ranked plan for M9 implementation streams

### Specifically use go-search + go-code (this is the example case)

Phase 2a — discover via `go-search`:
- `mcp__go_search__research` query="MemOS NLI rerank implementation"
- `mcp__go_search__research` query="mem0 v2 entity extraction prompt"
- `mcp__go_search__research` query="Graphiti expired_at temporal invalidation Cypher"
- `mcp__go_search__research` query="Zep Memgpt working memory sliding window"
- `mcp__go_search__research` query="LangMem trustcall PATCH JSON path"
- `mcp__go_search__research` query="Cognee knowledge graph extraction LLM"
- `mcp__go_search__research` query="Memobase user profile structured memory"
- `mcp__go_search__github_code_search` query="memory framework cross encoder rerank"

Phase 2b — clone + analyze via `go-code`:
- `git clone --depth 1` each promising repo into `/tmp/compete-research/<name>/`
- Per repo: `mcp__go-code__explore` for first-contact overview + health score
- For target patterns: `mcp__go-code__code_compare repo_a=/home/krolik/src/MemDB repo_b=/tmp/compete-research/<name>` to surface architectural deltas
- For specific symbols: `mcp__go-code__understand symbol="<name>"` to grasp implementation
- Use `mcp__go-code__semantic_search` to find equivalent functionality across both

### Files
- `docs/competitive/2026-04-26-memory-frameworks-survey.md` — NEW, structured survey:
  - Per framework: 1-paragraph summary, F1 numbers if published, key technique
  - Per technique: link to source code, our equivalent (if any), gap analysis, port effort estimate
  - Ranked port-targets table
- `docs/competitive/2026-04-26-port-target-spec/<name>.md` (1 file per top-3 port targets) — full implementation brief, ready to hand to next M9 implementer

### Hot candidates to investigate (don't limit to these)
1. **MemOS CoT query decomposition** — known gap in our ROADMAP-SEARCH comparison table; likely biggest cat-2 lift
2. **Graphiti `expired_at` temporal invalidation** — listed as Брать → п.1 in ROADMAP-ADD-PIPELINE; we have `valid_at` only
3. **MemOS `search_priority` dict** — listed as MemOS-only in our comparison; may inform multi-hop ordering
4. **LangMem trustcall PATCH** — listed as п.8 in ROADMAP, deferred for years; revisit value now
5. **Mem0 prompt engineering** — they publish their extraction prompt; compare to our unified prompt
6. **Zep Memgpt sliding window summary** — separate from our `mode=raw`; may complement

### DoD
- [ ] Survey doc exists, ≥ 8 frameworks covered, each with code refs
- [ ] Top-3 port-targets have full implementation specs
- [ ] Ranked table with effort/lift estimates
- [ ] PR `docs/competitive/m8-survey` opened
- [ ] At least 2 candidates worth dispatching as M8 streams (informs S4 COT brief)

### Branch: `docs/competitive/m8-survey`
### Model: Sonnet
### Effort: 6-10h
### Dispatched in PARALLEL with S1 + S5 (research-only, no code touch)

---

## 6. Stream 3 — GRAPH: D2 multi-hop diagnosis + targeted fix

### Context
cat-2 multi-hop F1 = 0.091 in Stage 2 — the lowest of all categories. Even with raw ingest + threshold=0.0 it didn't lift like cat-1/3/4. Indicates D2 graph traversal isn't surfacing connected facts.

### Goals
- Instrument D2 multi-hop hop attempts vs hits per question
- Identify root cause: missing edges? Hop limit? Query rewriting before edge match?
- Apply targeted fix
- Re-measure cat-2 in Stage 3 v2

### Use go-code (critical)
- `mcp__go-code__understand` symbol="multihopRecall" or "RecallByHop" or whatever D2 entry symbol is named
- `mcp__go-code__call_trace` direction=callees depth=4 — see what SQL D2 actually issues
- `mcp__go-code__code_graph` repo=MemDB query="D2 multi-hop AGE recursive CTE call chain from search service entry"
- `mcp__go-code__semantic_search` query="multi hop graph traversal hop count limit edge type filtering"
- `mcp__go-code__impact_analysis` on the candidate function before changing — confirm blast radius

### Diagnosis playbook
1. Add `slog.Debug` at each D2 hop step (hop count, source memory id, edge type, target memory ids returned)
2. Run 5 cat-2 questions from `evaluation/locomo/sample_gold.json` against the live system, capture logs
3. Identify pattern: are hops returning 0? returning wrong nodes? hop limit hit? edges not present?
4. Likely root causes (rank by probability):
   - Memory edges only created during D3 reorganizer (if reorg disabled or rare, no edges = no hops)
   - Edge types not matched (D2 may filter by type, source may use different type)
   - `D2_MAX_HOP=3` honored but each hop returns no candidates
   - Cosine threshold for hop targets too strict

### Files
- `memdb-go/internal/search/multihop.go` (or whatever the D2 file is — locate via go-code)
- `memdb-go/internal/search/multihop_test.go` — add tests for fix
- `memdb-go/internal/search/multihop_livepg_test.go` (`//go:build livepg`) — verify against real AGE
- New OTel metric `memdb.search.d2_hops{hop, outcome={hit|miss}}` for ongoing prod observability

### DoD
- [ ] D2 instrumentation lands (slog.Debug + OTel histogram)
- [ ] Root cause identified + documented in `docs/design/2026-04-26-d2-multihop-diagnosis.md`
- [ ] Fix applied OR brief created for next session if fix is non-trivial
- [ ] Stage 2 re-measurement on cat-2 shows F1 ≥ 0.18 (vs current 0.091) — gate
- [ ] PR `feat/d2-multihop-fix` opened

### Branch: `feat/d2-multihop-fix`
### Model: Opus (architectural judgment)
### Effort: 1-2 days
### Sequenced AFTER S2 (COMPETE may surface MemOS's multi-hop pattern worth incorporating)

---

## 7. Stream 4 — COT: port CoT query decomposition

### Context
Our ROADMAP-SEARCH comparison table flags **CoT query decomposition** as a MemOS-only feature we lack. Multi-hop and complex temporal questions benefit massively when LLM splits the question into sub-queries before retrieval.

Example: "What did Caroline do in Boston after she met Emma?" → decomposed to:
1. "When did Caroline meet Emma?"
2. "What did Caroline do in Boston?"
3. Filter (2) results by temporal relation to (1)

### Goals
- Port CoT decomposition as new D11 feature in `internal/search/`
- Gated by env `MEMDB_COT_DECOMPOSE` (default off)
- Cache decomposition (same query → cached sub-queries) with TTL
- Measure F1 lift on cat-2 + cat-3 in Stage 3 v2

### Use go-code + go-search
- `mcp__go_search__github_code_search` query="MemOS query decomposition" — find their implementation
- `mcp__go-code__code_compare repo_a=/home/krolik/src/MemDB repo_b=/tmp/MemOS` focus="query decomposition" — see how they structure it
- `mcp__go-code__understand` on our `IterativeRetrieval` (D5 staged prompts) — closest analog, see if we extend or build new
- `mcp__go-code__semantic_search` query="CoT chain of thought reasoning prompt for multi-hop question" — find papers/refs

### Files (after COMPETE delivers MemOS code refs)
- `memdb-go/internal/search/cot_decomposer.go` (NEW) — LLM call to split query into 1-N sub-queries
- `memdb-go/internal/search/cot_decomposer_test.go`
- `memdb-go/internal/search/cot_decomposer_livepg_test.go`
- `memdb-go/internal/search/service.go` — wire D11 step before D2 multi-hop (so decomposed sub-queries fan out to D2)
- `memdb-go/internal/config/config.go` — env vars `MEMDB_COT_DECOMPOSE`, `_MAX_SUBQUERIES`, `_TIMEOUT_MS`
- New metrics: `memdb.search.cot.decomposed{n}`, `memdb.search.cot.duration_ms`
- `evaluation/locomo/MILESTONES.md` — new section measuring CoT impact

### Design decisions (for the implementer to make + document)
- LLM call cost: 1 extra call per query — acceptable since CE rerank already cuts LLM rerank cost
- Cache key: `hash(query) → []sub-queries` with TTL similar to LLM rerank cache
- Failure mode: any decomposition error → fall back to original query (best-effort like CE rerank)
- Decompose only if query passes a heuristic (e.g., length > 8 words, contains "and", "after", "before", multiple entities)

### DoD
- [ ] CoT decomposer implemented + gated
- [ ] Unit + livepg tests green
- [ ] Stage 2 re-measurement: cat-2 F1 ≥ 0.15, cat-3 F1 ≥ 0.25 (compound effect with D2 fix)
- [ ] Design doc `docs/design/2026-04-26-cot-decomposition.md`
- [ ] PR `feat/cot-query-decomposition` opened

### Branch: `feat/cot-query-decomposition`
### Model: Opus (prompt design + cross-cutting wiring)
### Effort: 1-2 days
### Sequenced AFTER S2 (COMPETE delivers MemOS reference implementation)

---

## 8. Stream 5 — CLEANUP: X-Service-Secret extract + factual canary infra

### Context
- M7 review #72 surfaced 3rd-copy header check (`X-Service-Secret`) — hits project's "3rd dup → extract" rule
- F4 backlog #2: factual A/B canary needs `MEMDB_DEFAULT_ANSWER_STYLE` env + per-cube override

### Goals
- Extract `HeaderServiceSecret` const + `mw.CheckServiceSecret(r, expected) (bool, string)` helper
- Update 3 callers: `middleware/auth.go`, `middleware/ratelimit.go`, `server_routes.go pprofHandler`
- Add `MEMDB_DEFAULT_ANSWER_STYLE` env (default `conversational`) + per-cube override mechanism
- Add canary metric `memdb.chat.answer_acceptance_total{style, outcome={accepted|rejected}}` (operator decides accepted/rejected via a new feedback signal — separate concern, just emit the counter)

### Use go-code
- `mcp__go-code__impact_analysis` on `checkServiceSecret` (current lower-case package-private) — confirm only 3 callers
- `mcp__go-code__understand` on each caller before refactor

### Files
- `memdb-go/internal/server/middleware/auth.go` — promote `checkServiceSecret` → `CheckServiceSecret`, add header constants
- `memdb-go/internal/server/middleware/ratelimit.go` — call new helper
- `memdb-go/internal/server/server_routes.go` (pprofHandler) — call new helper
- `memdb-go/internal/handlers/types.go` — add per-cube `AnswerStyle` storage option (TBD with PRODUCT)
- `memdb-go/internal/config/config.go` — `MEMDB_DEFAULT_ANSWER_STYLE`
- Tests for refactor + new env var

### DoD
- [ ] Header constants in middleware package, used by all 3 callers
- [ ] No behavior change (regression tests confirm)
- [ ] `MEMDB_DEFAULT_ANSWER_STYLE` env wired
- [ ] PR `chore/auth-helpers-extract-and-default-answer-style` opened

### Branch: `chore/auth-helpers-extract-and-default-answer-style`
### Model: Sonnet
### Effort: 4-6h
### PARALLEL with S1 + S2 (disjoint files, worktree-isolation)

---

## 9. Stream 6 — MEASURE: Stage 3 v2 + per-conv breakdown

### Context
M7 Stage 3 OOM blocked the answer to "does F1=0.238 generalise". With INFRA hardened + S3/S4 landed, re-run cleanly.

### Goals
- Phase 3A v2 — clean ingest of all 10 convs (with GOMEMLIMIT), no errors
- Phase 3B v2 — full retrieval-only run on 1986 QAs (~1h)
- Phase 3C v2 — stratified chat-50 (1 per conv × cat) for actual F1 measurement
- Per-conv hit@k breakdown — identify outlier conversations
- Comparative table: M7 Stage 2 (conv-26 only) vs M8 Stage 3 v2 (all 10)

### Use go-code (for harness understanding)
- `mcp__go-code__understand` on `query.py` if it has any non-obvious behavior
- `mcp__go-code__semantic_search` on Python harness if needed

### Files
- `evaluation/locomo/results/m8-stage3-*.json` — committed results (use `git add -f`)
- `evaluation/locomo/MILESTONES.md` — new M8 Stage 3 section with all numbers
- `docs/eval/2026-04-26-m8-stage3-per-conv-breakdown.md` — per-conv variance analysis

### DoD
- [ ] All 1986 QAs scored, no ingest errors
- [ ] Per-conv hit@k table in MILESTONES
- [ ] Phase 3C F1 ≥ 0.18 → Stage 2 generalises
- [ ] Phase 3C F1 < 0.18 → flag for diagnosis (likely cat-2 still under-firing)
- [ ] PR `chore/m8-stage3-results` opened

### Branch: `chore/m8-stage3-results`
### Model: Sonnet
### Effort: 4-8h (depends on harness throughput)
### Sequenced AFTER S1 (INFRA) + S3 (GRAPH) + S4 (COT) merged

---

## 10. Stream 7 — CAT5: cat-5 adversarial regression diagnosis

### Context
cat-5 F1 dropped 0.133 → 0.092 between Stage 1 sample and Stage 2 full. Hypothesis: with more raw memories available, factual prompt finds *something* relevant for adversarial questions instead of correctly saying "no info".

### Goals
- Diagnose cat-5 false positives in Stage 2 predictions
- Decide: is this prompt-tunable or requires architectural change (e.g., explicit "no support" classifier)?
- Apply minimal fix or document for future session

### Use go-code
- N/A primarily — this is data analysis on `evaluation/locomo/results/m7-stage2.json`

### Files
- `docs/eval/2026-04-26-m8-cat5-adversarial-analysis.md` — failure mode review with examples
- Maybe: `memdb-go/internal/handlers/chat_prompt_tpl.go` — sharpen rule 6 ("no answer" decision)

### DoD
- [ ] Failure mode characterised (e.g., "60% of cat-5 false positives have hit@k > 0 — retrieval surfaces noise")
- [ ] Recommendation: prompt fix vs classifier vs different threshold for cat-5 questions
- [ ] PR `docs/m8-cat5-adversarial-analysis` (analysis-only) OR `fix/cat5-prompt-tightening` if minimal fix possible

### Branch: `docs/m8-cat5-adversarial-analysis` or `fix/cat5-prompt-tightening`
### Model: Sonnet
### Effort: 4-6h

---

## 11. Stream 8 — PRODUCT: factual canary on memos cube

### Context
Stream F (M7) found `factual` is 2.1× faster + higher F1. Backlog #2: A/B test as default for QA workloads. PRODUCT engineer designs the canary.

### Goals
- 10% canary on `memos` cube (vaelor's main consumer): half of `/product/chat/complete` requests get `answer_style=factual` server-side
- Track acceptance signal: emit `memdb.chat.answer_acceptance_total{style, outcome}` counter (operator marks via downstream user-feedback signal)
- Run for 24h, compare per-style: latency p50/p95, error rate, downstream user reaction (if signal available)
- Decide: promote to default, keep canary, or roll back

### Use go-code
- `mcp__go-code__call_trace` on `NativeChatComplete` — find the right place to inject A/B switch
- `mcp__go-code__semantic_search` query="canary feature flag random A/B split in HTTP handler"

### Files
- `memdb-go/internal/handlers/chat_canary.go` (NEW) — feature-flag logic, hash-based 10% split (sticky per user_id)
- `memdb-go/internal/config/config.go` — env `MEMDB_FACTUAL_CANARY_PCT` (0-100)
- Tests for canary logic
- `docs/perf/2026-04-26-factual-canary-design.md` — methodology + metrics + rollback plan
- `docs/perf/2026-04-27-factual-canary-results.md` — written after 24h observation

### DoD
- [ ] Canary live on `memos` cube, 10%
- [ ] Metric flowing in Prometheus
- [ ] 24h observation report → recommendation
- [ ] PR `feat/factual-canary` opened

### Branch: `feat/factual-canary`
### Model: Sonnet
### Effort: 4-6h impl + 24h observation + 2h analysis

---

## 12. Stream 9 — FINAL: MILESTONES + memory bank + release publish

### Context
At end of M8: consolidate findings, publish v2.2.0 release (which auto-triggers changelog-sync.yml from M7 F10), bank surprises in memory.

### TL self-tasks (no subagent needed)
1. Open PRs in TaskList order, two-stage review each, merge
2. Write final MILESTONES section for M8 with the actual numbers
3. Write `project_m8_multi_hop_lift.md` in memory
4. Update `feedback_*.md` files for any new learnings
5. Click "Publish release v2.2.0" on GitHub draft → goreleaser builds binaries → changelog-sync.yml opens auto-PR with the new CHANGELOG entry → controller merges
6. Concise Russian end-of-day summary to user

### DoD
- [ ] All 8 streams either merged or explicitly deferred with reason
- [ ] MILESTONES updated
- [ ] Memory bank entries created
- [ ] v2.2.0 release published, auto-changelog PR merged
- [ ] User summary delivered

---

## 13. Risk register

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|-----------|
| Stage 3 still OOMs after GOMEMLIMIT | Medium | High | INFRA produces pprof heap profile diagnosing the actual leak; have fallback "ingest 5 convs at a time" plan |
| D2 root cause is "no edges exist" → requires D3 reorganizer always-on | Medium | Medium | GRAPH documents the dependency; M9 plan addresses |
| CoT decomposition costs extra LLM call → latency regression | Medium | Medium | Gate behind env flag; PRODUCT canary measures; default off |
| COMPETE finds nothing actionable | Low | Low | Even null-result is valuable evidence; doc it |
| factual canary shows lower acceptance than conversational | Medium | Medium | Don't promote to default; document the trade-off; feature stays per-route |
| Cat-5 fix requires classifier (significant scope) | Medium | Low | Document for M9; M8 stays focused on multi-hop + generalisation |
| Background processes from subagents die again | Low | Medium | INFRA recovery template + controller-runs-long-jobs rule |
| Two parallel implementer subagents on overlapping files | Low | High | Plan disjoint sets; worktree-isolation; controller monitors |

---

## 14. Success criteria (all must be green)

- [ ] Stage 3 v2 retrieval hit@k ≥ 0.45 across all 10 convs
- [ ] Stage 3 v2 stratified chat F1 ≥ 0.18
- [ ] cat-2 multi-hop F1 ≥ 0.18 (≥2× current)
- [ ] cat-3 temporal F1 ≥ 0.25 (vs Stage 2 0.201) via CoT decomposition
- [ ] Competitor survey delivers ≥ 3 ranked port-targets with code refs
- [ ] factual canary live with 24h data
- [ ] X-Service-Secret refactor merged, no regression
- [ ] Recovery scripts heartbeat + checkpoint + trap ERR
- [ ] No regressions on `go test ./...`, vaelor smoke, oxpulse-chat
- [ ] MILESTONES updated, all design docs committed
- [ ] v2.2.0 released, auto-changelog flowed end-to-end (validates M7 F10)

---

## 15. What we do NOT do in M8

- ❌ Image / multimodal memory (still requires CLIP + GPU; deferred to M10+)
- ❌ MemCube cross-sharing (separate concern, no impact on F1)
- ❌ Full Python deprecation (Phase 5 timeline, separate)
- ❌ trustcall PATCH from LangMem (low expected lift; revisit in M9 if COMPETE flags it as high-value)
- ❌ Embedder swap (Voyage / BGE-M3) — no signal it's bottleneck
- ❌ Re-litigating M7 design choices

---

## 16. TL self-checks (lessons from M7)

1. Before declaring "done": `gh pr diff <N>` + `gh pr checks <N>` MYSELF, never trust subagent report alone
2. Long-running scripts go in MAIN session, not subagent worktree (they die when worktree reaped)
3. PR titles MUST start lowercase after colon (commit-lint regex `^(?![A-Z]).+$`)
4. Use `gh api -X PATCH` to fix titles, not `gh pr edit` (silent fail on GraphQL warning)
5. Spec reviewer's "scope creep" complaints often stem from stale-base diff — verify with fresh `git diff origin/main..HEAD`
6. Local branch deletion failures after merge are harmless (worktree references)
7. F1 "flat" on small sample doesn't mean compound failed — measure at TARGET scale
8. Check container resources BEFORE running 10× scale benchmarks
9. `set -euo pipefail` recovery scripts MUST have trap ERR + heartbeat
10. Skill `compound-sprint-orchestration` exists — invoke it for multi-stream sprints

---

## Final note for TL

M7 proved we can ship fast (12 PRs in a day, +349% F1). M8 proves we can do it again at higher altitude — with the multi-hop bottleneck closed, our number stops being "MemOS-tier" and starts being "best-in-class". The competitive analysis (S2) is the strategic anchor: once we know exactly what each rival has that we don't, every M-sprint becomes targeted instead of speculative.

Stay disciplined on disjoint streams, two-stage review, verification before merge. The skill is tested. Trust the process.
