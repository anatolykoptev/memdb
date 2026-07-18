package stealth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
)

// ClientOption configures a BrowserClient.
type ClientOption func(*clientConfig)

type clientConfig struct {
	proxyURL           string
	proxyPool          ProxyPoolProvider
	profile            TLSProfile
	timeout            int
	headerOrder        []string
	followRedirs       bool
	debug              bool
	backend            BackendFactory
	http3              bool
	insecureSkipVerify bool
	blockRetries       int
	cookieProvider     CookieProvider
	oxBrowserURL       string
	buildErrors        []error // deferred errors from option constructors

	// SSRF guards. Populated by defaultConfig() with the stdlib default-deny
	// closures so a zero-option client is fail-closed BY CONSTRUCTION. Each is
	// independently overridable (WithDialControl / WithRedirectGuard /
	// WithRequestURLGuard) or cleared as a set (WithoutSSRFGuard, tests only).
	dialControl     func(network, address string) error
	redirectGuard   func(req *http.Request, via []*http.Request) error
	requestURLGuard func(ctx context.Context, u *url.URL) error
}

func defaultConfig() *clientConfig {
	return &clientConfig{
		profile:         ProfileChrome131,
		timeout:         20,
		dialControl:     defaultDenyDial,
		redirectGuard:   defaultDenyRedirect,
		requestURLGuard: defaultDenyURL,
	}
}

// WithProxy sets the proxy URL (e.g. "socks5://user:pass@host:port").
func WithProxy(url string) ClientOption {
	return func(c *clientConfig) {
		c.proxyURL = url
	}
}

// WithProfile sets the TLS client profile for fingerprint impersonation.
func WithProfile(p TLSProfile) ClientOption {
	return func(c *clientConfig) {
		c.profile = p
	}
}

// WithTimeout sets the request timeout in seconds.
func WithTimeout(seconds int) ClientOption {
	return func(c *clientConfig) {
		c.timeout = seconds
	}
}

// WithHeaderOrder sets the default HTTP header ordering for requests.
func WithHeaderOrder(order []string) ClientOption {
	return func(c *clientConfig) {
		c.headerOrder = order
	}
}

// WithFollowRedirects enables redirect following (disabled by default).
func WithFollowRedirects() ClientOption {
	return func(c *clientConfig) {
		c.followRedirs = true
	}
}

// WithDebug enables request/response logging via slog.Debug.
// Automatically adds LoggingMiddleware to the middleware chain.
func WithDebug() ClientOption {
	return func(c *clientConfig) {
		c.debug = true
	}
}

// WithProxyPool enables per-request proxy rotation.
// Each call to Do() will cycle to the next proxy in the pool.
func WithProxyPool(pool ProxyPoolProvider) ClientOption {
	return func(c *clientConfig) {
		c.proxyPool = pool
	}
}

// WithBackend sets a custom backend factory for creating the HTTPDoer.
// If not set, the default bogdanfinn/tls-client backend is used.
func WithBackend(factory BackendFactory) ClientOption {
	return func(c *clientConfig) {
		c.backend = factory
	}
}

// WithStdHTTP uses the standard net/http backend instead of tls-client.
// No TLS fingerprinting — useful for testing and CGO-free environments.
func WithStdHTTP() ClientOption {
	return func(c *clientConfig) {
		c.backend = newStdBackend
	}
}

// WithHTTP3 enables HTTP/3 QUIC support (tls-client backend only).
func WithHTTP3() ClientOption {
	return func(c *clientConfig) {
		c.http3 = true
	}
}

// WithInsecureSkipVerify disables TLS certificate verification.
// WARNING: this makes connections vulnerable to man-in-the-middle attacks.
// Use only for local testing (httptest.NewTLSServer) or explicit MITM proxy
// inspection. Never use in production against public endpoints.
func WithInsecureSkipVerify() ClientOption {
	return func(c *clientConfig) {
		c.insecureSkipVerify = true
		slog.Warn("TLS certificate verification DISABLED via WithInsecureSkipVerify(). Connections are vulnerable to MITM attacks. Use only for self-signed certs in development.")
	}
}

// WithRetryOnBlock enables automatic retry with proxy rotation when the server
// returns a block status (403, 429). Each retry uses the next proxy from the pool.
// Requires WithProxyPool. n is the number of extra retries (total attempts = 1 + n).
func WithRetryOnBlock(n int) ClientOption {
	return func(c *clientConfig) {
		if n > 0 {
			c.blockRetries = n
		}
	}
}

// WithCookieSolver enables Cloudflare challenge solving via a CookieProvider.
// Automatically adds CloudflareDetectMiddleware and CloudflareCookieMiddleware.
func WithCookieSolver(provider CookieProvider) ClientOption {
	return func(c *clientConfig) {
		c.cookieProvider = provider
	}
}

// WithOxBrowser enables ox-browser integration for CF solving and smart fetch.
// If no CookieProvider is already set, adds OxBrowserSolver + CloudflareDetectMiddleware.
// Always adds SmartFetchMiddleware as a fallback for CF-challenged responses.
// url is the ox-browser base URL (e.g. "http://127.0.0.1:8901").
func WithOxBrowser(url string) ClientOption {
	return func(c *clientConfig) {
		c.oxBrowserURL = url
	}
}

// WithDialControl overrides the connect-time (tier-1) SSRF guard: the
// rebind-proof check run on the already-resolved address immediately before
// connect(2), on both backends' dialers. Pass go-kit/httputil's SSRFGuards()
// dial closure (or DenyBlockedAddress) here for the framework's full policy.
// A nil fn disables the connect-time guard.
//
// On a PROXIED client this hook sees only the proxy's address, never the real
// target — pair it with WithRequestURLGuard (tier 3), which does.
func WithDialControl(fn func(network, address string) error) ClientOption {
	return func(c *clientConfig) {
		c.dialControl = tagGuardErr2(fn)
	}
}

// tagGuardErr2 / tagGuardErrReq / tagGuardErrURL wrap a caller-supplied guard
// so any non-nil error it returns also satisfies errors.Is(err, ErrSSRFBlocked)
// (the caller's error chain is preserved via %w). This is what makes a guard
// REJECTION non-retryable in doWithRetry, which short-circuits on
// ErrSSRFBlocked: without it, a consumer wiring go-kit/httputil's CheckURL /
// SSRFGuards() (whose errors do NOT wrap this package's sentinel) would have a
// blocked target pointlessly retried against every proxy before failing. The
// built-in default guards already return ErrSSRFBlocked directly, so only the
// caller-supplied (With*) path needs tagging. A guard rejection is treated as
// non-retryable even when it stems from a target-resolve failure — retrying
// through a different proxy cannot change the target host's DNS answer.
func tagGuardErr2(fn func(network, address string) error) func(network, address string) error {
	if fn == nil {
		return nil
	}
	return func(network, address string) error {
		if err := fn(network, address); err != nil {
			return fmt.Errorf("%w: %w", ErrSSRFBlocked, err)
		}
		return nil
	}
}

// WithRedirectGuard overrides the per-hop (tier-2) SSRF guard: a
// CheckRedirect-shaped closure run on every redirect hop the backend follows
// (only relevant with WithFollowRedirects). It MUST re-own a hop cap — a
// custom CheckRedirect replaces net/http's built-in 10-hop limit. Pass
// go-kit/httputil's SSRFGuards() redirect closure here. A nil fn disables the
// per-hop guard (the backend then falls back to its built-in redirect cap).
//
// On the std backend, req and via are the exact *http.Request values
// net/http's own redirect loop uses (Body/Cancel/ctx and all). On the tls
// backend, req and via are adapted from bogdanfinn/fhttp's redirect chain:
// Method, URL, and Header are populated faithfully hop-by-hop (fhttp builds
// its own via chain from real prior requests, same shape as net/http's), but
// Body/GetBody/Cancel/Context and any other *http.Request field are left at
// their zero value — a guard that needs only len(via)/via[i].URL/via[i].Host
// (the go-kit SSRFGuards() shape) works unchanged on both backends; a guard
// reading Body or Context should not assume either backend populates them.
func WithRedirectGuard(fn func(req *http.Request, via []*http.Request) error) ClientOption {
	return func(c *clientConfig) {
		c.redirectGuard = tagGuardErrReq(fn)
	}
}

// tagGuardErrReq is tagGuardErr2 for the redirect-guard signature. net/http
// wraps a CheckRedirect error in a *url.Error, whose Unwrap chain errors.Is
// still traverses to ErrSSRFBlocked.
func tagGuardErrReq(fn func(req *http.Request, via []*http.Request) error) func(req *http.Request, via []*http.Request) error {
	if fn == nil {
		return nil
	}
	return func(req *http.Request, via []*http.Request) error {
		if err := fn(req, via); err != nil {
			return fmt.Errorf("%w: %w", ErrSSRFBlocked, err)
		}
		return nil
	}
}

// WithRequestURLGuard overrides the pre-request (tier-3) SSRF guard: a check
// on the INITIAL target URL, evaluated before the (possibly proxied) fetch.
// This is the ONLY tier that guards a proxied fetch's initial target, since a
// proxied dial control (tier 1) sees only the proxy. Signature matches
// go-kit/httputil.CheckURL, so a consumer can wire it directly. A nil fn
// disables the pre-request guard.
func WithRequestURLGuard(fn func(ctx context.Context, u *url.URL) error) ClientOption {
	return func(c *clientConfig) {
		c.requestURLGuard = tagGuardErrURL(fn)
	}
}

// tagGuardErrURL is tagGuardErr2 for the pre-request-guard signature. This is
// the load-bearing one for a PROXIED client (the only tier that sees the real
// target): baseHandler runs it inside the doWithRetry loop, so an untagged
// go-kit CheckURL rejection would otherwise be retried against every proxy.
func tagGuardErrURL(fn func(ctx context.Context, u *url.URL) error) func(ctx context.Context, u *url.URL) error {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, u *url.URL) error {
		if err := fn(ctx, u); err != nil {
			return fmt.Errorf("%w: %w", ErrSSRFBlocked, err)
		}
		return nil
	}
}

// WithoutSSRFGuard clears all three SSRF guard tiers (dial, redirect,
// pre-request), restoring the pre-guard byte-for-byte behavior.
//
// FOR TESTS ONLY. A BrowserClient is fail-closed by construction — its
// built-in stdlib default-deny refuses loopback/private/link-local targets,
// which is exactly what an httptest (127.0.0.1) suite needs to opt out of.
// Never call this from production code; the fleet fitness function forbids it
// outside _test.go.
func WithoutSSRFGuard() ClientOption {
	return func(c *clientConfig) {
		c.dialControl = nil
		c.redirectGuard = nil
		c.requestURLGuard = nil
	}
}
