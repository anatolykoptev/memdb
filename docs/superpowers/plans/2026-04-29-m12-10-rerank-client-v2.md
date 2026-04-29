# M12.10 — `go-kit/rerank.Client` v2.0 (Production-grade Rerank Library)

> **Mission**: extend `go-kit/rerank.Client` from minimal Cohere-shape POSTer into a production rerank library: functional options, typed Result, hooks, retry/circuit/fallback, sigmoid+threshold+sourceboost+token-truncation+drift-histogram, optional cascade, RRF multi-query. Drives MemDB headline LLM Judge from M11 floor (~50%) to M12+ target (~55-60%) AND lifts operational floor (50% fewer timeout failures, drift-detect via metric).
>
> **Driver**: M11 LoCoMo regression analysis (`docs/superpowers/plans/2026-04-29-m12-recovery-analysis.md`) showed CE rerank is the competitive moat (99.5% of search calls go through it). Current client is minimal — gaps in resiliency, quality knobs, observability cost both quality (1-3pp left on table) and operability (timeout cascades, no drift signal). Embed-server already serves `bge-reranker-v2-m3`; client-side investment unlocks per-call tuning without touching the server.
>
> **Mode**: subagent-driven development. Sequential per stream (controller serializes; parallel-safe only between G3 and G4 disjoint files). Each stream = (implementer subagent) + (spec compliance reviewer) + (code quality reviewer). Controller merges only.
>
> **Repo split**: client lives in `~/src/go-kit/rerank/` (separate repo `anatolykoptev/go-kit`). MemDB vendor bump after each merged stream.

---

## Plan revision history

**Rev 5 (2026-04-29, post-M12.10 closure)** — adds **G5 MathReranker** as bonus stream:
- After M12.10 G0-G4 closure (v0.27.0 tagged 2026-04-29), discussion identified math-based prefilter as the missing compute-saver in cascade pattern
- Pattern parallels go-search `research` smart_dedup: caller-side embed vectors + cosine math, no NN inference
- **Library-only** — caller decides if/when to wire (memdb-go integration is M13 concern, not this plan)
- ~460 LOC across `math_reranker.go`, `cosine.go`, `mmr.go` + tests
- v0.28.0 auto-tag via release-please pipeline (now active)

**Rev 4 (2026-04-29, post-G1 merge)** — G2 **split** into server-side and client-side after senior recon of embed-server:
- Verified `/home/krolik/src/embed-server/src/model_reranker.rs:10,249-255` — server returns RAW logits, no normalization (deliberate Cohere compat)
- Verified `api_rerank.rs` — server already implements `top_n` + sort + tokenizer auto-truncate (BERT pair semantics, keeps query, truncates doc tail)
- **G2-server (NEW stream)** — Rust PR в embed-server: `RerankRequest.normalize: Option<NormalizeMode>` (None|Sigmoid), default None preserves Cohere compat. Sigmoid applied server-side once for all callers. Effort: 1 day
- **G2-client (reduced scope)** — drops client-side Sigmoid; adds `WithServerNormalize(NormalizeSigmoid)` Opt that sets `"normalize"` JSON field. Keeps MinMax/ZScore (rare client calibration cases), instruction prefix, token-truncate informational hook, SourceWeights, Threshold, TopN, DryRun. Effort: 1.5 day
- Sequential: G2-server merges → embed-server bump → G2-client uses validated server contract

**Rev 3 (2026-04-29, post-G0 merge)** — scope tightened to **go-kit library only** after user feedback:
- **Dropped G3-pre** (embed-server `bge-reranker-base` deploy) — out of scope; not go-kit work
- **Dropped MemDB vendor-bump sections** from per-stream subagent tasks — controller handles vendor bumps separately, not subagent's concern
- **Dropped MemDB LoCoMo smoke harness** from subagent verification — moved to controller's discretion (subagent only runs go-kit unit tests + race + bench)
- **G3 cascade re-scoped**: pure library impl (`cascade.go` implements `Reranker` interface). Tests via mock HTTP servers. Caller decides when/if to wire — go-kit ships the type, integration is not this plan's concern
- Verified prod embed-server runs only `gte-multi-rerank` (not bge); G2 instruction prefix stays default-empty (gte/bge-m3 don't need it; option remains for E5/bge-v1.5 callers)

**Rev 2 (2026-04-29)** — full rewrite after senior architecture review of Rev 1:

| Rev 1 issue | Rev 2 fix |
|-------------|-----------|
| Flat `Config{}` extended each stream → would become 20+ field God Object by G4 | **Functional options pattern from G0**. `Config{}` kept as deprecated wrapper for v1 compat |
| No `Result` typed return → callers can't distinguish "reranker down" vs "model returned zeros" | **`RerankWithResult(...) (*Result, error)` parallel API in G0**. `Result.Status: Ok\|Degraded\|Fallback\|Skipped` |
| MMR built-in to rerank package (G4b) — but rerank score ≠ doc↔doc similarity | **Dropped from go-kit**. MMR stays in MemDB `internal/search/rerank/mmr.go` (correct layer; needs embed-vectors, not rerank scores) |
| LRU cache as hard dep on `hashicorp/golang-lru` — breaks go-kit "zero bloat" promise | **Cache as interface only** (`Cache` interface, caller supplies impl). go-kit zero-dep preserved |
| Multi-model fallback (Q12) deferred to M13 — but embed-server is single point of failure | **Promoted to G1**. `WithFallback(*Client)` lands with retry/circuit |
| `MaxCharsPerDoc` byte-truncation — broken for ru/en mix (bge-m3 is 512 *tokens*, not chars) | **`MaxTokensPerDoc` added in G2**. Approximate tokenizer (rune count / lang divisor). MaxCharsPerDoc kept as deprecated alias |
| No SourceWeights — RAG-AI canon (`tool_docs: 1.20, nvd: 0.90`) leaves 1pp on heterogeneous corpora | **Added in G2**. `WithSourceWeights(map[string]float32)` |
| G3 cascade with single embed-server model = 2 sequential RTTs, zero compute saved | **Hard prereq**: `bge-reranker-base` deployed in embed-server (tracked as G3-pre, separate ~30min PR). G3 blocks until G3-pre lands |
| No per-stream ablation criteria → "did this feature actually work?" answer is hand-wave | **Per-stream ablation harness**: 50-QA conv-26 with feature on vs off, headline delta + p99 latency + drift histogram |
| Hooks bundled into G1 — but G2/G3/G4 all need them | **Hooks foundation in G0** (along with options + Result type) |

**Rev 2 net**: 5 streams G0-G4 + 1 prereq G3-pre. Days unchanged (9), risk reduced.

---

## Current state (baseline 2026-04-28)

**File**: `~/src/go-kit/rerank/{client.go (184 LOC), http.go (82), metrics.go (36), client_test.go (251), example_test.go}`

**v1 API contract (must hold across v2)**:
```go
type Config struct {
    URL, Model, APIKey string
    Timeout time.Duration
    MaxDocs, MaxCharsPerDoc int
}
type Doc struct{ ID, Text string }
type Scored struct {
    Doc
    Score    float32
    OrigRank int
}
func New(cfg Config, logger *slog.Logger) *Client
func (c *Client) Rerank(ctx context.Context, query string, docs []Doc) []Scored
func (c *Client) Available() bool
```

**v1 callers in MemDB** (verified 2026-04-29 via grep):
- `memdb-go/internal/search/rerank/ce.go` — main CE strategy
- `memdb-go/internal/search/{service,cross_encoder_precompute,cross_encoder_step_test}.go`
- `memdb-go/internal/scheduler/{tree_ce_precompute,reorganizer}.go`
- `memdb-go/internal/server/{server_init,server_init_search}.go`

All v1 callers MUST keep working byte-for-byte after every stream merge.

**What works now**: Cohere-shape POST, tail preservation, stable sort via permutation, recordStatus/recordDuration metrics, rune-aware truncation.

---

## Architecture v2.0 — interfaces + functional options

**Package layout** (additive — no v1 file deleted):

```
~/src/go-kit/rerank/
├── client.go             # v1 client (kept), v2 New() signature added
├── http.go               # cohere request/response (extended for instruction prefix)
├── metrics.go            # extended — score histogram, retry/circuit/cache counters
├── config.go             # NEW (G0) — Opt functional options + cfgInternal
├── result.go             # NEW (G0) — Result type + Status enum
├── hooks.go              # NEW (G0) — Observer interface
├── retry.go              # NEW (G1) — RetryPolicy + exp backoff
├── circuit.go            # NEW (G1) — CircuitBreaker FSM
├── fallback.go           # NEW (G1) — chained client (primary → secondary)
├── normalize.go          # NEW (G2) — sigmoid/minmax/zscore
├── instruction.go        # NEW (G2) — query/doc prefix
├── tokens.go             # NEW (G2) — token-aware truncation
├── source_weights.go     # NEW (G2) — per-source score boost
├── cascade.go            # NEW (G3) — multi-stage chain (separate type, not Client extension)
├── multiquery.go         # NEW (G4) — RRF/max/avg combine
├── cache.go              # NEW (G4) — Cache interface only (no impl)
└── *_test.go             # tests per file
```

**Core interfaces** (G0):

```go
// Reranker — abstraction over pointwise/listwise/cascade/multiquery impls.
// Client (pointwise) is the default. Cascade/MultiQuery wrap any Reranker.
type Reranker interface {
    Rerank(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error)
    Available() bool
}

// Result — typed return so callers distinguish failure modes.
type Result struct {
    Scored []Scored
    Status Status   // Ok | Degraded | Fallback | Skipped
    Model  string   // which model produced this (informative)
    Err    error    // non-nil iff Status == Degraded
}

type Status uint8
const (
    StatusOk       Status = iota  // request succeeded, scores valid
    StatusDegraded                // request failed, returning input order with Score=0
    StatusFallback                // primary failed, secondary succeeded
    StatusSkipped                 // client unavailable (URL empty, len(docs)==0)
)

// Observer — hooks fire at well-defined points. nil-safe, panic-safe in client wiring.
type Observer interface {
    OnBeforeCall(ctx context.Context, query string, n int)
    OnAfterCall(ctx context.Context, status Status, dur time.Duration, n int)
    OnRetry(ctx context.Context, attempt int, err error)
    OnCircuitTransition(ctx context.Context, from, to CircuitState)
    OnCacheHit(ctx context.Context, n int)   // n = how many docs hit cache
    OnTruncate(ctx context.Context, docID string, beforeTok, afterTok int)
}

// Cache — interface only. go-kit/rerank ships NO impl. Callers wire LRU/Redis/memcache.
type Cache interface {
    Get(ctx context.Context, key string) (score float32, ok bool)
    Set(ctx context.Context, key string, score float32)
}

// Functional options — extensibility without breaking changes.
type Opt func(*cfgInternal)

func WithModel(string) Opt
func WithAPIKey(string) Opt
func WithTimeout(time.Duration) Opt
func WithMaxDocs(int) Opt
func WithMaxTokensPerDoc(int) Opt              // G2 — replaces MaxCharsPerDoc
func WithRetry(p RetryPolicy) Opt              // G1
func WithCircuit(c CircuitConfig) Opt          // G1
func WithFallback(*Client) Opt                 // G1 — multi-model fallback
func WithObserver(Observer) Opt                // G0
func WithHTTPClient(*http.Client) Opt          // G0 — injectable transport
func WithNormalize(NormalizeMode) Opt          // G2
func WithInstruction(query, doc string) Opt    // G2 — bge-v1.5 (NOT bge-m3)
func WithSourceWeights(map[string]float32) Opt // G2
func WithCache(Cache) Opt                      // G4

// New constructor (v2 entry point).
func New(url string, opts ...Opt) *Client

// Per-call options (passed to Rerank).
type RerankOpt func(*rerankCallCfg)

func WithTopN(int) RerankOpt
func WithThreshold(float32) RerankOpt          // applied AFTER normalize
func WithDryRun() RerankOpt                    // skip HTTP, return passthrough — testing
```

**v1 compatibility**: `New(cfg Config, logger *slog.Logger) *Client` kept as wrapper that translates to options. `Rerank(ctx, q, docs) []Scored` kept — calls `RerankWithResult` and discards Result.Status. v1 callers untouched. New tests assert v1 byte-identical behavior.

---

## Stream G0 — Foundation: Options + Result + Hooks (1 day, ★★★★★)

**Goal**: introduce functional options, typed Result, Observer interface. Foundation for all later streams. **Quality**: 0pp; **Operability**: 0 (no behavior change); **Required for**: G1, G2, G3, G4.

### Files (new)
- `config.go` (~80 LOC): `cfgInternal` struct (private), `Opt func(*cfgInternal)`, `WithModel/WithAPIKey/WithTimeout/WithMaxDocs/WithObserver/WithHTTPClient` options.
- `result.go` (~40 LOC): `Result`, `Status` + `String()` method.
- `hooks.go` (~50 LOC): `Observer` interface + `noopObserver` default + `safeCall` wrapper (recovers panics, never blocks client).
- `client_v2.go` (~60 LOC): new `New(url string, opts ...Opt) *Client` signature; `RerankWithResult(ctx, q, docs, opts...) (*Result, error)`. v1 `New(cfg, logger)` kept in `client.go` as deprecated wrapper.

### v1 compatibility wrapper
```go
// New (v1) — Deprecated: use New(url string, opts ...Opt).
// Kept for backward compatibility. Translates Config to options.
func NewLegacy(cfg Config, logger *slog.Logger) *Client {
    opts := []Opt{
        WithModel(cfg.Model),
        WithAPIKey(cfg.APIKey),
        WithTimeout(cfg.Timeout),
        WithMaxDocs(cfg.MaxDocs),
    }
    if cfg.MaxCharsPerDoc > 0 {
        opts = append(opts, WithMaxCharsPerDoc(cfg.MaxCharsPerDoc))  // alias to MaxTokens later
    }
    if logger != nil {
        opts = append(opts, WithObserver(slogObserver{logger}))
    }
    return New(cfg.URL, opts...)
}
```

NOTE: `New` cannot have two signatures in Go. Resolution:
- Keep current `func New(cfg Config, logger *slog.Logger) *Client` as **the v1 entry**.
- Add `func NewClient(url string, opts ...Opt) *Client` as **the v2 entry**.
- v1.x callers untouched. v2.x callers use `NewClient`. In v2.0 final (M13+), rename `New` → `NewLegacy` and `NewClient` → `New`. Documented in CHANGELOG.

### Metrics (new)
None in G0 — foundational only. G1+ wire metrics into hooks.

### Tests
- `config_test.go`: each Opt applies correctly to cfgInternal; defaults sane (Timeout=0 → no timeout, MaxDocs=0 → 50, etc.)
- `result_test.go`: Status.String() values, Result zero-value safe
- `hooks_test.go`: noopObserver no-ops, safeCall recovers panics, all 6 callbacks fire in expected order under instrumented client
- `client_v2_test.go`: `NewClient` produces equivalent client to `New(Config{...})`; `RerankWithResult` returns `Status=Ok` on success, `Status=Skipped` on empty docs/no URL, `Status=Degraded` on HTTP 500
- **v1 compat test**: `TestV1ApiUnchanged` — load v1 fixtures (existing tests), assert byte-identical `[]Scored` output

### Ablation harness
G0 has no quality ablation (foundational). Verification: existing M11 smoke runs at parity (±1pp) with G0 merged.

### Implementer prompt (G0)

```
TASK: Stream G0 — Foundation: Options + Result + Hooks
PLAN: docs/superpowers/plans/2026-04-29-m12-10-rerank-client-v2.md (this file)
SECTION: "Stream G0 — Foundation"

You are an implementer subagent for go-kit/rerank.Client v2.0.

🚨 MANDATORY:
- Use go-code MCP first: `mcp__go-code__understand` on Client.Rerank and Client.callCohere
  to read current behavior; `mcp__go-code__code_search "func New|Config struct"` to map
  v1 surface.
- v1 API surface is contract. NEVER silently disable v1 path.
- Two-stage review BEFORE you report DONE (spec compliance, then code quality).
- Worktree-isolated. Branch from go-kit main, NOT from MemDB.
- Push + PR via gh. NEVER merge.

WORKTREE SETUP:
1. cd ~/src/go-kit && git fetch origin
2. git worktree add /tmp/gokit-g0 -b feat/m12-10-rerank-foundation origin/main
3. cd /tmp/gokit-g0/rerank

IMPLEMENT (per plan G0 section, files: config.go, result.go, hooks.go, client_v2.go):
- Functional options pattern (Opt = func(*cfgInternal))
- Result + Status enum
- Observer interface with safeCall wrapper (panic-safe, nil-safe)
- NewClient(url, opts...) added — v1 New(cfg, logger) preserved as-is
- RerankWithResult(...) (*Result, error) — wraps existing Rerank, fills Status
- v1 Rerank(ctx, q, docs) []Scored kept unchanged — calls RerankWithResult under the hood
  but discards Status

VERIFICATION (must pass before reporting DONE):
- `go build ./...` clean
- `go vet ./...` clean
- `go test ./... -count=1 -race -short` green
- TestV1ApiUnchanged exists and passes (v1 callers see byte-identical output)
- `go test -bench=. -run=^$ -benchmem ./rerank/` — N0 alloc regression vs v1 (within 5%)

DELIVERABLE: PR URL + LOC counts per new file + test pass output + benchmark delta.
```

### Spec reviewer prompt (G0)
```
Verify Stream G0 implementation against plan section "Stream G0 — Foundation":
- All 4 new files exist (config.go, result.go, hooks.go, client_v2.go)
- All listed Opt functions present and apply correctly to cfgInternal
- Result + Status enum match spec exactly (4 status values, String() method)
- Observer interface has all 6 callbacks
- safeCall wrapper recovers panics — verified by test
- NewClient(url, opts...) exists; New(cfg, logger) v1 path UNCHANGED (file diff in client.go is additive only)
- RerankWithResult exists and returns proper Status for: success / empty-docs / no-URL / HTTP-500
- TestV1ApiUnchanged passes (byte-identical v1 output)
- No new external dependencies in go.mod
```

### Code reviewer prompt (G0)
```
Standard go-kit code review focus on G0 PR:
- Functional options idiomatic (Opt = func(*cfgInternal); options package-private internals)
- Observer dispatch is panic-safe AND nil-safe (default noopObserver, recover() in safeCall)
- No goroutine leaks (G0 has no goroutines yet — verify no `go func` introduced)
- Result struct field tags absent (no JSON serialization needed — internal type)
- v1 Config + New still exposed and behavior preserved
- Documentation: each Opt has 1-line godoc; Result/Observer interfaces fully documented
- Zero new deps in go.mod (go-kit "zero bloat" rule)
```

---

## Stream G1 — Resiliency + Multi-Model Fallback (2 days, ★★★★★)

**Goal**: convert the "instant fail on any error" path into retry+circuit-breaker+fallback chain. **Quality**: 0pp; **Operability**: -50% timeout failures observed under M11 load + zero downtime when primary embed-server stalls.

### Files (new)
- `retry.go` (~90 LOC): `RetryPolicy{MaxAttempts, BaseBackoff, MaxBackoff, Multiplier, Jitter, RetryableStatus}`; helper `do[T any](ctx, p RetryPolicy, fn func() (T, error)) (T, error)`.
- `circuit.go` (~140 LOC): `CircuitBreaker` FSM (Closed → Open → HalfOpen). Thread-safe via `sync.RWMutex`. `CircuitConfig{FailThreshold, OpenDuration, HalfOpenProbes, FailRateWindow}`.
- `fallback.go` (~70 LOC): `WithFallback(secondary *Client)` Opt. Inside `RerankWithResult`: try primary; on Status=Degraded **with non-4xx error**, try secondary; on success — `Status=Fallback`.

### Defaults (sane for embed-server prod load)
- Retry: `MaxAttempts=3, BaseBackoff=200ms, MaxBackoff=2s, Multiplier=2.0, Jitter=0.1, RetryableStatus={500,502,503,504}`
- Circuit: `FailThreshold=5, OpenDuration=30s, HalfOpenProbes=1, FailRateWindow=10s`
- Fallback: nil (no secondary by default)

### Wiring (in `client.callCohere`)
```
Rerank → check circuit (open? short-circuit Status=Degraded ErrCircuitOpen)
       → cache.Get if cache configured (G4 hooks here)
       → retry.Do(ctx, policy, func() { hc.Do(req) })
       → on success: cache.Set, observer.OnAfterCall, Status=Ok
       → on retry-exhausted Degraded: if fallback configured, fallback.Rerank
       → on fallback success: Status=Fallback
       → on fallback failure or no fallback: Status=Degraded, return passthrough
```

### Metrics (new)
```
rerank_retry_attempt_total{model, attempt}                    counter
rerank_circuit_state{model, state}                            gauge
rerank_circuit_transition_total{model, from, to}              counter
rerank_giveup_total{model, reason}                            counter  (exhausted|circuit_open|4xx|fallback_used)
rerank_fallback_used_total{primary, secondary}                counter
```

### Tests
- `retry_test.go`: max attempts, backoff timing (deterministic via injected clock), retryable status filter, ctx cancellation aborts, jitter applied (statistical bounds), 4xx no-retry
- `circuit_test.go`: state transitions exhaustive (closed→open after threshold, open→half-open after timer, half-open→closed on success, half-open→open on failure), thread-safe under parallel invocation (-race)
- `fallback_test.go`: primary 503 → secondary called → Status=Fallback; primary 4xx → no fallback (4xx is caller error); primary timeout exhausted → fallback; both fail → Status=Degraded
- `client_test.go` extended: `TestClient_RetryOn5xx`, `TestClient_NoRetryOn4xx`, `TestClient_CircuitOpenSkipsCall`, `TestClient_HookCallbacksFire`, `TestClient_FallbackChain`

### Ablation harness (G1 specific)
- **Synthetic 503 test**: injected HTTP 503 stream at 30% rate over 1000 requests → assert (a) ≥99% eventually return Status=Ok or Status=Fallback (with 1 fallback configured), (b) circuit opens within 5 fails, (c) p99 latency stays bounded (no infinite retry storm)
- **No quality ablation** — G1 is operability only

### Implementer prompt (G1)
```
TASK: Stream G1 — Resiliency + Multi-Model Fallback
PLAN: same plan file, SECTION "Stream G1"
PREREQ: G0 merged (uses Opt, Observer, Result types).

[same MANDATORY block as G0]

WORKTREE: branch feat/m12-10-rerank-resiliency from origin/main (after G0 merged)

IMPLEMENT per G1 section: retry.go, circuit.go, fallback.go + wire into client.callCohere
+ extend metrics.go with 5 new metric series.

DEFAULTS active: Retry on {500,502,503,504} 3 attempts; Circuit threshold=5; Fallback nil.
v1 callers (no Opt) get retry-on-by-default — this is INTENTIONAL but document loudly in CHANGELOG
("v1.x → v2.0: retry on 5xx is now default; opt out via WithRetry(rerank.NoRetry)").

VERIFICATION:
- All G0 tests still pass
- New tests cover: retry exhaustion, circuit transitions, ctx cancellation, 4xx no-retry, fallback chain
- Synthetic 503 ablation harness passes (≥99% eventual success with fallback)
- `go test -race ./...` green (concurrent circuit access)

DELIVERABLE: PR URL + new metric series listed + ablation harness output (success rate, p99 latency).
```

### Spec reviewer prompt (G1)
```
Verify Stream G1:
- All RetryPolicy fields respected (esp. Jitter, RetryableStatus filter)
- CircuitBreaker thread-safe (race detector clean under TestCircuit_ConcurrentAccess)
- Hooks fire in spec order: OnBeforeCall → OnRetry* → OnAfterCall (or OnCircuitTransition)
- v1 callers (zero opts) get retry-on-by-default — intentional, MUST be in CHANGELOG
- 4xx errors NEVER retry (test: 400 → 1 attempt, no retry)
- Fallback only triggers on non-4xx Degraded — not on Ok or 4xx
- All 5 new metrics pre-registered with zero label values
- Synthetic 503 ablation harness output present in PR body
```

### Code reviewer prompt (G1)
```
- Goroutine leaks in retry path: verify timer.Stop() in all branches
- Circuit state machine: mutex strategy (RWMutex correct? state read under RLock, write under Lock)
- Memory: retry buffer reuse (do not double-marshal request body — use bytes.NewReader pattern)
- Error wrapping: `errors.Is(err, ErrCircuitOpen)` works; original HTTP error preserved
- Hooks contract: panic in observer does NOT kill request (safeCall recovery)
- Fallback chain: max depth 1 (primary→secondary), no recursion or infinite loop
- Default RetryPolicy struct copies, not pointer share (avoid mutation across clients)
```

---

## Stream G2 — Score Quality + Filtering API (2 days, ★★★★★)

**Goal**: normalize scores to known range, accept threshold/TopN at call site, support instruction prefixes, per-source boost, token-aware truncation, drift histogram. **Quality**: +1-3pp on bge-reranker-v2-m3 (instruction prefix + sigmoid + sourceboost combined); **Operability**: drift detection via score histogram.

### Files (new)
- `normalize.go` (~80 LOC): `NormalizeMode` (None|Sigmoid|MinMax|ZScore); pure `Normalize(scores []float32, mode) []float32`. Sigmoid uses `math.Exp` (not custom approx).
- `instruction.go` (~50 LOC): `WithInstruction(query, doc string)` — applied in callCohere. **bge-m3 needs no prefix** (model card explicit). For bge-v1.5 / E5 callers.
- `tokens.go` (~70 LOC): `WithMaxTokensPerDoc(int)`. Approximate tokenizer: Cyrillic runes ÷ 1.5, Latin runes ÷ 4, others raw count. For Mixed-script doc, sum per-script. Sufficient for bge-m3 512-token window. `MaxCharsPerDoc` deprecated alias.
- `source_weights.go` (~50 LOC): `WithSourceWeights(map[string]float32)`. Applied AFTER normalize: `final = normalized * weight[doc.Source]`. Default weight 1.0 if source not in map. Doc gets new field `Source string` (additive).
- `metrics.go` extended: `rerank_score_distribution{model, bucket}` histogram; `rerank_below_threshold_total{model}`; `rerank_truncate_total{model, reason}`.

### `Doc` extension (additive)
```go
type Doc struct {
    ID     string
    Text   string
    Source string  // NEW G2 — for SourceWeights, optional
}
```

v1 callers using `Doc{ID, Text}` work unchanged (Source = "" → no weight applied).

### `RerankOpt` per-call options
```go
type rerankCallCfg struct {
    TopN      int
    Threshold float32
    DryRun    bool
}
func WithTopN(n int) RerankOpt
func WithThreshold(min float32) RerankOpt  // applied AFTER normalize, AFTER sourceboost
func WithDryRun() RerankOpt                 // skip HTTP, return passthrough — testing
```

### Pipeline order (canonical)
```
docs[]
  → MaxTokensPerDoc truncate (G2)
  → optional instruction prefix (G2)
  → POST /v1/rerank
  → raw scores
  → Normalize per NormalizeMode (G2, default None for v1 compat)
  → SourceWeights apply (G2, default no-op)
  → score_distribution metric emit (G2)
  → Threshold filter (per-call opt)
  → TopN cut (per-call opt)
  → tail preserve (existing)
  → return Scored[]
```

### Tests
- `normalize_test.go`: each mode produces expected output; sort order preserved; Sigmoid handles extreme logits ±20 without inf
- `instruction_test.go`: prefix application, empty defaults are no-op (bge-m3 case), HTTP request body contains prefix
- `tokens_test.go`: ru/en/mixed truncation; assert post-truncate token count ≤ MaxTokens; OnTruncate hook fires
- `source_weights_test.go`: per-source multiplier; missing source → weight 1.0; no panic on nil map
- Score histogram test: known scores produce expected bucket counts

### Ablation harness (G2 specific)
- **A/B on conv-26 50 QA** (LoCoMo single-conv):
  - Baseline: `WithNormalize(None)` (v1 behavior)
  - Treatment: `WithNormalize(Sigmoid) + WithInstruction("Represent this question for searching relevant passages:", "")` — but ONLY if model is bge-v1.5 or E5; bge-m3 leaves both empty
  - Treatment B: + `WithSourceWeights({"raw_observation": 1.10, "agg_summary": 0.95})` (test heterogeneous)
  - Metrics: headline LLM Judge %; per-cat-1 % (single-hop, where ranker matters most); p99 score distribution shift
- **Drift simulation**: feed shifted score distribution (synthetic), assert `rerank_score_distribution{bucket=">1.0"}` increase visible

### Implementer prompt (G2)
```
TASK: Stream G2 — Score Quality + Filtering API
PLAN: same plan file, SECTION "Stream G2"
PREREQ: G0 merged (uses Opt + Observer); G1 merged (uses Status types)

[same MANDATORY block]

WORKTREE: branch feat/m12-10-rerank-quality-api from origin/main (after G1 merged)

IMPLEMENT per G2 section: normalize.go, instruction.go, tokens.go, source_weights.go.
Extend Doc with Source string field (additive). Wire pipeline in defined order
(truncate → prefix → POST → normalize → weight → metric → threshold → TopN → tail).

CRITICAL: bge-reranker-v2-m3 (our prod model) needs NO instruction prefix per
model card. WithInstruction defaults empty. Test on bge-v1.5 fixture, but DO NOT
ship instruction prefix to bge-m3 in MemDB wiring.

VERIFICATION:
- All G0+G1 tests pass
- Pipeline order test: assert each stage applied in spec order via instrumented Observer
- Ablation harness on conv-26 50 QA: report headline % baseline vs each treatment
  (sigmoid alone, sigmoid+sourceweights, sigmoid+sourceweights+instruction-on-bge-v1.5)
- score_distribution histogram visible in metrics output

DELIVERABLE: PR URL + ablation table (3 treatments × headline%/cat-1%/p99-score) + 
verification that bge-m3 prod path has NO instruction prefix wired.
```

### Spec reviewer prompt (G2)
```
- All NormalizeMode values implemented; Sigmoid uses math.Exp (NOT custom)
- Sort order preserved across all modes (monotonic transformations only)
- Threshold applied AFTER normalize (NOT before); TopN AFTER threshold
- TopN=0 returns all; TopN > len(docs) → no panic, returns all
- WithInstruction empty default (v1 compat + bge-m3 correctness)
- SourceWeights nil map → no-op; missing source → weight 1.0; no panic
- MaxTokensPerDoc tokenizer correct on ru/en/mixed (test fixtures)
- score_distribution buckets cover [-2, 2] range with underflow/overflow
- Ablation harness output included in PR body
```

### Code reviewer prompt (G2)
```
- No score copies (in-place where safe; explicit copy only when needed)
- Sigmoid uses math.Exp (precision); test with extreme logits
- Threshold comparison on normalized score, NOT raw
- Pipeline ordering correct (verify by reading client.go)
- Tokenizer documented complexity (O(n) per doc)
- Doc.Source field doesn't break JSON marshal (no tag = field name capitalized)
```

---

## Stream G3 — Multi-stage Cascade (2 days, ★★★★, library type only)

**Goal**: pure library impl. `Cascade` type implements `Reranker` interface, chains arbitrary `Reranker` stages with TopN cuts. **Caller decides** when/if to wire to actual multi-model deploy. **Quality**: 0pp ± 1pp (cascade = compute optimization tool, not quality lever); **Operability**: per-stage observability via existing `Observer` hooks.

**Out of scope**: embed-server deploy, model selection, MemDB integration. This stream ships the type + tests; deploy/wiring is a separate decision.

### File
- `cascade.go` (~160 LOC): `Cascade{Stages []StageConfig}` — implements `Reranker` interface; `Cascade.Rerank(ctx, q, docs, opts)` runs stages in order.

### API
```go
type StageConfig struct {
    Reranker          Reranker          // any Reranker (Client, MultiQuery, ...)
    KeepTopN          int               // hand to next stage; 0 → all
    StopBelowThreshold float32          // early-exit if all top scores below threshold
    Label             string            // for metrics (e.g. "fast-prefilter")
}

type Cascade struct {
    Stages []StageConfig
}

func (c Cascade) Rerank(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error)
func (c Cascade) Available() bool  // all stages available
```

### Wiring example
```go
fast := rerank.NewClient("http://embed:8082", rerank.WithModel("bge-reranker-base"))
slow := rerank.NewClient("http://embed:8082", rerank.WithModel("bge-reranker-v2-m3"))
cascade := rerank.Cascade{Stages: []rerank.StageConfig{
    {Reranker: fast, KeepTopN: 20, Label: "fast-prefilter"},
    {Reranker: slow, KeepTopN: 10, Label: "slow-rerank"},
}}
res, err := cascade.Rerank(ctx, query, docs)
```

### Metrics
```
rerank_cascade_stage_total{label, stage_idx, outcome}     counter
rerank_cascade_topn_throughput_in{label}                  histogram (input doc count)
rerank_cascade_topn_throughput_out{label}                 histogram (after KeepTopN)
rerank_cascade_early_exit_total{label, reason}            counter
rerank_cascade_total_duration_seconds                     histogram
```

### Tests
- `cascade_test.go`: stage chaining, KeepTopN cuts list, StopBelowThreshold short-circuits, Hooks propagate to inner Rerankers, empty Stages → return docs unchanged, mid-stage failure handled (Status=Degraded propagates)
- Integration test with mock HTTP servers returning known scores at 2 stages

### Verification (G3-specific, library-level only)
- Mock 2-stage cascade: stage 1 returns 20 ranked docs from 50 input → stage 2 returns 10 from 20. Verify final order, count, scores.
- Memory benchmark via `-benchmem`: peak alloc = max(stage_in size), not sum across stages.
- No live model needed; live ablation (real latency / quality overlap) is caller's responsibility post-deploy.

### Implementer prompt (G3)
```
TASK: Stream G3 — Multi-stage Cascade (pure library type)
PLAN: same plan file, SECTION "Stream G3"
PREREQ: G2 merged (uses Reranker interface from G0, score normalization from G2)

[MANDATORY block]

WORKTREE: branch feat/m12-10-rerank-cascade from origin/main.

IMPLEMENT per G3 section: cascade.go (single file). Cascade.Rerank implements
Reranker interface. KeepTopN 0 = pass all. StopBelowThreshold 0 = no early exit.

NO real embed-server, NO live model. Tests use mock httptest.Server returning
fixed scores per stage. Caller decides deploy/wiring later.

VERIFICATION:
- All prior tests pass
- Cascade unit tests cover: chaining, KeepTopN cut, StopBelowThreshold, hooks,
  empty stages, mid-stage failure, Reranker interface compliance
- Memory benchmark: peak alloc = max(stage_in) not sum (verify via -benchmem)

DELIVERABLE: PR URL + LOC counts + test pass output + memory benchmark.
```

### Spec reviewer prompt (G3)
```
- Cascade implements Reranker interface (compile-check)
- Stage chain runs in order; each stage receives output of previous
- KeepTopN=0 means "pass all"; KeepTopN > len(docs) → no-op
- StopBelowThreshold computed per-stage on normalized scores (NOT raw)
- Hooks fire per-stage (nested ctx scope OK)
- Empty Stages → return docs unchanged with Status=Skipped (no panic)
- Mid-stage failure: Status=Degraded propagates, downstream stages skipped
- Ablation harness output present in PR body
```

### Code reviewer prompt (G3)
```
- Slice aliasing: each stage gets own slice (Cascade.Rerank does not mutate caller's docs)
- Score from stage[N] survives or is replaced — DOCUMENT WHICH (default: replaced; consider opt to keep)
- Memory: peak alloc = max stage size, not sum (verify benchmark)
- No deadlock if same Reranker instance reused across stages (sync.Mutex etc.)
- Cascade is value type or pointer? Document choice (recommend value — config-like)
```

---

## Stream G4 — Quality Boosters: Multi-Query + Cache (2 days, ★★★★, parallel-safe with G3 after G2)

**Goal**: opt-in features lifting headline 1-3pp on multi-hop queries + reducing latency via cache. **Two sub-features** (G4a, G4b). G4-MMR explicitly **DROPPED** from this stream (out of layer — see Plan revision history).

### Files (new)
- `multiquery.go` (~140 LOC): `MultiQuery{Inner Reranker, Combine CombineMode}`; method `RerankMulti(ctx, queries []string, docs []Doc, opts ...RerankOpt) (*Result, error)`. Implements `Reranker` via wrapping (treats `query[0]` as primary if called via plain `Rerank`).
- `cache.go` (~60 LOC): `Cache` interface only. Wired in `client.callCohere`: cache lookup before HTTP, cache set after success. Key = `sha256(model + "\x00" + query + "\x00" + doc.Text)`. **NO impl shipped** — caller supplies (memdb-go uses Redis, vaelor uses sync.Map).

### API — multi-query
```go
type CombineMode uint8
const (
    CombineMax CombineMode = iota  // max score across queries
    CombineAvg                      // arithmetic mean
    CombineRRF                      // reciprocal rank fusion (k=60 default)
)

type MultiQuery struct {
    Inner   Reranker
    Combine CombineMode
    RRFK    int  // for CombineRRF, default 60 (LangChain4j convention)
}

func (m MultiQuery) RerankMulti(ctx, queries []string, docs []Doc, opts ...RerankOpt) (*Result, error)
```

Concurrency: `errgroup.WithContext(ctx)` — N parallel calls (one per query) with bounded concurrency (default min(len(queries), 4)). On any 4xx → fast-fail. On all-but-one 5xx → succeed with partial results (CombineMax fills missing as 0; CombineRRF skips missing).

### API — cache
```go
type Cache interface {
    Get(ctx context.Context, key string) (score float32, ok bool)
    Set(ctx context.Context, key string, score float32)
}

func WithCache(c Cache) Opt  // wires cache into client
```

`go-kit/rerank` ships **no Cache impl**. Tests use a stub `mapCache`. Document expected impl behaviors (TTL, eviction) in interface godoc.

### Metrics
```
rerank_multiquery_combine_total{mode}                   counter
rerank_multiquery_partial_total{reason}                 counter (one_query_failed | majority_failed)
rerank_cache_hit_total{model}                           counter
rerank_cache_miss_total{model}                          counter
rerank_cache_set_total{model}                           counter
```

### Tests
- `multiquery_test.go`: CombineMax / CombineAvg / CombineRRF correctness on known inputs; partial failure handling; bounded concurrency; -race clean
- `cache_test.go`: cache hit short-circuits HTTP (verified via mock server call count); cache key includes model name (different model → different key); ctx cancellation flows to Cache.Get/Set
- Integration: same (query, doc) two consecutive Rerank calls → 2nd is cache hit; metric `rerank_cache_hit_total` increments

### Ablation harness (G4 specific)
- **Multi-query A/B on conv-26 cat-2** (multi-hop):
  - Baseline: single query
  - Treatment: query + 2 LLM-generated paraphrases via Combine=Max
  - Expected: +1-3pp on cat-2; latency 3× (parallel with concurrency=3 → 1× wall clock)
- **Cache hit rate**: instrumented MemDB run on hot 100-QA test → measure `cache_hit / (hit+miss)` ratio. Expected 5-15% on staged refine+justify (paraphrase-driven near-misses).

### Implementer prompt (G4)
```
TASK: Stream G4 — Quality Boosters (Multi-Query + Cache)
PLAN: same plan file, SECTION "Stream G4"
PREREQ: G0+G1+G2 merged. G3 may merge first or in parallel (disjoint files).

[MANDATORY block]

WORKTREE: branch feat/m12-10-rerank-boosters from origin/main.

IMPLEMENT per G4 section:
  - multiquery.go (RerankMulti + 3 combine modes + bounded concurrency)
  - cache.go (Cache interface ONLY — no impl)
  - Wire WithCache into client.callCohere (lookup before HTTP, set after success,
    key = sha256(model + query + doc.Text))

CRITICAL: NO LRU IMPL in go-kit. Cache is interface only. Tests use stub.
DO NOT add hashicorp/golang-lru or similar to go.mod.

VERIFICATION:
- All prior tests pass
- Multi-query: 3 combine modes correctness; partial-failure handling; -race clean
- Cache: hit short-circuits HTTP; cache key correct; metrics emit
- go.mod unchanged (zero new deps)
- Ablation harness output: cat-2 multi-query treatment vs baseline; cache hit rate

DELIVERABLE: PR URL + ablation table + go.mod diff (must be empty for new deps).
```

### Spec reviewer prompt (G4)
```
- CombineMax / Avg / RRF produce correct outputs on fixed-input tests
- Multi-query bounded concurrency (default min(N, 4)); ctx cancellation propagates
- Empty queries → ErrEmptyQueries (NOT silent skip)
- Cache interface — no impl in go-kit; stub used in tests
- Cache key = sha256(model + "\x00" + query + "\x00" + doc.Text); changing model changes key
- ctx threaded through Cache.Get/Set
- Ablation harness output present
- go.mod unchanged for new external deps (zero-dep promise)
```

### Code reviewer prompt (G4)
```
- Multi-query: errgroup pattern correct; no goroutine leak on early ctx cancellation
- Combine implementations: stable order on ties (use SliceStable)
- Cache: thread-safe ASSUMED FROM IMPL (interface contract documented)
- Cache key SHA256 (FIPS-friendly), NOT MD5
- Integration: cache lookup BEFORE retry/circuit (cache hit shouldn't trigger circuit)
- No raw query/doc in metric labels (PII-safe: only model name in labels)
```

---

## Cross-stream coordination

### Disjoint-file matrix (parallel-safe analysis)

| Stream | Files NEW | Files EDITED |
|--------|-----------|--------------|
| G0 | config.go, result.go, hooks.go, client_v2.go | client.go (additive: NewLegacy wrapper) |
| G1 | retry.go, circuit.go, fallback.go | client.go (callCohere wrap), metrics.go |
| G2 | normalize.go, instruction.go, tokens.go, source_weights.go | client.go (Rerank pipeline), http.go (prefix), metrics.go, doc.go (Doc.Source) |
| G3 | cascade.go | metrics.go |
| G4 | multiquery.go, cache.go | client.go (cache hook in callCohere), metrics.go |

**Conflict zones**: `client.go::callCohere` (G1, G2, G4 all touch it), `metrics.go` (G1-G4 all add metrics).

**Mitigation**:
- **Strict serial G0 → G1 → G2** (each touches client.go::callCohere wiring).
- **Parallel-safe**: G3 with G4 after G2 lands (G3 only touches new cascade.go + metrics.go; G4 touches client.go::callCohere cache hook, but G2's pipeline is stable by then).
- Even with parallel G3+G4, controller serializes merges (one at a time).

### MemDB vendor bump (controller-only, NOT subagent task)

go-kit version bumps in MemDB are **controller's responsibility** after each stream merges. Subagents do **not** touch MemDB. The bump procedure (for controller reference):
1. `cd ~/src/MemDB/memdb-go && go get github.com/anatolykoptev/go-kit@<sha>`
2. `go mod vendor` (CRITICAL — `vendor/modules.txt` mismatch breaks build otherwise)
3. Wire new feature into `internal/search/rerank/ce.go` per stream feature surface

This is operational integration work, distinct from go-kit library development.

---

## Verification per stream (library-level only)

Each stream's implementer subagent runs **only** go-kit checks:

1. `go build ./...` clean
2. `go vet ./...` clean
3. `go test ./... -count=1 -race -short` green (race detector mandatory for G1+ which adds concurrency)
4. `TestV1ApiUnchanged` green (the v1 invariant)
5. Stream-specific synthetic harness (mock servers, table-driven tests):
   - **G0**: equivalence test NewClient↔v1 New on existing fixtures
   - **G1**: synthetic 503 stream — ≥99% eventual success with retry+fallback wired; circuit opens within threshold
   - **G2**: pipeline order test (truncate → prefix → POST → normalize → weight → metric → threshold → TopN); each stage observable via instrumented Observer
   - **G3**: 2-stage mock cascade — final ordering + memory benchmark
   - **G4 multi-query**: 3 combine modes correctness on fixed inputs; bounded concurrency under -race
   - **G4 cache**: hit short-circuits HTTP (mock call count = 1 across 2 Rerank calls with identical args)
6. `git diff origin/main -- go.mod` empty for new external `require` entries

**Live LoCoMo / MemDB integration smokes are controller's concern**, run after vendor bump, not part of subagent's DoD.

---

## Done criteria (M12.10 closure)

1. **All 5 streams (G0-G4) merged in go-kit + vendored in MemDB.** G3-pre merged in embed-server.
2. **Combined ablation on 50-QA conv-26**: headline ≥ **53% excl-5** (M11 baseline 50%; combined G2+G4 expected +2-4pp). If under 53%, root-cause and decide: ship anyway with operability win, or pull a stream.
3. **All new metrics emit during smoke** (verified via `curl :8080/metrics | grep rerank_`).
4. **Zero regressions in existing tests** (go-kit + MemDB CI green, race detector clean).
5. **No silent disable of any v1 behavior** — all new features opt-in via Opt; defaults preserve v1 semantics; v1 callers see byte-identical output (verified by `TestV1ApiUnchanged`).
6. **Zero new external deps in go-kit go.mod** (zero-bloat promise; verified by go.mod diff being empty for `require` lines, except internal version bumps).
7. **PR bodies document**: diff summary, file LOC counts, ablation harness output (numeric), specific hypotheses validated.
8. **CHANGELOG entry in go-kit** documenting v1 → v2 migration path with examples.

---

## Sprint sequencing

```
Day 1:    G0 (foundation)          → review × 2  → merge → MemDB vendor bump → smoke
Day 2-3:  G1 (resiliency+fallback) → review × 2  → merge → bump → smoke
Day 4-5:  G2 (quality api)         → review × 2  → merge → bump → smoke
Day 5:    G3-pre (embed-server bge-base) — parallel with G2 review (different repo)
Day 6-7:  G3 (cascade) AND G4 (boosters) — parallel implementers (disjoint files)
            G3 merge → bump → smoke
            G4 merge → bump → smoke
Day 8:    Combined sample run + headline write-up + drift baseline snapshot
Day 9:    M12.10 done write-up; surface follow-ups for M13
```

If any stream blows budget by >1 day, controller pulls scope (drop G4 cache, drop G3, or move to M13). Quality streams (G1+G2) are non-negotiable; G3+G4 are stretch.

---

## Risk + mitigation

| Risk | Mitigation |
|------|-----------|
| go-kit + MemDB vendor cycle breaks (`vendor/modules.txt` mismatch) | Run `go mod vendor` ALWAYS after `go get`. Test build before push. |
| Embed-server bge-base prereq slips → G3 blocked | G3 marked HARD PREREQ on G3-pre. If G3-pre slips >1 day, drop G3 to M13. G3 is stretch goal. |
| G1 retry storm under embed-server saturation | Circuit breaker is the failsafe. Synthetic 503 ablation validates this BEFORE merge. |
| G1 default-on retry breaks v1 caller assumption ("instant fail") | CHANGELOG explicit; opt-out via `WithRetry(rerank.NoRetry)`. v1.x → v2.0 IS a major version bump. |
| G4 cache stale after model change | Cache key includes model name; changing `WithModel` invalidates implicitly. Document in interface godoc. |
| Subagent silently disables a v1 path while implementing | TestV1ApiUnchanged in G0 baseline. Every subsequent PR must keep it green. Spec reviewer enforces. |
| Ablation harness gives noisy delta (±1pp) → stream "passes" by luck | Multi-seed harness (3 random seeds, report mean ± std). If std > delta, mark INCONCLUSIVE and require larger sample. |

---

## Subagent dispatch template (per stream)

```
TASK: [Stream Letter] — [Stream Title]
PLAN: docs/superpowers/plans/2026-04-29-m12-10-rerank-client-v2.md
SECTION: read [Stream Letter] section verbatim — implement EXACTLY per spec.

You are an implementer subagent for go-kit/rerank.Client v2.0.

🚨 MANDATORY:
- v1 API is contract. NEVER silently disable v1 path. TestV1ApiUnchanged must stay green.
- Use go-code MCP first: explore go-kit/rerank, understand current Rerank/callCohere
  before editing.
- Two-stage review BEFORE you report DONE (spec compliance, then code quality).
- Worktree-isolated. Branch from go-kit main, NOT from MemDB.
- Push + PR via gh. NEVER merge — controller decides.

WORKTREE SETUP:
1. cd ~/src/go-kit && git fetch origin
2. git worktree add /tmp/gokit-<letter> -b feat/m12-10-rerank-<feature> origin/main
3. cd /tmp/gokit-<letter>/rerank

VERIFICATION (must pass before reporting DONE):
- go build ./... clean
- go vet ./... clean
- go test ./... -count=1 -race -short green
- TestV1ApiUnchanged passes
- Ablation harness output matches per-stream expected delta (or BLOCKED if below)
- go.mod diff empty for new external deps (zero-bloat)

DELIVERABLE: PR URL + LOC per file + test output + ablation numbers.

If you hit BLOCKED: report status, do NOT force a workaround.
If you hit NEEDS_CONTEXT: report and wait — do NOT guess prod config.
```

---

## Stream G5 — MathReranker (post-closure bonus, library only)

**Goal**: cheap prefilter stage for `Cascade` using cosine similarity + optional MMR diversity over pre-computed `Doc.EmbedVector`. Solves the "compute saving without 2nd model deploy" gap that G3 cascade couldn't fill alone (no fast multilingual model in HF zoo for our prod). **Quality**: 0pp ± 1pp (cascade is compute optimizer, not quality lever); **Speed**: 2-5× compute reduction on 50-doc batches when wired as Stage 0.

**Library-only** — no MemDB integration in this stream. Caller decides if/when to wire (env-flagged in M13).

### Files (4 NEW + 1 EDIT)

- `math_reranker.go` (~110 LOC) — `MathReranker{QueryVector, Lambda}` struct implementing `Reranker` interface
- `cosine.go` (~40 LOC) — `cosineSim(a, b []float32) float32` pure helper; handles zero-norm edge case
- `mmr.go` (~90 LOC) — `applyMMR(docs []Doc, relScores []float32, queryVec []float32, lambda float32) []Scored` pure greedy MMR
- `client.go` (EDIT) — add `Doc.EmbedVector []float32` field (additive, like Doc.Source was in G2-client)
- `metrics.go` (EDIT) — 3 new series for math stage observability

### API

```go
// MathReranker scores docs by cosine similarity to a pre-computed query
// embedding, optionally applying MMR diversity. Pure Go vector math, no HTTP.
//
// Caller computes QueryVector via embed-server /v1/embeddings before Rerank.
// Each Doc must have EmbedVector populated; docs with empty vectors get
// score=0 (sorted to tail).
//
// Composable in Cascade as Stage 0 prefilter — cheap CPU vector ops cut a
// 50-doc batch to top-N before passing to a NN reranker.
//
// Implements Reranker interface.
type MathReranker struct {
    // QueryVector — caller-provided query embedding. If empty, all scores
    // are 0 and Rerank returns input order (Status=Skipped).
    QueryVector []float32

    // Lambda controls MMR relevance-vs-diversity tradeoff (standard
    // Carbonell-Goldstein 1998 convention):
    //   1.0 = pure relevance (λ=1 → MMR equivalent of pure cosine sort)
    //   0.5 = balanced relevance/diversity (recommended default for diversity)
    //   0.0 = pure diversity (skip MMR; pure cosine sort fast path)
    // Default 0 (no MMR; falls through to pure cosine sort).
    Lambda float32
}

func (m MathReranker) Rerank(ctx context.Context, query string, docs []Doc) []Scored
func (m MathReranker) RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error)
func (m MathReranker) Available() bool

// Compile-time check
var _ Reranker = MathReranker{}
```

### `Doc.EmbedVector` additive field

```go
type Doc struct {
    ID          string
    Text        string
    Source      string       // G2-client
    EmbedVector []float32    // G5: optional, used by MathReranker
}
```

v1 callers untouched (named init `Doc{ID: ..., Text: ...}` works; positional doesn't). G5 adds nothing requiring caller changes for existing flows.

### Pipeline behavior

- Empty `QueryVector` → return `Result{Status: StatusSkipped}` with passthrough Scored (no panic)
- Doc with empty `EmbedVector` → score = 0 (will sort to bottom)
- All docs have empty vectors → effectively passthrough sorted by ID/order
- Lambda = 0 → pure cosine sort (fastest)
- Lambda > 0 → MMR greedy: pick top relevance, then iteratively select doc maximizing `λ * relevance - (1-λ) * max(cosine to picked)`
- ctx cancellation honored (early return mid-MMR loop)
- DryRun opt → passthrough (consistent with other Rerankers)

### MMR algorithm (greedy, O(n²))

```go
// applyMMR — Maximal Marginal Relevance (Carbonell & Goldstein 1998).
// Greedy selection: at each step, pick the doc maximizing
//   λ * relevance(d, q) - (1-λ) * max_{p in picked}(cosine(d, p))
//
// O(n²) in worst case; fine for typical N=50-200. Document if N > 1000.
func applyMMR(docs []Doc, relScores []float32, queryVec []float32, lambda float32) []Scored {
    n := len(docs)
    picked := make([]int, 0, n)
    remaining := make(map[int]struct{}, n)
    for i := range docs {
        remaining[i] = struct{}{}
    }

    for len(picked) < n {
        var bestIdx = -1
        var bestScore = float32(-1e30)
        for i := range remaining {
            mmrScore := lambda * relScores[i]
            if len(picked) > 0 {
                var maxSim float32
                for _, p := range picked {
                    sim := cosineSim(docs[i].EmbedVector, docs[p].EmbedVector)
                    if sim > maxSim {
                        maxSim = sim
                    }
                }
                mmrScore -= (1 - lambda) * maxSim
            }
            if mmrScore > bestScore {
                bestScore = mmrScore
                bestIdx = i
            }
        }
        if bestIdx < 0 {
            break
        }
        picked = append(picked, bestIdx)
        delete(remaining, bestIdx)
    }

    // Build Scored in MMR order. Score = original relevance (cosine to query),
    // NOT the modified MMR score — preserves caller's threshold semantics.
    out := make([]Scored, 0, n)
    for rank, idx := range picked {
        out = append(out, Scored{
            Doc:      docs[idx],
            Score:    relScores[idx],
            OrigRank: idx,
        })
        _ = rank // intentionally unused — Scored doesn't carry rank
    }
    return out
}
```

### Cosine similarity (pure helper)

```go
// cosineSim returns the cosine similarity between two equal-length vectors.
// Returns 0 if either vector has zero norm or lengths mismatch (defensive,
// no panic; caller treats as "irrelevant").
func cosineSim(a, b []float32) float32 {
    if len(a) != len(b) || len(a) == 0 {
        return 0
    }
    var dot, na, nb float32
    for i := range a {
        dot += a[i] * b[i]
        na += a[i] * a[i]
        nb += b[i] * b[i]
    }
    if na == 0 || nb == 0 {
        return 0
    }
    return dot / float32(math.Sqrt(float64(na))*math.Sqrt(float64(nb)))
}
```

### Metrics (3 new series)

```
rerank_math_score_distribution{label}     histogram  — cosine score distribution
rerank_math_mmr_applied_total{label}      counter    — MMR was triggered (lambda > 0)
rerank_math_empty_vector_total{label}     counter    — caller passed Doc with empty EmbedVector (debug aid)
```

`label` field ties to caller-provided context (e.g. "prefilter-stage"); default empty if not set. For now no Label field on MathReranker — single global dimension. If demand emerges, add `Label string` field in M13.

### Tests

**`math_reranker_test.go`**:
- `TestMathReranker_PureCosineRanking_KnownVectors` — fixed vectors, expected order
- `TestMathReranker_EmptyQueryVector_StatusSkipped` — `QueryVector: nil` → Status=Skipped
- `TestMathReranker_MissingDocVector_ScoreZero` — Doc with empty EmbedVector → score=0
- `TestMathReranker_AllDocsEmpty_PassthroughOrder` — all docs have nil vectors → input order preserved
- `TestMathReranker_Lambda0_NoMMR_PureRelevance` — Lambda=0 → ordering matches cosine sort
- `TestMathReranker_Lambda1_PureDiversity` — Lambda=1 → ordering matches max-marginal selection
- `TestMathReranker_LambdaMid_BalancedSelection` — Lambda=0.5 → known-input expected output
- `TestMathReranker_SatisfiesRerankerInterface` — `var _ Reranker = MathReranker{}` compile assertion
- `TestMathReranker_CtxCancellation_StopsEarly` — long MMR loop, ctx canceled mid-way
- `TestMathReranker_DryRunOpt_ReturnsPassthrough` — WithDryRun() → Status=Skipped, no math
- `TestMathReranker_RerankWithResult_StatusOk` — happy path, 5 docs, 5 vectors, scored output
- `TestMathReranker_AvailableTrue_WhenQueryVectorPresent`
- `TestMathReranker_AvailableFalse_WhenQueryVectorEmpty`

**`cosine_test.go`**:
- `TestCosineSim_KnownPairs` — sanity: parallel vectors → 1.0; orthogonal → 0; antiparallel → -1.0 (well, +0 since it's actually -1, document)
- `TestCosineSim_EmptyVectors_ReturnsZero` — defensive
- `TestCosineSim_LengthMismatch_ReturnsZero` — defensive
- `TestCosineSim_ZeroNorm_NoNaN` — input all zeros → 0, not NaN

**`mmr_test.go`**:
- `TestApplyMMR_Lambda1_PureDiversity_KnownInput`
- `TestApplyMMR_Lambda0_PureRelevance_KnownInput` — ordering matches cosine sort
- `TestApplyMMR_EmptyDocs_NoP anic`
- `TestApplyMMR_AllSimilarDocs_Lambda05_DegradesGracefully` — high mutual similarity, should still order
- `TestApplyMMR_LinearComplexity_BoundsForN200` — bench-style, peak alloc bounded

**`v1_compat_test.go` UNCHANGED** — TestV1ApiUnchanged 9/9 byte-identical.

### Cascade composition example (in godoc, NOT wired in code)

```go
math := rerank.MathReranker{
    QueryVector: queryVec,  // caller computed via embed-server
    Lambda:      0.7,        // 70/30 relevance/diversity
}
nn := rerank.NewClient(url, rerank.WithModel("gte-multi-rerank"))

cascade := rerank.Cascade{Stages: []rerank.StageConfig{
    {Reranker: math, KeepTopN: 20, Label: "math-prefilter"},
    {Reranker: nn,   KeepTopN: 10, Label: "nn-deep"},
}}
res, _ := cascade.RerankWithResult(ctx, query, docsWithVecs)
```

This snippet lives ONLY in MathReranker rustdoc comment — NO code change in Cascade or Client wiring.

### Verification

```
cd /tmp/gokit-g5
go build ./...
go vet ./...
gofmt -l rerank/
go test ./rerank/... -count=1 -race -short                   # green, including TestV1ApiUnchanged 9/9
go test -bench=. -run=^$ -benchmem ./rerank/                 # MMR alloc proportional to N (not N²)
git diff origin/main -- go.mod                               # NO new external deps
```

### Out of scope for G5

- **MemDB integration** — caller decides in M13; this stream is library-only
- **Async / parallel MMR** — sequential greedy is O(n²) in CPU but minimal alloc; not worth complexity for n<1000
- **Approximate cosine** (FAISS, HNSW) — for n<1000 raw cosine is faster than index overhead
- **Hybrid blending** with NN scores — defer to M13 G7
- **Cross-language MMR variants** (cluster-MMR, sub-modular) — research-grade, M13+

---

## Out of scope (M13+)

- **Late chunking**: long docs (>512 token) lose needle to head-truncation. Slide-window + max-pool. ~3-day stream. Defer to M13.
- **G6 — Listwise rerank** (RankZephyr / monoT5 listwise): different transport (LLM-based), different latency profile. Add via `Reranker` interface, separate impl. M13.
- **G7 — Hybrid blending** (`α * embed_cos + (1-α) * sigmoid(rerank)`): requires `Doc.EmbedScore` field; useful for noise-robustness on weak rerankers. M13.
- **G8 — Asymmetric rerank**: different model for symmetric vs "find docs that ANSWER" queries. M13.
- **G9 — Confidence calibration** (Platt / isotonic): needs labeled validation set. No infra yet. M13+.
- **MMR diversity** — explicitly NOT a go-kit/rerank concern (wrong layer; needs embed-vectors not rerank scores). Stays in MemDB `internal/search/rerank/mmr.go`. Cleanup `Noted` for M13.

---

## Backlog observations (M11.1 / M12.4 cleanup, surfaced during plan review)

- `Noted`: M11 staged.go prompts (stage2 / stage3) should move to a `prompts/` subdir for editable-without-recompile (currently const string literals)
- `Noted`: `mmr.go` in MemDB internal/search/rerank/ — keep here (correct layer) but document its independence from go-kit/rerank
- `Noted`: `cross_encoder_adapter.go` precompute lookup uses `properties.ce_score_topk` JSON — score normalization (G2) requires schema migration to store normalized scores; defer to M13
- `Noted`: `EMBED_MODELS` and `RERANKER_MODELS` env vars in embed-server CLAUDE.md — current prod has only `gte-multi-rerank` (per CLAUDE.md), but README example shows `bge-reranker-v2-m3`. Verify which is actually loaded BEFORE any G2 ablation (instruction prefix decision depends on this)

---

End of plan.
