package stealth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// OxBrowserSolverConfig configures the ox-browser CF solver.
type OxBrowserSolverConfig struct {
	// BaseURL of ox-browser (e.g. "http://127.0.0.1:8901").
	BaseURL string

	// CacheTTL for solved cookies. Default: 25 minutes.
	CacheTTL time.Duration

	// ProxyFn, if non-nil, routes requests to ox-browser through a proxy.
	// Compatible with http.Transport.Proxy — pass proxyPool.TransportProxy().
	ProxyFn func(*http.Request) (*url.URL, error)
}

// OxBrowserSolver implements CookieProvider using ox-browser /solve.
// Drop-in replacement for ByparrSolver.
type OxBrowserSolver struct {
	client *OxBrowserClient
	ttl    time.Duration

	mu    sync.RWMutex
	cache map[string]cachedCookie
}

// NewOxBrowserSolver creates a CookieProvider backed by ox-browser.
func NewOxBrowserSolver(cfg OxBrowserSolverConfig) *OxBrowserSolver {
	ttl := cfg.CacheTTL
	if ttl == 0 {
		ttl = 25 * time.Minute
	}
	var oxClient *OxBrowserClient
	if cfg.ProxyFn != nil {
		oxClient = NewOxBrowserClientWithProxy(cfg.BaseURL, cfg.ProxyFn)
	} else {
		oxClient = NewOxBrowserClient(cfg.BaseURL)
	}
	return &OxBrowserSolver{
		client: oxClient,
		ttl:    ttl,
		cache:  make(map[string]cachedCookie),
	}
}

// GetCookie returns a cached cf_clearance cookie for the domain.
func (s *OxBrowserSolver) GetCookie(domain string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.cache[domain]
	if !ok || time.Now().After(entry.expiresAt) {
		return ""
	}
	return entry.cookie
}

// Solve calls ox-browser /solve to obtain CF clearance cookies.
func (s *OxBrowserSolver) Solve(domain string, challenge *CloudflareError) (string, error) {
	if challenge != nil && challenge.Type == ChallengeBlock {
		return "", fmt.Errorf("block challenges are not solvable")
	}

	challengeType := "js_challenge"
	if challenge != nil {
		challengeType = string(challenge.Type)
	}

	url := fmt.Sprintf("https://%s", domain)
	cookies, err := s.client.Solve(context.Background(), url, challengeType)
	if err != nil {
		return "", err
	}

	clearance, ok := cookies["cf_clearance"]
	if !ok {
		return "", fmt.Errorf("cf_clearance not found in ox-browser response")
	}

	cookie := fmt.Sprintf("cf_clearance=%s", clearance)

	s.mu.Lock()
	s.cache[domain] = cachedCookie{
		cookie:    cookie,
		expiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Unlock()

	return cookie, nil
}
