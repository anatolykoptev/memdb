package middleware

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	Enabled       bool
	RPS           float64 // Sustained requests per second
	Burst         int     // Maximum burst size
	ServiceSecret string  // Requests with valid service secret bypass rate limiting
}

// RateLimit returns middleware that enforces per-IP token bucket rate limiting.
// Requests with a valid X-Service-Secret header bypass the rate limiter.
func RateLimit(logger *slog.Logger, cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !cfg.Enabled {
			return next
		}

		store := newIPStore(cfg.RPS, cfg.Burst)

		// Periodically clean up stale entries
		go store.cleanup(5 * time.Minute)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting for health endpoints
			if r.URL.Path == "/health" || r.URL.Path == "/ready" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip for service secret
			if cfg.ServiceSecret != "" {
				if secret := r.Header.Get("X-Service-Secret"); secret == cfg.ServiceSecret {
					next.ServeHTTP(w, r)
					return
				}
			}

			ip := extractIP(r)
			limiter := store.get(ip)

			if !limiter.Allow() {
				retryAfter := math.Ceil(1.0 / cfg.RPS)
				w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"code":429,"message":"rate limit exceeded","data":null}`)

				logger.Warn("rate limit exceeded",
					slog.String("ip", ip),
					slog.String("path", r.URL.Path),
				)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ipStore manages per-IP rate limiters.
type ipStore struct {
	mu       sync.Mutex
	limiters map[string]*ipEntry
	rps      float64
	burst    int
}

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPStore(rps float64, burst int) *ipStore {
	return &ipStore{
		limiters: make(map[string]*ipEntry),
		rps:      rps,
		burst:    burst,
	}
}

func (s *ipStore) get(ip string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.limiters[ip]
	if !exists {
		limiter := rate.NewLimiter(rate.Limit(s.rps), s.burst)
		s.limiters[ip] = &ipEntry{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// cleanup removes entries not seen for the given duration.
func (s *ipStore) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		for ip, entry := range s.limiters {
			if time.Since(entry.lastSeen) > 10*time.Minute {
				delete(s.limiters, ip)
			}
		}
		s.mu.Unlock()
	}
}

// extractIP gets the client IP from X-Forwarded-For, X-Real-IP, or RemoteAddr.
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (client IP)
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
