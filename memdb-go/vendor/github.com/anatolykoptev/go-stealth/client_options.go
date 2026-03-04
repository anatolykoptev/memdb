package stealth

// ClientOption configures a BrowserClient.
type ClientOption func(*clientConfig)

type clientConfig struct {
	proxyURL     string
	proxyPool    ProxyPoolProvider
	profile      TLSProfile
	timeout      int
	headerOrder  []string
	followRedirs bool
	debug        bool
	backend      BackendFactory
	http3        bool
}

func defaultConfig() *clientConfig {
	return &clientConfig{
		profile: ProfileChrome131,
		timeout: 20,
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
