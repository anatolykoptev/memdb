package stealth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ByparrConfig configures the Byparr/FlareSolverr solver.
type ByparrConfig struct {
	// BaseURL is the solver endpoint (e.g. "http://127.0.0.1:8191").
	BaseURL string

	// Timeout for solve requests. Default: 60s.
	Timeout time.Duration

	// CacheTTL for solved cookies. Default: 25 minutes (cf_clearance lifetime).
	CacheTTL time.Duration
}

func (c *ByparrConfig) defaults() {
	if c.Timeout == 0 {
		c.Timeout = 60 * time.Second
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 25 * time.Minute
	}
}

// ByparrSolver implements CookieProvider by calling a FlareSolverr-compatible API.
// Caches solved cookies per domain with TTL.
type ByparrSolver struct {
	baseURL string
	client  *http.Client
	ttl     time.Duration

	mu    sync.RWMutex
	cache map[string]cachedCookie
}

type cachedCookie struct {
	cookie    string
	expiresAt time.Time
}

// NewByparrSolver creates a CookieProvider that calls Byparr/FlareSolverr.
func NewByparrSolver(cfg ByparrConfig) *ByparrSolver {
	cfg.defaults()
	return &ByparrSolver{
		baseURL: cfg.BaseURL,
		client:  &http.Client{Timeout: cfg.Timeout},
		ttl:     cfg.CacheTTL,
		cache:   make(map[string]cachedCookie),
	}
}

// GetCookie returns a cached cf_clearance cookie for the domain.
func (s *ByparrSolver) GetCookie(domain string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.cache[domain]
	if !ok || time.Now().After(entry.expiresAt) {
		return ""
	}
	return entry.cookie
}

// Solve calls the Byparr/FlareSolverr API to solve a Cloudflare challenge.
func (s *ByparrSolver) Solve(domain string, challenge *CloudflareError) (string, error) {
	if challenge != nil && challenge.Type == ChallengeBlock {
		return "", fmt.Errorf("block challenges are not solvable")
	}

	url := fmt.Sprintf("https://%s", domain)
	cookie, err := s.solveViaAPI(url)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.cache[domain] = cachedCookie{
		cookie:    cookie,
		expiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Unlock()

	return cookie, nil
}

// flareSolverr request/response types.
type solverRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type solverResponse struct {
	Status   string          `json:"status"`
	Message  string          `json:"message"`
	Solution *solverSolution `json:"solution"`
}

type solverSolution struct {
	URL       string         `json:"url"`
	Cookies   []solverCookie `json:"cookies"`
	UserAgent string         `json:"userAgent"`
}

type solverCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (s *ByparrSolver) solveViaAPI(url string) (string, error) {
	body, err := json.Marshal(solverRequest{
		Cmd:        "request.get",
		URL:        url,
		MaxTimeout: 60000,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	resp, err := s.client.Post(s.baseURL+"/v1", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("solver request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("solver returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result solverResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if result.Status != "ok" {
		return "", fmt.Errorf("solver error: %s", result.Message)
	}

	if result.Solution == nil {
		return "", fmt.Errorf("solver returned no solution")
	}

	for _, c := range result.Solution.Cookies {
		if c.Name == "cf_clearance" {
			return fmt.Sprintf("cf_clearance=%s", c.Value), nil
		}
	}

	return "", fmt.Errorf("cf_clearance cookie not found in solver response")
}
