# Stage 3 Memory Profile Playbook — 2026-04-26

## Context

M7 Stage 3 (2026-04-25) OOM-crashed memdb-go at conversation 41 (~21 min into a
10-conversation LoCoMo ingest + 1986-QA benchmark).  The container limit was 512 MiB.
`GOMEMLIMIT` was set to 400 MiB but the runtime still ballooned past the hard container
limit before the GC could reclaim — classic sign that allocation rate exceeded GC throughput.

M8 Stream 1 raises the container limit to 6 GiB and auto-wires `GOMEMLIMIT` to 80% of
whatever limit is detected.  This playbook documents how to capture a heap profile during
the next Stage 3 run (M8 Stream 6 / MEASURE) and what to look for.

## How to Capture a Heap Profile

```bash
# 1. Grab heap snapshot during ingest (while conversations are being ingested)
SERVICE_SECRET="$(grep INTERNAL_SERVICE_SECRET ~/deploy/krolik-server/.env | cut -d= -f2)"
curl -s -H "X-Service-Secret: $SERVICE_SECRET" \
     http://127.0.0.1:8080/debug/pprof/heap \
     > /tmp/heap-$(date +%Y%m%d-%H%M%S).pb.gz

# 2. Quick top-20 by cumulative allocation
go tool pprof -top -cum /tmp/heap-*.pb.gz | head -20

# 3. Interactive TUI (flame graph in browser)
go tool pprof -http=:6060 /tmp/heap-*.pb.gz
# then open http://localhost:6060/ui/flamegraph
```

Capture at least two snapshots: one mid-ingest (~conv 20) and one near the peak (~conv 35).
Diff them to see what accumulated between the two points.

```bash
# Diff two heap profiles
go tool pprof -top -cum -diff_base /tmp/heap-mid.pb.gz /tmp/heap-peak.pb.gz | head -20
```

## Pprof Route Auth

The `/debug/pprof/*` routes on memdb-go require the `X-Service-Secret` header
(registered in `memdb-go/internal/server/server_routes.go`, added in commit c6f2220).

## Known Hot Paths to Inspect

From code review (not yet profiled under Stage 3 load):

| Function | File | Why it may allocate |
|----------|------|---------------------|
| `nativeRawAddForCube` | `internal/handlers/add_raw.go:25` | Unbounded `nodes`/`items`/`embeddings` slice growth for large message batches |
| `processRawMemory` | `internal/handlers/add_raw.go:94` | Allocates `db.MemoryInsertNode` + `addResponseItem` per message |
| `buildRawNode` | `internal/handlers/add_raw.go:123` | `marshalProps` calls `json.Marshal` on a `map[string]any` every time |
| `marshalProps` / `buildNodeProps` | `internal/handlers/add.go` | `map[string]any` allocation per node |
| `InsertMemoryNodes` | `internal/db/postgres.go` | Bulk insert: builds SQL query string for N rows |

## What to Look For

### Scenario A — `runtime.makeslice` dominates

The `nodes`, `items`, and `embeddings` slices in `nativeRawAddForCube` grow one-element at
a time via `append`.  For a LoCoMo conversation with 200 messages this means ~200 slice
reallocations.

**Fix**: preallocate with `make([]T, 0, len(texts))` once `texts` length is known.

### Scenario B — `json.Marshal` / `encoding/json` dominates

`buildNodeProps` returns a `map[string]any` which is then passed to `json.Marshal`.
Each call allocates the map, its buckets, and the intermediate `[]byte` buffer.

**Fix**: switch to `encoding/json.Encoder` with a pre-allocated `bytes.Buffer` held in
a `sync.Pool`.  Alternative: use `jsoniter` or replace the property map with a typed struct
that has a `MarshalJSON` method.

### Scenario C — Embedding vector retention

Each conversation retains its full embedding matrix in the `embeddings [][]float32` slice
until `writeRawCache` is called.  For 200 messages × 1024 floats × 4 bytes = ~800 KiB per
conversation.

**Fix**: flush to the VSET cache incrementally (inside the per-message loop) instead of
accumulating the full matrix and flushing at the end.

## Action Thresholds

Evaluate after Stream 6 (MEASURE) runs the full Stage 3:

| Observation | Action |
|-------------|--------|
| `runtime.makeslice` is top-3 allocator in `nativeRawAddForCube` | Preallocate slices with capacity = `len(texts)` |
| `json.Marshal` > 20% of heap diff | Replace property map with typed struct + `sync.Pool` buffer |
| `embeddings [][]float32` accumulates > 100 MiB | Flush to cache inside the per-message loop, clear slice after each batch of N |
| Peak heap > 3 GiB with 6 GiB container limit | Reduce `MEMDB_ADD_WORKERS` from 4 → 2 to limit concurrency |

## Baseline Numbers (to be filled by Stream 6)

After Stream 6 completes, record:

| Metric | Value |
|--------|-------|
| Peak RSS at conv-41 | TODO |
| `GOMEMLIMIT` effective value | TODO |
| Top-3 cumulative allocators | TODO |
| Heap at mid-ingest (conv-20) | TODO |
| Heap at peak (conv-35) | TODO |

## Related Files

- `memdb-go/cmd/server/gomemlimit.go` — cgroup detection + `SetMemoryLimit` wiring
- `memdb-go/internal/handlers/add_raw.go` — raw-mode ingest pipeline
- `docs/perf/2026-04-25-m7-latency-report.md` — M7 latency baseline
