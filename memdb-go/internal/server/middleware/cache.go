package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/cache"
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
		ttl:    30 * time.Second,
		keyFn:  func(_ *http.Request, body []byte) string { return cache.SearchCacheKey(body) },
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

			// Respect Cache-Control: no-cache
			if r.Header.Get("Cache-Control") == "no-cache" {
				w.Header().Set("X-Cache", "BYPASS")
				next.ServeHTTP(w, r)
				return
			}

			// Read body for POST cache key generation
			var body []byte
			if rule.isPost {
				var err error
				body, err = io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil {
					next.ServeHTTP(w, r)
					return
				}
				// Restore body for downstream handlers
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}

			cacheKey := rule.keyFn(r, body)
			if cacheKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Try cache hit
			cached, err := cfg.Client.Get(r.Context(), cacheKey)
			if err != nil {
				logger.Debug("cache get error", slog.Any("error", err))
			}
			if cached != nil {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(http.StatusOK)
				w.Write(cached)
				return
			}

			// Cache miss — capture response
			w.Header().Set("X-Cache", "MISS")
			rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
			next.ServeHTTP(rec, r)

			// Only cache successful responses
			if rec.statusCode == http.StatusOK {
				if err := cfg.Client.Set(r.Context(), cacheKey, rec.body.Bytes(), rule.ttl); err != nil {
					logger.Debug("cache set error", slog.Any("error", err))
				}
			}
		})
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
