package stealth

// TLSProfile identifies a TLS fingerprint profile.
type TLSProfile string

// Built-in TLS profiles.
const (
	ProfileChrome131    TLSProfile = "chrome_131"
	ProfileChrome133    TLSProfile = "chrome_133"
	ProfileFirefox133   TLSProfile = "firefox_133"
	ProfileSafari16     TLSProfile = "safari_16_0"
	ProfileSafariIOS18  TLSProfile = "safari_ios_18_0"
	ProfileSafariIOS17  TLSProfile = "safari_ios_17_0"
)

// BackendConfig holds backend-agnostic configuration for creating an HTTPDoer.
type BackendConfig struct {
	Profile         TLSProfile
	ProxyURL        string
	TimeoutSeconds  int
	FollowRedirects bool
	HTTP3           bool
}

// HTTPDoer executes HTTP requests. TLS backends implement this interface.
type HTTPDoer interface {
	Do(req *Request) (*Response, error)
	SetProxy(url string) error
	GetCookieValue(rawURL, name string) string
}

// BackendFactory creates an HTTPDoer from configuration.
type BackendFactory func(cfg BackendConfig) (HTTPDoer, error)
