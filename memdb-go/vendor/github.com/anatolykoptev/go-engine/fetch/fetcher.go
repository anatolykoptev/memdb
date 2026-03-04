// Package fetch provides HTTP body retrieval with retry, proxy rotation,
// and Chrome TLS fingerprint impersonation via go-stealth.
//
// The [Fetcher] is the main entry point. Configure with functional options:
//
//	f := fetch.New(fetch.WithProxyPool(pool), fetch.WithTimeout(30*time.Second))
//	body, err := f.FetchBody(ctx, "https://example.com")
package fetch

import (
	"context"
	"fmt"
	"net/http"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
	"github.com/anatolykoptev/go-stealth/proxypool"
)

// Default configuration values.
const (
	defaultTimeout             = 30 * time.Second
	defaultMaxIdleConns        = 20
	defaultMaxIdleConnsPerHost = 5
	defaultIdleConnTimeout     = 30 * time.Second
	defaultTLSHandshakeTimeout = 15 * time.Second
	defaultMaxRedirects        = 10
	browserClientTimeoutSec    = 15
)

// RetryConfig controls retry behavior (re-exported from go-stealth).
type RetryConfig = stealth.RetryConfig

// DefaultRetryConfig is suitable for most HTTP calls.
var DefaultRetryConfig = stealth.DefaultRetryConfig

// FetchRetryConfig is tuned for web page fetching (slower initial, more patience).
var FetchRetryConfig = RetryConfig{
	MaxRetries:  3,
	InitialWait: 1 * time.Second,
	MaxWait:     10 * time.Second,
	Multiplier:  2.0,
}

// Fetcher retrieves HTTP response bodies with optional proxy routing.
type Fetcher struct {
	httpClient    *http.Client
	browserClient *stealth.BrowserClient
	retryConfig   RetryConfig
	retryTracker  *stealth.RetryTracker
}

// Option configures a Fetcher.
type Option func(*Fetcher)

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(f *Fetcher) { f.httpClient.Timeout = d }
}

// WithRetryConfig sets the retry configuration.
func WithRetryConfig(rc RetryConfig) Option {
	return func(f *Fetcher) { f.retryConfig = rc }
}

// WithProxyPool initializes a BrowserClient with Chrome TLS fingerprint and proxy rotation.
func WithProxyPool(pool proxypool.ProxyPool) Option {
	return func(f *Fetcher) {
		if pool == nil {
			return
		}
		bc, err := stealth.NewClient(
			stealth.WithTimeout(browserClientTimeoutSec),
			stealth.WithProxyPool(pool),
			stealth.WithFollowRedirects(),
		)
		if err != nil {
			return
		}
		f.browserClient = bc
	}
}

// New creates a Fetcher with the given options.
func New(opts ...Option) *Fetcher {
	f := &Fetcher{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        defaultMaxIdleConns,
				MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
				IdleConnTimeout:     defaultIdleConnTimeout,
				DisableCompression:  false,
				TLSHandshakeTimeout: defaultTLSHandshakeTimeout,
			},
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= defaultMaxRedirects {
					return fmt.Errorf("stopped after %d redirects", defaultMaxRedirects)
				}
				return nil
			},
		},
		retryConfig: FetchRetryConfig,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// FetchBody retrieves the response body bytes from a URL.
// Routes through BrowserClient (residential proxy) when available,
// falls back to standard HTTP client otherwise.
// When a RetryTracker is configured, it checks ShouldRetry before each request
// and records the outcome (attempt or success) after.
func (f *Fetcher) FetchBody(ctx context.Context, url string) ([]byte, error) {
	if f.retryTracker != nil && !f.retryTracker.ShouldRetry(url) {
		return nil, ErrPermanentlyFailed
	}

	var body []byte
	var err error
	if f.browserClient != nil {
		body, err = f.fetchViaProxy(ctx, url)
	} else {
		body, err = f.fetchViaHTTP(ctx, url)
	}

	if f.retryTracker != nil {
		if err != nil {
			f.retryTracker.RecordAttempt(url, err)
		} else {
			f.retryTracker.RecordSuccess(url)
		}
	}

	return body, err
}

// HasProxy reports whether the fetcher has a proxy-backed BrowserClient.
func (f *Fetcher) HasProxy() bool {
	return f.browserClient != nil
}

// BrowserClient returns the underlying stealth BrowserClient, or nil.
// Use this to share the browser client with search functions (DDG, Startpage).
func (f *Fetcher) BrowserClient() *stealth.BrowserClient {
	return f.browserClient
}

// fetchViaProxy routes through BrowserClient with Chrome TLS fingerprint.
func (f *Fetcher) fetchViaProxy(ctx context.Context, fetchURL string) ([]byte, error) {
	headers := ChromeHeaders()
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	return RetryDo(ctx, f.retryConfig, func() ([]byte, error) {
		data, _, status, err := f.browserClient.Do(http.MethodGet, fetchURL, headers, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, &stealth.HttpStatusError{StatusCode: status}
		}
		return data, nil
	})
}

// fetchViaHTTP uses the standard HTTP client with retry.
func (f *Fetcher) fetchViaHTTP(ctx context.Context, fetchURL string) ([]byte, error) {
	resp, err := RetryHTTP(ctx, f.retryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", RandomUserAgent())
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		return f.httpClient.Do(req) //nolint:gosec // URL is user-provided by design
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &stealth.HttpStatusError{StatusCode: resp.StatusCode}
	}

	return ReadResponseBody(resp)
}
