package stealth

import "net/http"

// TLSProfile identifies a TLS fingerprint profile.
type TLSProfile string

// Built-in TLS profiles.
const (
	ProfileChrome131   TLSProfile = "chrome_131"
	ProfileChrome133   TLSProfile = "chrome_133"
	ProfileFirefox133  TLSProfile = "firefox_133"
	ProfileSafari16    TLSProfile = "safari_16_0"
	ProfileSafariIOS18 TLSProfile = "safari_ios_18_0"
	ProfileSafariIOS17 TLSProfile = "safari_ios_17_0"
)

// BackendConfig holds backend-agnostic configuration for creating an HTTPDoer.
type BackendConfig struct {
	Profile         TLSProfile
	ProxyURL        string
	TimeoutSeconds  int
	FollowRedirects bool
	HTTP3           bool

	// InsecureSkipVerify, when true, disables TLS certificate verification.
	// Honored by the tls-client backend; the std backend verifies by default
	// (net/http) and will honor this field when wired.
	InsecureSkipVerify bool

	// DialControl, when non-nil, is installed as the connect-time SSRF guard
	// on the backend's dialer (net.Dialer.Control on the std backend;
	// tls-client WithDialer on the tls backend). It fires on the
	// already-resolved address, per redirect hop, on the DIRECT path.
	DialControl func(network, address string) error

	// RedirectGuard, when non-nil AND FollowRedirects is true, is installed as
	// the per-hop redirect check (http.Client.CheckRedirect on the std
	// backend; an fhttp→stdlib-adapted WithCustomRedirectFunc on the tls
	// backend). It must re-own the redirect hop cap.
	RedirectGuard func(req *http.Request, via []*http.Request) error
}

// HTTPDoer executes HTTP requests. TLS backends implement this interface.
type HTTPDoer interface {
	Do(req *Request) (*Response, error)
	SetProxy(url string) error
	GetCookieValue(rawURL, name string) string
}

// BackendFactory creates an HTTPDoer from configuration.
type BackendFactory func(cfg BackendConfig) (HTTPDoer, error)
