# memdb-go Benchmark Results

## Setup

- **Go gateway**: memdb-go on `:8080` (native handlers + reverse proxy)
- **Python backend**: memos-api (FastAPI/Uvicorn) on `:8000`
- **Load tool**: [vegeta](https://github.com/tsenart/vegeta)
- **Hardware**: Oracle Cloud VM (ARM64), PolarDB (PostgreSQL + Apache AGE)
- **Date**: 2026-02-09

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
| search | `POST /product/search` | 10 | 30s | Proxied (embedding + DB) |
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

### Search Endpoint (proxied — measures proxy overhead)

5 req/s, 10s duration, 50 requests

| Metric | Go Gateway (:8080) | Python Direct (:8000) | Overhead |
|--------|--------------------|-----------------------|----------|
| p50 | 7.84ms | 7.21ms | +0.63ms |
| p95 | 8.51ms | 7.78ms | +0.73ms |
| p99 | 37.6ms | 8.22ms | outlier |
| Success | 100% | 100% | - |

### Scalability (Go gateway at higher load)

| Endpoint | Rate | p50 | p95 | Success |
|----------|------|-----|-----|---------|
| health | 50/s | 0.56ms | 0.70ms | 100% |
| users | 50/s | 9.67s | 18.0s | 100% |
| get_all | 20/s | 30s (timeout) | 30s | 42% |
| search | 20/s | 7.95ms | 9.81ms | 100% |

## Key Findings

1. **Native handlers are dramatically faster**: get_all is 70x faster than Python (363ms vs 25.6s)
2. **Proxy overhead is minimal**: ~0.6ms added latency for proxied endpoints
3. **Connection pool saturation**: At 50 req/s, DB queries queue behind 20-connection pool limit. Users endpoint degrades from 186ms to 9.7s. Consider increasing pool size or adding connection-aware load shedding.
4. **Large responses**: get_all returns ~1.7MB per request (full JSONB properties including embeddings). Stripping embedding vectors from responses would reduce payload 10x and improve throughput.
5. **Health endpoint**: Go is 3.2x faster than Python for the simplest endpoint (pure HTTP overhead comparison)

## Notes

- Native endpoints bypass Python entirely: Go -> PolarDB direct
- Proxied endpoints (search, add, chat): Go -> Python -> PolarDB (Go adds ~0.6ms)
- PolarDB query latency for DISTINCT scan: ~186ms (cold), scales poorly under connection contention
- Rate limiting uses per-IP token bucket via `golang.org/x/time/rate`
- Service secret bypasses both auth and rate limiting for internal calls
