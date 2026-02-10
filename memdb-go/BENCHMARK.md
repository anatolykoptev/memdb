# memdb-go Benchmark Results

## Setup

- **Go gateway**: memdb-go on `:8080` (reverse proxy to Python)
- **Python backend**: memos-api (FastAPI/Uvicorn) on `:8000`
- **Load tool**: [vegeta](https://github.com/tsenart/vegeta)

## Running Tests

```bash
export SERVICE_SECRET=your-secret

# Single scenario
cd memdb-go/loadtest
./run.sh health -r 50 -d 30s

# Compare Go gateway vs Python direct
./run.sh compare -r 20 -d 30s

# Rate limit burst test
./run.sh ratelimit
```

## Scenarios

| Scenario | Endpoint | Rate | Duration | Notes |
|----------|----------|------|----------|-------|
| health | `GET /health` | 10 | 30s | Baseline proxy latency |
| search | `POST /product/search` | 10 | 30s | Main workload (embedding + DB) |
| add | `POST /product/add` | 5 | 30s | Fast mode (no LLM) |
| stream | `POST /product/chat/stream` | 2 | 10s | SSE streaming |
| ratelimit | `GET /health` | 200 | 1s | Verify 429 after burst |
| compare | health + search | 10 | 30s | Go vs Python side-by-side |

## Results

> Run `./run.sh compare` and paste results below.

### Health Endpoint (baseline proxy overhead)

| Metric | Go Gateway (:8080) | Python Direct (:8000) |
|--------|--------------------|-----------------------|
| p50 | | |
| p95 | | |
| p99 | | |
| Throughput | | |
| Errors | | |

### Search Endpoint (real workload)

| Metric | Go Gateway (:8080) | Python Direct (:8000) |
|--------|--------------------|-----------------------|
| p50 | | |
| p95 | | |
| p99 | | |
| Throughput | | |
| Errors | | |

### Rate Limiting

| Metric | Value |
|--------|-------|
| Burst (configured) | |
| Total requests | 200 |
| 200 OK | |
| 429 Too Many Requests | |

## Notes

- Health endpoint measures pure proxy overhead (Go HTTP stack + reverse proxy)
- Search endpoint includes embedding generation + PolarDB query (dominated by Python processing time)
- SSE streaming tested at low rate due to long-lived connections
- Rate limiting uses per-IP token bucket via `golang.org/x/time/rate`
