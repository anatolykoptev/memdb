# go-stealth

[![Go 1.26+](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Generic anti-ban toolkit for Go — TLS fingerprinting, proxy rotation, rate limiting, middleware, session management, and a generic pool with health tracking.

**Not a scraping framework** — a reusable HTTP layer that makes any Go HTTP client look like a real browser.

## Features

- **TLS Fingerprinting** — 18 browser profiles (Chrome, Firefox, Safari, Edge) across 5 OS via [tls-client](https://github.com/bogdanfinn/tls-client)
- **Proxy Rotation** — static list or [Webshare](https://www.webshare.io/) API, per-proxy health tracking with auto-skip
- **Rate Limiting** — per-key sliding window + per-domain limiter with wildcard matching
- **Middleware** — composable Handler/Middleware/Chain pattern (logging, retry, rate limit, client hints)
- **Retry & Backoff** — exponential backoff with jitter, retryable error detection, generic `RetryDo[T]()`
- **Generic Pool** — `Pool[T Identity]` with round-robin, health tracking, soft/permanent deactivation, cooldown
- **Sessions** — fixed profile + cookie jar, request counting, file-based persistence
- **`http.RoundTripper`** — drop-in replacement for `http.DefaultTransport`

## Install

```bash
go get github.com/anatolykoptev/go-stealth
```

## Quick Start

```go
client, _ := stealth.NewClient(
    stealth.WithProfile(stealth.RandomProfile()),
)
body, headers, status, err := client.Do("GET", "https://example.com", nil, nil)
```

### With Proxy Pool

```go
pool, _ := proxypool.NewWebshare(os.Getenv("WEBSHARE_API_KEY"))
client, _ := stealth.NewClient(
    stealth.WithProxyPool(pool),
    stealth.WithRetryOnBlock(2),
)
```

### Country Targeting

By default `NewWebshare` targets the US. Use `WebshareConfig` or the rotating constructor for finer control.

```go
// 1. Backbone with default US (existing call — unchanged)
pool, _ := proxypool.NewWebshare(apiKey)

// 2. Multi-country via API filter + username injection
pool, _ := proxypool.NewWebshareWithConfig(apiKey, proxypool.WebshareConfig{
    Countries: []string{"US", "GB", "DE"},
})

// 3. Rotating endpoint — no API key needed, Webshare rotates IPs internally
pool, _ := proxypool.NewWebshareRotating(os.Getenv("WEBSHARE_USER"), os.Getenv("WEBSHARE_PASS"), "US")
```

The pool round-robins across all entries. With multiple countries, each base proxy is duplicated per country so rotations naturally spread across geographies.

### As http.RoundTripper

```go
client, _ := stealth.NewClient()
resp, err := client.StdClient().Get("https://example.com")
```

### Middleware

```go
client, _ := stealth.NewClient()
client.Use(stealth.LoggingMiddleware)
client.Use(stealth.RetryMiddleware(stealth.DefaultRetryConfig))
client.Use(stealth.RateLimitMiddleware(ratelimit.NewLimiter(ratelimit.DefaultConfig)))
```

### Rate Limiting

```go
limiter := ratelimit.NewDomainLimiter(ratelimit.DomainConfig{
    Rules: map[string]ratelimit.Config{
        "api.example.com": {RequestsPerWindow: 10, WindowDuration: time.Minute},
        "*.example.com":   {RequestsPerWindow: 30, WindowDuration: time.Minute},
    },
})
limiter.Wait(ctx, "https://api.example.com/v1/users")
```

### Generic Pool

```go
pool := pool.New(accounts, pool.Config{
    AlertHook: func(topic string, payload any) { log.Println(topic, payload) },
})
acc, err := pool.Next(func(a *Account) bool { return a.IsReady() })
```

## Packages

| Package | Purpose |
|---------|---------|
| `stealth` | BrowserClient, middleware, profiles, retry, backoff |
| `pool` | Generic `Pool[T Identity]` with health tracking |
| `proxypool` | ProxyPool interface + Static, Webshare, HealthyProxyPool |
| `ratelimit` | Per-key sliding window + per-domain limiter |
| `session` | Stateful browsing with persistence |

## Used By

- [go-twitter](https://github.com/anatolykoptev/go-twitter) — Twitter/X scraping
- [go-threads](https://github.com/anatolykoptev/go-threads) — Threads.net scraping
- [go-search](https://github.com/anatolykoptev/go-search) — Web search MCP server
- [go-hully](https://github.com/anatolykoptev/go-hully) — Crypto intelligence

## License

MIT
