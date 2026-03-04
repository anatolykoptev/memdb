package stealth

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-stealth/ratelimit"
)

// LoggingMiddleware logs each request's method, URL, status, and latency via slog.
func LoggingMiddleware(next Handler) Handler {
	return func(req *Request) (*Response, error) {
		start := time.Now()
		resp, err := next(req)
		latency := time.Since(start)

		if err != nil {
			slog.Debug("http request failed",
				slog.String("method", req.Method),
				slog.String("url", req.URL),
				slog.Duration("latency", latency),
				slog.Any("error", err),
			)
			return resp, err
		}

		slog.Debug("http request",
			slog.String("method", req.Method),
			slog.String("url", req.URL),
			slog.Int("status", resp.StatusCode),
			slog.Duration("latency", latency),
		)
		return resp, nil
	}
}

// RetryMiddleware returns a middleware that retries failed requests using the given config.
func RetryMiddleware(cfg RetryConfig) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			return RetryDo(context.Background(), cfg, func() (*Response, error) {
				resp, err := next(req)
				if err != nil {
					return nil, err
				}
				if IsRetryableStatus(resp.StatusCode) {
					return nil, &HttpStatusError{StatusCode: resp.StatusCode}
				}
				return resp, nil
			})
		}
	}
}

// RetryMiddlewareWithContext returns a retry middleware that respects context cancellation.
func RetryMiddlewareWithContext(ctx context.Context, cfg RetryConfig) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			return RetryDo(ctx, cfg, func() (*Response, error) {
				resp, err := next(req)
				if err != nil {
					return nil, err
				}
				if IsRetryableStatus(resp.StatusCode) {
					return nil, &HttpStatusError{StatusCode: resp.StatusCode}
				}
				return resp, nil
			})
		}
	}
}

// RateLimitMiddleware returns a middleware that enforces per-domain rate limits.
// Blocks until the request is allowed or context deadline is exceeded.
func RateLimitMiddleware(limiter *ratelimit.DomainLimiter) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			ctx := context.Background()
			if err := limiter.Wait(ctx, req.URL); err != nil {
				return nil, err
			}
			resp, err := next(req)
			if err != nil {
				return resp, err
			}
			// Auto-parse Retry-After on 429
			if resp.StatusCode == 429 {
				if retryAfter := resp.Headers["retry-after"]; retryAfter != "" {
					limiter.MarkRateLimited(req.URL, time.Now().Add(parseRetryAfterValue(retryAfter)))
				}
			}
			return resp, err
		}
	}
}

// RateLimitMiddlewareWithContext returns a rate limit middleware that respects context cancellation.
func RateLimitMiddlewareWithContext(ctx context.Context, limiter *ratelimit.DomainLimiter) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			if err := limiter.Wait(ctx, req.URL); err != nil {
				return nil, err
			}
			resp, err := next(req)
			if err != nil {
				return resp, err
			}
			if resp.StatusCode == 429 {
				if retryAfter := resp.Headers["retry-after"]; retryAfter != "" {
					limiter.MarkRateLimited(req.URL, time.Now().Add(parseRetryAfterValue(retryAfter)))
				}
			}
			return resp, err
		}
	}
}

// ClientHintsMiddleware auto-injects sec-ch-ua-* headers for Chromium-based profiles.
// This ensures Client Hints match the User-Agent, preventing fingerprint mismatch detection.
func ClientHintsMiddleware(next Handler) Handler {
	return func(req *Request) (*Response, error) {
		if req.Headers == nil {
			return next(req)
		}
		ua := req.Headers["user-agent"]
		if ua == "" {
			ua = req.Headers["User-Agent"]
		}
		if ua == "" {
			return next(req)
		}

		hints := ClientHintsHeaders(ua)
		for k, v := range hints {
			// Don't override if consumer already set them
			if _, exists := req.Headers[k]; !exists {
				req.Headers[k] = v
			}
		}

		return next(req)
	}
}

// parseRetryAfterValue parses a Retry-After header value (seconds or HTTP-date).
func parseRetryAfterValue(val string) time.Duration {
	// Reuse logic from ParseRetryAfter but for a raw string value
	// Try seconds first
	var seconds int
	if _, err := time.ParseDuration(val + "s"); err == nil {
		// fallback
	}
	for _, c := range val {
		if c < '0' || c > '9' {
			seconds = -1
			break
		}
		seconds = seconds*10 + int(c-'0')
	}
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	// Try HTTP-date
	for _, layout := range []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC850,
		time.ANSIC,
	} {
		if t, err := time.Parse(layout, val); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
			return 0
		}
	}
	// Default: 30 second backoff if unparseable
	return 30 * time.Second
}
