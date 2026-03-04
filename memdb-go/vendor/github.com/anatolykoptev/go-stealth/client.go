package stealth

import (
	"io"
	"log/slog"
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
	doer        HTTPDoer
	headerOrder []string
	proxyPool   ProxyPoolProvider // nil = no auto-rotation
	middlewares []Middleware
	handler     Handler // lazy-built from middlewares + base handler
	debug       bool
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

	backendCfg := BackendConfig{
		Profile:         cfg.profile,
		ProxyURL:        cfg.proxyURL,
		TimeoutSeconds:  cfg.timeout,
		FollowRedirects: cfg.followRedirs,
		HTTP3:           cfg.http3,
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
		doer:        doer,
		headerOrder: order,
		proxyPool:   cfg.proxyPool,
		debug:       cfg.debug,
	}
	if cfg.debug {
		bc.Use(LoggingMiddleware)
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
func (bc *BrowserClient) baseHandler(order []string) Handler {
	return func(req *Request) (*Response, error) {
		req.HeaderOrder = order
		return bc.doer.Do(req)
	}
}

// Do executes an HTTP request with TLS fingerprint impersonation.
// Returns (body bytes, response headers, HTTP status code, error).
// If a ProxyPool was configured, each call rotates to the next proxy.
// Middleware added via Use() is applied to each request.
func (bc *BrowserClient) Do(method, urlStr string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error) {
	if bc.proxyPool != nil {
		proxyURL := bc.proxyPool.Next()
		if err := bc.SetProxy(proxyURL); err != nil {
			slog.Warn("proxy: SetProxy failed", slog.String("proxy", MaskProxy(proxyURL)), slog.Any("error", err))
		}
	}

	req := &Request{Method: method, URL: urlStr, Headers: headers, Body: body}
	handler := bc.buildHandler()

	resp, err := handler(req)
	if err != nil {
		if resp != nil {
			return nil, nil, resp.StatusCode, err
		}
		return nil, nil, 0, err
	}

	return resp.Body, resp.Headers, resp.StatusCode, nil
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
	if bc.proxyPool != nil {
		proxyURL := bc.proxyPool.Next()
		if err := bc.SetProxy(proxyURL); err != nil {
			slog.Warn("proxy: SetProxy failed", slog.String("proxy", MaskProxy(proxyURL)), slog.Any("error", err))
		}
	}

	req := &Request{Method: method, URL: urlStr, Headers: headers, Body: body}

	base := bc.baseHandler(order)
	var handler Handler
	if len(bc.middlewares) > 0 {
		handler = Chain(bc.middlewares...)(base)
	} else {
		handler = base
	}

	resp, err := handler(req)
	if err != nil {
		if resp != nil {
			return nil, nil, resp.StatusCode, err
		}
		return nil, nil, 0, err
	}

	return resp.Body, resp.Headers, resp.StatusCode, nil
}
