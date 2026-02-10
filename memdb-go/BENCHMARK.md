# memdb-go Benchmark Results

## Setup

- **Go gateway**: memdb-go on `:8080` (native handlers + reverse proxy)
- **Python backend**: memos-api (FastAPI/Uvicorn) on `:8000`
- **Load tool**: [vegeta](https://github.com/tsenart/vegeta)
- **Hardware**: Oracle Cloud VM (ARM64), PolarDB (PostgreSQL + Apache AGE)
- **Date**: 2026-02-09 (Phase 2), 2026-02-10 (Phase 3 search)

## Running Tests

```bash
export SERVICE_SECRET=your-secret

# Single scenario
cd memdb-go/loadtest
./run.sh health -r 50 -d 10s

# Compare Go gateway vs Python direct
./run.sh compare -r 5 -d 10s

# Rate limit burst test
./run.sh ratelimit
```

## Scenarios

| Scenario | Endpoint | Default Rate | Duration | Notes |
|----------|----------|------|----------|-------|
| health | `GET /health` | 10 | 30s | Native Go (no DB) |
| search | `POST /product/search` | 10 | 30s | **Native Go** (Phase 3: VoyageAI + PolarDB + Qdrant) |
| get_all | `POST /product/get_all` | 10 | 30s | Native Go (PolarDB direct) |
| users | `GET /product/users` | 10 | 30s | Native Go (PolarDB direct) |
| add | `POST /product/add` | 5 | 30s | Proxied (LLM + embedding) |
| stream | `POST /product/chat/stream` | 2 | 10s | Proxied (SSE streaming) |
| ratelimit | `GET /health` | 200 | 1s | Verify 429 after burst |
| compare | all above | 5-50 | 10-30s | Go vs Python side-by-side |

## Results

### Health Endpoint (native Go, no DB)

50 req/s, 10s duration, 500 requests

| Metric | Go Gateway (:8080) | Python Direct (:8000) | Speedup |
|--------|--------------------|-----------------------|---------|
| p50 | 0.56ms | 1.80ms | **3.2x** |
| p95 | 0.70ms | 2.08ms | 3.0x |
| p99 | 1.12ms | 2.64ms | 2.4x |
| Max | 3.40ms | 3.08ms | - |
| Success | 100% | 100% | - |

### Get All Memories (native Go vs Python ORM)

5 req/s, 10s duration, 50 requests (page_size=20)

| Metric | Go Gateway (:8080) | Python Direct (:8000) | Speedup |
|--------|--------------------|-----------------------|---------|
| p50 | 363ms | 25,581ms | **70x** |
| p95 | 381ms | 30,001ms (timeout) | - |
| p99 | 406ms | 30,001ms (timeout) | - |
| Mean response | 1.76MB | 1.57MB | - |
| Success | 100% | 54% | - |

### List Users (native Go vs Python N/A)

5 req/s, 10s duration, 50 requests

| Metric | Go Gateway (:8080) | Python Direct (:8000) |
|--------|--------------------|-----------------------|
| p50 | 186ms | N/A (404) |
| p95 | 191ms | N/A (404) |
| Success | 100% | 0% (endpoint missing) |

### Search Endpoint — Phase 2 (proxied, measures proxy overhead)

5 req/s, 10s duration, 50 requests — **before native search (2026-02-09)**

| Metric | Go Gateway (:8080) | Python Direct (:8000) | Overhead |
|--------|--------------------|-----------------------|----------|
| p50 | 7.84ms | 7.21ms | +0.63ms |
| p95 | 8.51ms | 7.78ms | +0.73ms |
| p99 | 37.6ms | 8.22ms | outlier |
| Success | 100% | 100% | - |

### Search Endpoint — Phase 3 (native Go, 2026-02-10)

Native search pipeline: VoyageAI embed (~200ms) → parallel errgroup{PolarDB vector, PolarDB fulltext, Qdrant preferences} → merge → MMR dedup → format response.

**Latency under load (vegeta, 30s duration, dedup=mmr, top_k=6)**

| Rate | Requests | p50 | p90 | p95 | p99 | max | Success |
|------|----------|-----|-----|-----|-----|-----|---------|
| 5/s | 150 | **1.8ms** | 2.4ms | 2.9ms | 424ms | 427ms | 100% |
| 10/s | 300 | **1.7ms** | 2.3ms | 2.8ms | 469ms | 487ms | 100% |
| 20/s | 300 | **1.7ms** | 8.7ms | 58ms | 1.25s | 1.4s | 100% |

**Cache behavior:**
- Cached (Redis, 30s TTL): ~1.7ms p50 (dominates at all rates — same query in target file)
- Uncached (first request / unique query): 347-463ms (VoyageAI embed ~200ms + parallel DB ~100ms)
- Mean response size: **392KB** (full memory results with metadata)

**Uncached latency (unique queries, no cache hits)**

| Run | Latency | Notes |
|-----|---------|-------|
| 1 | 348ms | English query |
| 2 | 355ms | English query |
| 3 | 349ms | English query |
| 4 | 464ms | Russian query (longer tokenization) |
| 5 | 369ms | English multi-topic query |

**vs Python (same query, same parameters):**

Python returns 0 results at ~7ms for the same queries (fast-mode goal parser drops complex queries). Go native search bypasses the goal parser and queries DB directly, finding 5-6 text + 1-3 skill + 6 preference memories.

### Scalability (Go gateway at higher load)

| Endpoint | Rate | p50 | p95 | Success |
|----------|------|-----|-----|---------|
| health | 50/s | 0.56ms | 0.70ms | 100% |
| users | 50/s | 9.67s | 18.0s | 100% |
| get_all | 20/s | 30s (timeout) | 30s | 42% |
| **search** | **20/s** | **1.7ms** | **58ms** | **100%** |

## Key Findings

1. **Native handlers are dramatically faster**: get_all is 70x faster than Python (363ms vs 25.6s)
2. **Native search hits target latency**: 347-463ms uncached (target was 350-500ms), ~1.7ms cached
3. **Native search finds more results**: Go returns 5-6 text + 1-3 skill + 6 pref memories; Python returns 0 for the same queries (goal parser limitation)
4. **Cache is critical**: Redis 30s TTL keeps p50 at ~1.7ms under sustained load. At 20 rps, 100% success rate
5. **Proxy overhead is minimal**: ~0.6ms added latency for proxied endpoints
6. **Connection pool saturation**: At 50 req/s, DB queries queue behind 20-connection pool limit. Users endpoint degrades from 186ms to 9.7s. Cache-aside resolves this for read-heavy workloads.
7. **VoyageAI is the bottleneck**: Uncached search spends ~200ms of 350ms on the embedding API call. At 20 rps tail latency grows to 1.25s p99 from API contention.

## Notes

- **15 of 33 routes native** (45%) as of Phase 3 — search, get_all, delete, users, instances, config
- **18 routes proxied**: add, chat/*, feedback, suggestions, scheduler (need LLM/embedding pipeline)
- Native search pipeline: VoyageAI embed → parallel errgroup{PolarDB vector, PolarDB fulltext, Qdrant pref×2} → merge → dedup → format
- PolarDB query latency for DISTINCT scan: ~186ms (cold), scales poorly under connection contention
- Rate limiting uses per-IP token bucket via `golang.org/x/time/rate`
- Service secret bypasses both auth and rate limiting for internal calls
- Search cache key: `memdb:db:search:{user_id}:{sha256(query)[:8]}:{top_k}:{dedup}` with 30s TTL
