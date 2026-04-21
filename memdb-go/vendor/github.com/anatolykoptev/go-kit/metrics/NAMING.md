# Metric Naming Conventions

Project-wide standard для всех go-* сервисов. Следуй при добавлении новой метрики.

## Regex

`[a-zA-Z_:][a-zA-Z0-9_:]*` — требование prometheus. `.` недопустим (`api.latency` ломает prom-backend).

## Required suffixes

| Тип | Суффикс | Примеры |
|-----|---------|---------|
| Counter (monotonic) | `_total` | `llm_calls_total`, `wp_rest_errors_total` |
| Histogram/Timer | `_seconds` или `_bytes` / `_ratio` | `request_duration_seconds`, `response_size_bytes` |
| Gauge | базовая единица | `queue_depth`, `memory_resident_bytes`, `connections_active` |
| Metadata | `_info` | `build_info`, `process_info` |

Base units: **seconds**, **bytes**. НЕ `_ms`, НЕ `_kb`, НЕ `_mb` — prometheus конвертирует сам.

## Colons reserved

Colon `:` в имени — ТОЛЬКО для recording rules / aggregated metrics (`instance:cpu:rate5m`). Для per-request лейблов ИСПОЛЬЗУЙ `metrics.Label(name, kvs...)`.

**Антипаттерн** (cardinality bomb + невалидный parse):

    m.reg.Incr("api:" + endpoint + ":success") // ❌

**Правильно**:

    m.reg.Incr(metrics.Label("api_requests_total",
        "endpoint", endpoint,
        "status", "success")) // ✓

## Namespace prefix

Каждый сервис — свой namespace через `NewPrometheusRegistry("<svc>")`:

- go-wp → `gowp_*`
- go-nerv → `gonerv_*`
- go-engine (lib) → наследует namespace consumer-а
- go-kit/fileopt → `gokit_fileopt_*`

## Cardinality

НЕ использовать лейблы с неограниченным набором значений:

- ❌ `user_id`, UUID, email, raw URL, client IP, timestamp
- ✓ HTTP method, route pattern, status code class (2xx/4xx/5xx), tenant при разумном N

## Nil safety

`*Registry` безопасен для nil — не пиши `if reg != nil { reg.Incr(...) }`. Nullable зависимости просто не инициализируются и все методы становятся no-op.

## Standardised helpers

Вместо ручных call/error пар — `metrics.TrackCall(reg, callName, errName, fn)`.

Для HTTP — `metrics/httpmw.Middleware(reg, "http")`.
Для MCP — `metrics/mcpmw.Middleware(reg, "tool")`.

## Refactor cheatsheet (текущий парк)

| Файл | До | После |
|------|-----|-------|
| `go-hully/internal/metrics/metrics.go` | `api:$endpoint:success` | `Label("api_requests_total","endpoint",e,"status","success")` |
| `go-wp/internal/wptools/shared/rest.go` | `Incr("wp_rest_calls")` | `Incr("wp_rest_calls_total")` |
| `go-engine/llm/client.go` | manual call+err incr pair | `TrackCall(reg, "llm_calls_total", "llm_errors_total", fn)` |
| legacy `api.latency.count` | `.` separator | `api_latency_seconds` (prom histogram) |
