package ratelimit

import (
	"context"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DomainConfig defines rate limit rules for a specific domain pattern.
type DomainConfig struct {
	// Domain is the domain pattern to match. Supports exact match ("api.twitter.com")
	// and suffix match ("*.twitter.com"). Empty string matches all domains.
	Domain string

	// RequestsPerWindow limits the number of requests within WindowDuration.
	RequestsPerWindow int

	// WindowDuration is the sliding window length.
	WindowDuration time.Duration

	// MinDelay is the minimum delay between consecutive requests to this domain.
	// Zero means no delay. Inspired by colly's LimitRule.Delay.
	MinDelay time.Duration

	// RandomDelay adds a random delay of [0, RandomDelay) on top of MinDelay.
	// Inspired by geziyor's 0.5x-1.5x randomization pattern.
	RandomDelay time.Duration
}

// DomainLimiter applies per-domain rate limiting with configurable delay.
type DomainLimiter struct {
	mu       sync.RWMutex
	rules    []DomainConfig
	limiters map[string]*Limiter
	lastReq  map[string]time.Time // last request time per domain
}

// NewDomainLimiter creates a limiter with domain-specific rate limits.
// Rules are checked in order; the first matching rule is used.
func NewDomainLimiter(rules ...DomainConfig) *DomainLimiter {
	dl := &DomainLimiter{
		rules:    rules,
		limiters: make(map[string]*Limiter),
		lastReq:  make(map[string]time.Time),
	}
	return dl
}

// Allow checks if a request to the given URL is allowed.
// Returns true and updates internal state if allowed.
func (dl *DomainLimiter) Allow(rawURL string) bool {
	domain := extractDomain(rawURL)
	rule := dl.matchRule(domain)
	if rule == nil {
		return true
	}

	dl.mu.Lock()
	defer dl.mu.Unlock()

	// Check min delay (+ optional random delay)
	if rule.MinDelay > 0 || rule.RandomDelay > 0 {
		if last, ok := dl.lastReq[domain]; ok {
			delay := rule.MinDelay
			if rule.RandomDelay > 0 {
				delay += time.Duration(rand.Int64N(int64(rule.RandomDelay)))
			}
			if time.Since(last) < delay {
				return false
			}
		}
	}

	// Check rate limit
	limiter := dl.getLimiter(domain, rule)
	if !limiter.Allow(domain) {
		return false
	}

	dl.lastReq[domain] = time.Now()
	return true
}

// Wait blocks until a request to the given URL is allowed, or ctx is cancelled.
func (dl *DomainLimiter) Wait(ctx context.Context, rawURL string) error {
	for {
		if dl.Allow(rawURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// MarkRateLimited marks a domain as rate-limited until the given time.
func (dl *DomainLimiter) MarkRateLimited(rawURL string, until time.Time) {
	domain := extractDomain(rawURL)
	rule := dl.matchRule(domain)
	if rule == nil {
		return
	}

	dl.mu.Lock()
	limiter := dl.getLimiter(domain, rule)
	dl.mu.Unlock()

	limiter.MarkRateLimited(domain, until)
}

func (dl *DomainLimiter) matchRule(domain string) *DomainConfig {
	for i := range dl.rules {
		r := &dl.rules[i]
		if r.Domain == "" {
			return r // wildcard, matches everything
		}
		if r.Domain == domain {
			return r
		}
		if strings.HasPrefix(r.Domain, "*.") {
			suffix := r.Domain[1:] // ".twitter.com"
			if strings.HasSuffix(domain, suffix) || domain == r.Domain[2:] {
				return r
			}
		}
	}
	return nil
}

func (dl *DomainLimiter) getLimiter(domain string, rule *DomainConfig) *Limiter {
	l, ok := dl.limiters[domain]
	if !ok {
		l = NewLimiter(Config{
			RequestsPerWindow: rule.RequestsPerWindow,
			WindowDuration:    rule.WindowDuration,
		})
		dl.limiters[domain] = l
	}
	return l
}

func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if host == "" {
		return rawURL
	}
	return strings.ToLower(host)
}
