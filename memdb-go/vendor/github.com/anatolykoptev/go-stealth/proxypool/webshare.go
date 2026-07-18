package proxypool

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// WebshareMode controls how proxy URLs are constructed and which endpoint is used.
type WebshareMode int

const (
	// ModeBackbone uses the shared gateway p.webshare.io:port with backbone IPs (default).
	ModeBackbone WebshareMode = iota
	// ModeRotating uses p.webshare.io:80 with Webshare-side rotation; no API key needed.
	ModeRotating
	// ModeDirect connects directly to the proxy IP:port without a gateway.
	ModeDirect
)

// WebshareConfig holds options for NewWebshareWithConfig.
// Countries: ISO-2 codes, empty = ["US"]. Mode: default ModeBackbone.
// PageSize: default 100. BaseURL: override for tests (query params still appended).
type WebshareConfig struct {
	Countries  []string
	Mode       WebshareMode
	PageSize   int
	HTTPClient *http.Client
	BaseURL    string
	Logger     *slog.Logger
}

// Webshare implements ProxyPool using the Webshare API.
type Webshare struct {
	proxies []string
	counter atomic.Uint64
}

type webshareResponse struct {
	Results []webshareProxy `json:"results"`
	Next    *string         `json:"next"`
}

type webshareProxy struct {
	ProxyAddress string `json:"proxy_address"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

const (
	webshareDefaultBase = "https://proxy.webshare.io/api/v2/proxy/list/"
	webshareDefaultHost = "p.webshare.io" // shared gateway for backbone proxies
	webshareMaxPages    = 50
)

// NewWebshare fetches proxies from the Webshare API using defaults (US backbone proxies).
// Back-compat wrapper — existing callers continue to work unchanged.
func NewWebshare(apiKey string) (*Webshare, error) {
	return NewWebshareWithConfig(apiKey, WebshareConfig{})
}

// NewWebshareWithConfig is the primary constructor. Validates config, applies defaults,
// fetches with pagination, and injects country modifiers into usernames.
func NewWebshareWithConfig(apiKey string, cfg WebshareConfig) (*Webshare, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("proxy: empty API key")
	}
	if err := applyConfigDefaults(&cfg); err != nil {
		return nil, err
	}
	if cfg.Mode == ModeRotating {
		return buildRotatingFromAPI(apiKey, cfg, cfg.Logger)
	}
	proxies, err := fetchAllProxies(apiKey, cfg)
	if err != nil {
		return nil, err
	}
	result := injectCountryModifiers(proxies, cfg.Countries, cfg.Mode)
	cfg.Logger.Info("proxy pool initialized",
		slog.Int("count", len(result)),
		slog.Any("countries", cfg.Countries),
		slog.String("mode", modeString(cfg.Mode)),
	)
	return &Webshare{proxies: result}, nil
}

// NewWebshareRotating builds a rotating pool without API calls (zero network).
// Each country → one entry: http://username-CC-rotate:password@p.webshare.io:80.
// Defaults to ["US"] when no countries are specified.
func NewWebshareRotating(username, password string, countries ...string) (*Webshare, error) {
	cc := countries
	if len(cc) == 0 {
		cc = []string{"US"}
	}
	deduped, err := validateAndDedup(cc)
	if err != nil {
		return nil, err
	}

	proxies := make([]string, 0, len(deduped))
	for _, c := range deduped {
		u := fmt.Sprintf("http://%s-%s-rotate:%s@%s:80", username, c, password, webshareDefaultHost)
		proxies = append(proxies, u)
	}

	slog.Default().Info("proxy pool initialized",
		slog.Int("count", len(proxies)),
		slog.Any("countries", deduped),
		slog.String("mode", "rotating"),
	)

	return &Webshare{proxies: proxies}, nil
}

// newWebshareFromURL is an internal helper used by legacy tests.
// It does NOT apply country defaults — it fetches exactly what the URL says.
func newWebshareFromURL(apiURL, apiKey string) (*Webshare, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("proxy: empty API key")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	proxies, err := fetchPage(client, apiURL, apiKey)
	if err != nil {
		return nil, err
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("proxy: webshare returned 0 proxies")
	}

	result := make([]string, 0, len(proxies))
	for _, p := range proxies {
		host := p.ProxyAddress
		if host == "" {
			host = webshareDefaultHost
		}
		result = append(result, fmt.Sprintf("http://%s:%s@%s:%d", p.Username, p.Password, host, p.Port))
	}

	slog.Info("proxy pool initialized", slog.Int("count", len(result)))
	return &Webshare{proxies: result}, nil
}

// buildRotatingFromAPI fetches credentials from one API page, then builds rotating URLs.
func buildRotatingFromAPI(apiKey string, cfg WebshareConfig, logger *slog.Logger) (*Webshare, error) {
	onePageURL := buildBaseURL(cfg.BaseURL) + "?mode=backbone&page_size=1"
	page, err := fetchPage(cfg.HTTPClient, onePageURL, apiKey)
	if err != nil {
		return nil, err
	}
	if len(page) == 0 {
		return nil, fmt.Errorf("proxy: webshare returned 0 proxies for rotating credentials")
	}

	proxies := make([]string, 0, len(cfg.Countries))
	for _, c := range cfg.Countries {
		u := fmt.Sprintf("http://%s-%s-rotate:%s@%s:80", page[0].Username, c, page[0].Password, webshareDefaultHost)
		proxies = append(proxies, u)
	}

	logger.Info("proxy pool initialized",
		slog.Int("count", len(proxies)),
		slog.Any("countries", cfg.Countries),
		slog.String("mode", "rotating"),
	)
	return &Webshare{proxies: proxies}, nil
}

// Next returns the next proxy URL in round-robin order.
func (w *Webshare) Next() string {
	if len(w.proxies) == 0 {
		return ""
	}
	idx := w.counter.Add(1) % uint64(len(w.proxies))
	return w.proxies[idx]
}

// Len returns the number of proxies in the pool.
func (w *Webshare) Len() int {
	return len(w.proxies)
}

// TransportProxy returns a function suitable for http.Transport.Proxy.
func (w *Webshare) TransportProxy() func(*http.Request) (*url.URL, error) {
	return func(_ *http.Request) (*url.URL, error) {
		return url.Parse(w.Next())
	}
}

func modeString(m WebshareMode) string {
	switch m {
	case ModeRotating:
		return "rotating"
	case ModeDirect:
		return "direct"
	default:
		return "backbone"
	}
}
