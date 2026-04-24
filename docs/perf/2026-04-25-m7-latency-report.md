# M7 Perf/Latency Report — 2026-04-25

## Context

Profiling memdb-go binary at commit 841febc2 (Streams A+B+C). Container `memdb-go` started
2026-04-24T14:39:31Z. Tests run 2026-04-24 ~15:30–17:00 UTC. All synthetic cubes
hard-deleted after each test group.

Stream summary:
- A: `answer_style=factual` server-side prompt routing + `memdb_chat_prompt_template_used_total` metric
- B: LoCoMo harness switches to `mode=raw` — no memdb-go perf impact
- C: `window_chars *int` per-request override in `fullAddRequest`; `windowSizeFor(req)` replaces
  bare `windowChars` constant in `extractFastMemories`. Valid range: [128, 16384], default 4096.

## Results

### /product/add latency

Synthetic payload: 30 messages, ~57 chars/message (~1710 chars total). With default
window=4096 all 30 messages fit in **1 window** → 1 memory. With window=512 the same
messages produce **24 windows** → 24 embed+dedup+insert cycles per request.

| Config | n | p50 | p95 | p99 | memories/req |
|--------|---|-----|-----|-----|---|
| window=4096 (default) | 100 | 0.778s | 1.213s | 1.438s | 1 |
| window=512 | 100 | 13.584s | 20.030s | 27.923s | 24 |
| Δ p95 | — | — | **+1551%** (threshold: +20%) | — | — |

**Verdict: FAIL**

The p95 delta far exceeds the 20% threshold. Root cause: 24× more embed+dedup+insert
operations per request (one per window). Each embed call to the local ONNX server takes
~550ms; 24 sequential calls = ~13s. This is expected behaviour — `window_chars=512` is a
precision knob, not a latency-neutral one. The default (4096) stays well within the 1.2s p95
budget for short conversations.

Note: the 8× estimate in the task spec was based on assuming 8× more windows; the actual
ratio for a 1710-char conversation is 24× (512-char windows with 800-char overlap).

### /product/chat/complete latency

Pre-populated cube: 1 LongTermMemory node (6 turns about Paris). 50 sequential requests
per variant, same query ("Tell me about Paris and its history in detail").

| Config | n | p50 | p95 | mean |
|--------|---|-----|-----|------|
| answer_style=conversational (default) | 50 | 10.151s | 14.747s | 10.199s |
| answer_style=factual | 50 | 4.307s | 7.011s | 4.475s |
| Δ p95 | — | — | **−52.5%** (factual is faster) | — |

**Verdict: PASS — with a significant positive surprise**

Factual is 2.1× faster at p95, not "marginally faster" as anticipated. The factual QA prompt
is ~700 bytes vs ~3500 bytes for conversational, reducing LLM input tokens by ~80%. Since
LLM call (via CLIProxyAPI → Gemini Flash) dominates chat latency, shorter prompt = faster
first-token = faster total. This is a material latency win for LoCoMo-style benchmarks that
use `answer_style=factual` and could be considered for production QA use-cases where
concise factual answers are acceptable.

### pprof CPU profile

`/debug/pprof/profile` returns **404** (auth passes, route not registered). pprof is not
mounted in `server_routes.go` — there is no `_ "net/http/pprof"` import or explicit
`mux.HandleFunc("/debug/pprof/..."` registration. CPU profiling is unavailable in this
binary without a rebuild.

pprof section **skipped** — not a blocker.

### Prometheus metrics

Stream A metric confirmed live at container start and incrementing correctly during tests:

```
memdb_chat_prompt_template_used_total{template="conversational"} 51
memdb_chat_prompt_template_used_total{template="factual"}        160
```

Labels use OTel-generated `otel_scope_name="memdb-go/chat"` namespace. Both templates
are being counted. The `factual` counter was already at 109 before our benchmark (pre-existing
chat activity since container start at 14:39 UTC).

Add pipeline counters at end of session (includes all test activity + pre-existing):

```
memdb_add_requests_total{mode="fast", outcome="success"}  400+
memdb_add_memories_total{mode="fast"}                    2123+
```

Internal `memdb_add_duration_ms_milliseconds` histogram (mode=fast) shows bimodal
distribution: bucket boundary at le=25ms has 229 entries (fast, 1-window requests) vs
le=+Inf=399+ total (includes the slow 24-window w512 requests well above 10000ms).

## Recommendations

1. **window_chars=512 is a precision knob, not safe for latency-sensitive paths.** Keep
   the default at 4096 in production. Document that window_chars < 512 produces linear
   latency growth with window count. Consider adding a warning log when `windowChars < 1024`.

2. **answer_style=factual is a free 2× speedup for QA workloads.** The −52% p95 improvement
   is large enough to recommend `answer_style=factual` as default for LoCoMo evals and any
   production use-case where short-phrase answers are acceptable (search snippets, fact
   lookup). A follow-up A/B test on real traffic is warranted.

3. **pprof is not registered** — add `_ "net/http/pprof"` + a `/debug/pprof/` route group
   behind the internal-auth middleware for future profiling. This is a one-line change in
   `server_routes.go`.

4. **embed throughput is the bottleneck for small window_chars.** `go tool pprof` was
   unavailable, but log timing (24 × ~550ms ≈ 13s per request) points to sequential embed
   calls. Batching embed calls per request (instead of one-at-a-time) would cut window=512
   latency by ~10× without changing semantics. Filed as a perf opportunity.

5. **Staged rollout drill described in plan §7 (MEMDB_ANSWER_STYLE_DEFAULT flag + 10%
   canary)** deferred to a separate session — not blocking for the M7 measurement goals.
