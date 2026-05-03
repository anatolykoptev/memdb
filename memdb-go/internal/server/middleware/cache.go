package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
)

// routeLabel derives a short metric-friendly label from the raw routeKey
// (e.g. "POST /product/search" → "search"). Falls back to the full key.
func routeLabel(routeKey string) string {
	parts := strings.Split(routeKey, "/")
	if len(parts) == 0 {
		return routeKey
	}
	return parts[len(parts)-1]
}

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
			// Bypass cache for requests that take a fundamentally different
			// code path or hit external sources whose results change every
			// call. Returning empty string opts the request out of caching.
			if shouldBypassSearchCache(body) {
				return ""
			}
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

			label := routeLabel(routeKey)
			keyStart := time.Now()
			cacheKey := rule.keyFn(r, body)
			recordCacheKeyBuild(r.Context(), label, keyStart)
			if cacheKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			if serveCacheHit(w, r, cfg.Client, cacheKey, logger, label) {
				return
			}
			recordCacheMiss(r.Context(), label)

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
func serveCacheHit(w http.ResponseWriter, r *http.Request, client *cache.Client, cacheKey string, logger *slog.Logger, label string) bool {
	cached, err := client.Get(r.Context(), cacheKey)
	if err != nil {
		logger.DebugContext(r.Context(), "cache get error", slog.Any("error", err))
	}
	if cached == nil {
		return false
	}
	recordCacheHit(r.Context(), label)
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
			logger.DebugContext(r.Context(), "cache set error", slog.Any("error", err))
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

// shouldBypassSearchCache returns true for /product/search requests that must
// NEVER be served from cache. Two cases:
//   1. internet_search=true — content is fetched from external APIs, results
//      change between calls, caching would freeze stale fetched docs.
//   2. len(speakers)>=2 — handler takes the dual-speaker fan-out branch
//      (handleDualSpeakerSearch). Different code path produces a
//      request-shaped merged response that the v3 cache key only partially
//      covers; safer to bypass entirely.
//
// Implementation note: we do a minimal JSON unmarshal here rather than
// reusing ParseSearchCacheKey because that function does not (yet) parse
// internet_search / speakers and we want this gate to fire even on parse
// errors of those specific fields. If body is malformed we fall through
// to the keyFn which will also fail and bypass — same outcome.
func shouldBypassSearchCache(body []byte) bool {
	var m struct {
		InternetSearch *bool    `json:"internet_search"`
		Speakers       []string `json:"speakers"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return false // let downstream parser handle the malformed body
	}
	if m.InternetSearch != nil && *m.InternetSearch {
		return true
	}
	if len(m.Speakers) >= 2 {
		return true
	}
	return false
}
