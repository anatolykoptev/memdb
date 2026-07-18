package stealth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

// DefaultHeaderOrder is a generic Chrome-like header order.
var DefaultHeaderOrder = []string{
	"accept",
	"accept-language",
	"accept-encoding",
	"referer",
	"cookie",
	"user-agent",
}

// BrowserClient wraps an HTTPDoer backend with middleware, proxy rotation,
// and TLS fingerprint impersonation.
type BrowserClient struct {
	doer         HTTPDoer
	headerOrder  []string
	proxyPool    ProxyPoolProvider // nil = no auto-rotation
	middlewares  []Middleware
	handler      Handler // lazy-built from middlewares + base handler
	debug        bool
	blockRetries int // extra retry attempts on 403/429 (requires proxyPool)

	// requestURLGuard is the pre-request (tier-3) SSRF check on the initial
	// target URL, evaluated before the (possibly proxied) fetch. nil = no
	// pre-request guard (WithoutSSRFGuard). This is the only tier that guards
	// a proxied fetch's initial target.
	requestURLGuard func(ctx context.Context, u *url.URL) error
}

// ProxyPoolProvider returns the next proxy URL for rotation.
type ProxyPoolProvider interface {
	Next() string
}

// NewClient creates a BrowserClient with the given options.
func NewClient(opts ...ClientOption) (*BrowserClient, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(cfg)
	}
	if len(cfg.buildErrors) > 0 {
		return nil, cfg.buildErrors[0]
	}

	backendCfg := BackendConfig{
		Profile:            cfg.profile,
		ProxyURL:           cfg.proxyURL,
		TimeoutSeconds:     cfg.timeout,
		FollowRedirects:    cfg.followRedirs,
		HTTP3:              cfg.http3,
		InsecureSkipVerify: cfg.insecureSkipVerify,
		DialControl:        cfg.dialControl,
		RedirectGuard:      cfg.redirectGuard,
	}

	factory := cfg.backend
	if factory == nil {
		factory = newTLSClientBackend
	}

	doer, err := factory(backendCfg)
	if err != nil {
		return nil, err
	}

	order := cfg.headerOrder
	if order == nil {
		order = DefaultHeaderOrder
	}

	bc := &BrowserClient{
		doer:            doer,
		headerOrder:     order,
		proxyPool:       cfg.proxyPool,
		debug:           cfg.debug,
		blockRetries:    cfg.blockRetries,
		requestURLGuard: cfg.requestURLGuard,
	}
	if cfg.debug {
		bc.Use(LoggingMiddleware)
	}
	if cfg.cookieProvider != nil {
		bc.Use(CloudflareCookieMiddleware(cfg.cookieProvider))
		bc.Use(CloudflareDetectMiddleware)
	}
	if cfg.oxBrowserURL != "" {
		oxProxyFn := oxBrowserProxyFn(cfg.proxyPool)
		oxClient := newOxBrowserClientMaybeProxy(cfg.oxBrowserURL, oxProxyFn)
		if cfg.cookieProvider == nil {
			bc.Use(CloudflareCookieMiddleware(NewOxBrowserSolver(OxBrowserSolverConfig{
				BaseURL: cfg.oxBrowserURL,
				ProxyFn: oxProxyFn,
			})))
			bc.Use(CloudflareDetectMiddleware)
		}
		bc.Use(SmartFetchMiddleware(oxClient))
	}
	return bc, nil
}

// Use appends middlewares to the client's middleware chain.
// Middlewares execute in the order they are added (first added = outermost).
func (bc *BrowserClient) Use(mw ...Middleware) {
	bc.middlewares = append(bc.middlewares, mw...)
	bc.handler = nil // rebuild on next Do()
}

// buildHandler constructs the handler chain from middlewares + base handler.
func (bc *BrowserClient) buildHandler() Handler {
	if bc.handler != nil {
		return bc.handler
	}
	base := bc.baseHandler(bc.headerOrder)
	if len(bc.middlewares) > 0 {
		bc.handler = Chain(bc.middlewares...)(base)
	} else {
		bc.handler = base
	}
	return bc.handler
}

// baseHandler returns the core Handler that delegates to the backend.
// It runs the pre-request (tier-3) SSRF guard on the final target URL before
// handing off to the backend — the only tier that guards a proxied fetch's
// initial target (tier-1 dial control sees only the proxy there).
func (bc *BrowserClient) baseHandler(order []string) Handler {
	return func(req *Request) (*Response, error) {
		if bc.requestURLGuard != nil {
			u, err := url.Parse(req.URL)
			if err != nil {
				return nil, fmt.Errorf("%w: parse %q: %w", ErrSSRFBlocked, req.URL, err)
			}
			if err := bc.requestURLGuard(context.Background(), u); err != nil {
				return nil, err
			}
		}
		req.HeaderOrder = order
		return bc.doer.Do(req)
	}
}

// Do executes an HTTP request with TLS fingerprint impersonation.
// Returns (body bytes, response headers, HTTP status code, error).
// If a ProxyPool was configured, each call rotates to the next proxy.
// Middleware added via Use() is applied to each request.
func (bc *BrowserClient) Do(method, urlStr string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error) {
	req := &Request{Method: method, URL: urlStr, Headers: headers, Body: body}
	return bc.doWithRetry(req, bc.buildHandler())
}

// SetProxy changes the proxy URL for subsequent requests.
func (bc *BrowserClient) SetProxy(proxyURL string) error {
	return bc.doer.SetProxy(proxyURL)
}

// GetCookieValue returns the value of a named cookie for the given URL.
func (bc *BrowserClient) GetCookieValue(rawURL, name string) string {
	return bc.doer.GetCookieValue(rawURL, name)
}

// DoWithHeaderOrder executes a request with a custom header order.
// Middleware and proxy rotation are applied.
func (bc *BrowserClient) DoWithHeaderOrder(method, urlStr string, headers map[string]string, body io.Reader, order []string) ([]byte, map[string]string, int, error) {
	req := &Request{Method: method, URL: urlStr, Headers: headers, Body: body}

	base := bc.baseHandler(order)
	var handler Handler
	if len(bc.middlewares) > 0 {
		handler = Chain(bc.middlewares...)(base)
	} else {
		handler = base
	}

	return bc.doWithRetry(req, handler)
}

// isBlockStatus returns true if the HTTP status indicates a proxy block.
func isBlockStatus(code int) bool {
	switch code {
	case http.StatusForbidden, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}

// doWithRetry executes a request through the handler, retrying with proxy
// rotation on block statuses (403, 429). Requires proxyPool and blockRetries > 0.
//
// If SetProxy fails for a given proxy, that attempt is skipped and the next
// proxy from the pool is tried. If the pool is exhausted without a successful
// SetProxy, an error is returned without sending any request.
func (bc *BrowserClient) doWithRetry(req *Request, handler Handler) ([]byte, map[string]string, int, error) {
	maxAttempts := 1
	if bc.proxyPool != nil && bc.blockRetries > 0 {
		maxAttempts = 1 + bc.blockRetries
	}

	for attempt := range maxAttempts {
		if bc.proxyPool != nil {
			proxyURL := bc.proxyPool.Next()
			if err := bc.SetProxy(proxyURL); err != nil {
				slog.Warn("proxy: SetProxy failed, skipping to next proxy",
					slog.String("proxy", MaskProxy(proxyURL)),
					slog.Int("attempt", attempt+1),
					slog.Any("error", err))
				if attempt == maxAttempts-1 {
					return nil, nil, 0, fmt.Errorf("proxy pool exhausted: all %d proxies failed SetProxy: %w", maxAttempts, err)
				}
				continue
			}
		}

		resp, err := handler(req)
		if err != nil {
			// An SSRF-blocked target is a verdict about the URL/address, not
			// about the proxy in use — every retry would re-block identically
			// (tier-3's pre-request check runs before SetProxy on the next
			// attempt too). Return immediately instead of burning the proxy
			// pool on a request that can never succeed.
			if errors.Is(err, ErrSSRFBlocked) {
				return nil, nil, 0, err
			}
			// Retry on proxy errors (502, connection refused, etc.) with a new proxy.
			if attempt < maxAttempts-1 && bc.proxyPool != nil {
				slog.Debug("request error, retrying with new proxy",
					slog.String("url", req.URL),
					slog.Int("attempt", attempt+1),
					slog.Any("error", err))
				continue
			}
			if resp != nil {
				return nil, nil, resp.StatusCode, err
			}
			return nil, nil, 0, err
		}

		if attempt < maxAttempts-1 && isBlockStatus(resp.StatusCode) {
			slog.Debug("block status, retrying with new proxy",
				slog.String("url", req.URL),
				slog.Int("status", resp.StatusCode),
				slog.Int("attempt", attempt+1))
			jitter := time.Duration(100+rand.IntN(200)) * time.Millisecond //nolint:mnd,gosec
			time.Sleep(jitter)
			continue
		}

		return resp.Body, resp.Headers, resp.StatusCode, nil
	}

	// Unreachable, but satisfies compiler.
	return nil, nil, 0, nil
}

// transportProxyProvider is a subset of proxypool.ProxyPool used for type assertion.
type transportProxyProvider interface {
	TransportProxy() func(*http.Request) (*url.URL, error)
}

// oxBrowserProxyFn extracts a TransportProxy function from pool if it supports it.
// Returns nil if pool is nil or does not implement TransportProxy.
func oxBrowserProxyFn(pool ProxyPoolProvider) func(*http.Request) (*url.URL, error) {
	if pool == nil {
		return nil
	}
	if pp, ok := pool.(transportProxyProvider); ok {
		return pp.TransportProxy()
	}
	return nil
}

// newOxBrowserClientMaybeProxy creates an OxBrowserClient with or without proxy.
func newOxBrowserClientMaybeProxy(baseURL string, proxyFn func(*http.Request) (*url.URL, error)) *OxBrowserClient {
	if proxyFn != nil {
		return NewOxBrowserClientWithProxy(baseURL, proxyFn)
	}
	return NewOxBrowserClient(baseURL)
}
