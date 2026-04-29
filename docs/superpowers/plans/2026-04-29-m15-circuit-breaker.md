# M15 — CircuitBreaker для bge-reranker (go-kit/rerank G1)

**Date:** 2026-04-29
**Owner:** controller (subagent-driven)
**Status:** ready to dispatch

## Goal

Wire `go-kit/rerank.CircuitBreaker` (G1, available since v0.27.0) onto the bge-reranker (cross-encoder) HTTP client used by MemDB. Default OFF, env-gated. When enabled: after N consecutive failures embed-server is isolated → instant fail-fast → degraded-but-known state → no cascading timeouts under load.

## Background

`internal/server/server_init_search.go` constructs the rerank.Client used by `internal/search/rerank/CrossEncoder`. The client today has retry (since G1 default `defaultRetryPolicy()` shipped) but no circuit breaker. Result: when embed-server is OOM / restarting / network-blipped, search threads pile up retrying with exp backoff for the full timeout window — search p99 latency tail explodes. CircuitBreaker is the standard remedy: detect failure rate, isolate the dependency, fail-fast.

go-kit ships:
- `WithCircuit(CircuitConfig) Opt` — per-client option
- `CircuitConfig{FailThreshold, OpenDuration, HalfOpenProbes, FailRateWindow}` — knobs
- 3 Prometheus metrics already wired:
  - `rerank_circuit_state{model, state}` gauge (closed/open/half-open)
  - `rerank_circuit_transition_total{model, from, to}` counter
  - `rerank_giveup_total{model, reason="circuit_open"}` counter

Plus `ErrCircuitOpen` sentinel — caller can identify circuit-open errors deterministically and Cascade can move on to fallback stage gracefully.

## What

Add `WithCircuit(...)` option to the rerank.Client construction site under env-gate `MEMDB_RERANK_CIRCUIT=1` (default OFF). Tunable knobs via env. Default OFF for safe rollout — flip in prod after one observation window confirms metrics behave.

## Where (one file edit + tests)

* `internal/server/server_init_search.go` — rerank.Client construction site. Add the `WithCircuit` option behind env-gate.
* `internal/server/server_init_search_test.go` (NEW or extend existing) — verify env-gate behaviour: unset → no Circuit option; "1" → option present with parsed config.

## Env

| Variable | Default | Description |
|---|---|---|
| `MEMDB_RERANK_CIRCUIT` | `0` | Master gate. `1` to enable. |
| `MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD` | `5` | Consecutive fails to open circuit. |
| `MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S` | `30` | Seconds to keep circuit open before half-open probe. |
| `MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES` | `1` | Probe requests in half-open before re-close. |
| `MEMDB_RERANK_CIRCUIT_FAIL_WINDOW_S` | `60` | Sliding window for fail-rate count. |

Defaults are sane for production: 5 fails over 60s → 30s isolation → 1 probe → close-or-reopen. Tunable without rebuild.

## Wiring sketch

```go
// internal/server/server_init_search.go (rerank client construction)

opts := []rerank.Opt{
    rerank.WithModel(modelName),
    rerank.WithServerNormalize(rerank.ServerNormalizeSigmoid),
    // ... existing options
}

if circuitEnabled() {
    cfg := rerank.CircuitConfig{
        FailThreshold:   envIntCB("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", 5),
        OpenDuration:    time.Duration(envIntCB("MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S", 30)) * time.Second,
        HalfOpenProbes:  envIntCB("MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES", 1),
        FailRateWindow:  time.Duration(envIntCB("MEMDB_RERANK_CIRCUIT_FAIL_WINDOW_S", 60)) * time.Second,
    }
    opts = append(opts, rerank.WithCircuit(cfg))
}

client := rerank.NewClient(url, opts...)
```

```go
// helper
func circuitEnabled() bool {
    return os.Getenv("MEMDB_RERANK_CIRCUIT") == "1"
}

func envIntCB(key string, def int) int {
    s := os.Getenv(key)
    if s == "" { return def }
    v, err := strconv.Atoi(s)
    if err != nil || v <= 0 { return def }
    return v
}
```

## Tests

1. `TestRerankCircuit_DisabledByEnv_NoOpt` — env unset → no `WithCircuit` invoked, client constructed with same opts as before.
2. `TestRerankCircuit_EnabledByEnv_OptPresent` — env=`1` → `WithCircuit` invoked with parsed config (verify by mocking the option chain or by inspecting `client.cfg.circuit` if exposed; alternative: integration test that observes `rerank_circuit_state` metric == `closed` after first call).
3. `TestRerankCircuit_BadEnv_DefaultsApplied` — env=`1` but `_FAIL_THRESHOLD=abc` (unparseable) → defaults used, no panic.

If `client.cfg.circuit` is private/not testable directly, the smoke test can be: env=`1`, hit a fake CE backend that returns 503 5 times, verify the 6th call returns `ErrCircuitOpen` immediately (faster than retry backoff).

## Acceptance

- `GOWORK=off go build ./...` clean.
- `GOWORK=off go test ./internal/server/... -count=1` passes.
- Disabled-by-default: env unset → bytewise identical client behaviour.
- Metrics already exist in go-kit/rerank → no new metric init needed.

## Risk

Low. Fallback is the existing CrossEncoder skip-path (no rerank → passthrough cosine). Even if circuit opens incorrectly, search degrades to current behaviour, doesn't break.

The one thing to verify: `ErrCircuitOpen` must be classified as "skip" by `CrossEncoder.Rerank`, not as a hard error that aborts the whole pipeline. Check `internal/search/rerank/ce.go:runLive` error handling. If it currently treats any non-nil error as a skip-with-warn, we're fine. If it returns the error up the chain, we need a small adapter to swallow `ErrCircuitOpen` specifically.

## Out of scope

* `WithFallback(*Client)` — secondary backend, separate task. We don't have a secondary embed-server endpoint yet.
* Circuit on LLM Judge — `internal/search/rerank/llm.go` uses different client (CLIProxyAPI), not rerank.Client. Circuit on it is a separate sprint.

## Rollback

`MEMDB_RERANK_CIRCUIT=0` (or unset) → reverts to current behaviour. No data state, no migrations, no lingering effects.

## Observation plan (post-merge)

1. Merge default OFF.
2. Flip `MEMDB_RERANK_CIRCUIT=1` in `~/deploy/krolik-server/.env` after baseline finishes.
3. Watch `rerank_circuit_state` gauge — should stay `closed` under normal load.
4. Synthetic test: kill embed-server for 60s. Verify:
   - `rerank_circuit_transition_total{from="closed",to="open"}` += 1
   - Search latency degrades gracefully (no minute-long retry storms)
   - After embed-server returns + 30s, `rerank_circuit_state{state="closed"}` += 1
5. If observation passes, leave on permanently.
