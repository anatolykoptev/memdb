package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
)

// CacheConfig defines which paths to cache and their TTLs.
type CacheConfig struct {
	Client *cache.Client // nil = caching disabled
}

// cacheRule maps a route to its TTL and key generation.
type cacheRule struct {
	ttl    time.Duration
	keyFn  func(r *http.Request, body []byte) string
	isPost bool
}

var cacheRules = map[string]cacheRule{
	"GET /product/scheduler/allstatus": {
		ttl:   5 * time.Second,
		keyFn: func(r *http.Request, _ []byte) string { return cache.PathCacheKey(r.URL.Path) },
	},
	"GET /product/scheduler/task_queue_status": {
		ttl:   5 * time.Second,
		keyFn: func(r *http.Request, _ []byte) string { return cache.PathCacheKey(r.URL.Path) },
	},
	"POST /product/search": {
		ttl: 30 * time.Second,
		keyFn: func(_ *http.Request, body []byte) string {
			fields, err := cache.ParseSearchCacheKey(body)
			if err != nil {
				return ""
			}
			return cache.SearchCacheKey(fields)
		},
		isPost: true,
	},
}

// Cache returns middleware that caches responses for configured endpoints.
func Cache(logger *slog.Logger, cfg CacheConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if cfg.Client == nil {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			routeKey := r.Method + " " + r.URL.Path
			rule, ok := cacheRules[routeKey]
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			if r.Header.Get("Cache-Control") == "no-cache" {
				w.Header().Set("X-Cache", "BYPASS")
				next.ServeHTTP(w, r)
				return
			}

			body, ok := readPostBody(w, r, rule, next)
			if !ok {
				return // body read failed; response already sent via next
			}

			cacheKey := rule.keyFn(r, body)
			if cacheKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			if serveCacheHit(w, r, cfg.Client, cacheKey, logger) {
				return
			}

			captureAndCache(w, r, next, cfg.Client, cacheKey, rule.ttl, logger)
		})
	}
}

// readPostBody reads the request body for POST rules and restores it for downstream handlers.
// Returns (body, true) on success; on error it calls next and returns (nil, false).
func readPostBody(w http.ResponseWriter, r *http.Request, rule cacheRule, next http.Handler) ([]byte, bool) {
	if !rule.isPost {
		return nil, true
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		next.ServeHTTP(w, r)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return body, true
}

// serveCacheHit writes a cached response if available and returns true.
func serveCacheHit(w http.ResponseWriter, r *http.Request, client *cache.Client, cacheKey string, logger *slog.Logger) bool {
	cached, err := client.Get(r.Context(), cacheKey)
	if err != nil {
		logger.Debug("cache get error", slog.Any("error", err))
	}
	if cached == nil {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "HIT")
	w.WriteHeader(http.StatusOK)
	w.Write(cached) //nolint:errcheck // best-effort write
	return true
}

// captureAndCache runs the handler, captures the response, and stores successful responses.
func captureAndCache(w http.ResponseWriter, r *http.Request, next http.Handler, client *cache.Client, cacheKey string, ttl time.Duration, logger *slog.Logger) {
	w.Header().Set("X-Cache", "MISS")
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
	next.ServeHTTP(rec, r)
	if rec.statusCode == http.StatusOK {
		if err := client.Set(r.Context(), cacheKey, rec.body.Bytes(), ttl); err != nil {
			logger.Debug("cache set error", slog.Any("error", err))
		}
	}
}

// responseRecorder captures the response body for caching.
type responseRecorder struct {
	http.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
