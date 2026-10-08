package proxypool

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
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
// RefreshInterval: periodic credential re-fetch, default 15min (with ±25%
// jitter); a negative value disables periodic refresh. RefreshMinGap: minimum
// gap between 407-triggered refreshes, default 1min.
type WebshareConfig struct {
	Countries  []string
	Mode       WebshareMode
	PageSize   int
	HTTPClient *http.Client
	BaseURL    string
	Logger     *slog.Logger

	RefreshInterval time.Duration
	RefreshMinGap   time.Duration
}

// Webshare implements ProxyPool using the Webshare API.
type Webshare struct {
	proxies atomic.Pointer[[]string] // current URL list; swapped atomically on refresh
	counter atomic.Uint64

	logger   *slog.Logger
	fetch    func(ctx context.Context) ([]string, error) // nil = refresh unsupported (static creds)
	interval time.Duration
	minGap   time.Duration

	sf          singleflight.Group // collapses concurrent refreshes into one fetch
	triggerMu   sync.Mutex         // guards lastTrigger + closed + trigger-side wg.Add
	lastTrigger time.Time
	closed      bool

	authFailures    atomic.Uint64
	refreshOK       atomic.Uint64
	refreshErrs     atomic.Uint64
	lastRefreshNano atomic.Int64

	stop context.CancelFunc // nil when refresh was never wired
	ctx  context.Context    // cancelled by stop; carried into in-flight fetches
	wg   sync.WaitGroup     // refresher loop + in-flight triggered refreshes
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
	fetch := func(ctx context.Context) ([]string, error) {
		proxies, err := fetchAllProxies(ctx, apiKey, cfg)
		if err != nil {
			return nil, err
		}
		return injectCountryModifiers(proxies, cfg.Countries, cfg.Mode), nil
	}
	result, err := fetch(context.Background())
	if err != nil {
		return nil, err
	}
	cfg.Logger.Info("proxy pool initialized",
		slog.Int("count", len(result)),
		slog.Any("countries", cfg.Countries),
		slog.String("mode", modeString(cfg.Mode)),
	)
	w := newWebsharePool(result, cfg.Logger)
	w.enableRefresh(cfg, fetch)
	return w, nil
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

	proxies := rotatingURLs(username, password, deduped)

	slog.Default().Info("proxy pool initialized",
		slog.Int("count", len(proxies)),
		slog.Any("countries", deduped),
		slog.String("mode", "rotating"),
	)

	// Static credentials — no API access, so no refresh is wired.
	return newWebsharePool(proxies, slog.Default()), nil
}

// newWebshareFromURL is an internal helper used by legacy tests.
// It does NOT apply country defaults — it fetches exactly what the URL says.
func newWebshareFromURL(apiURL, apiKey string) (*Webshare, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("proxy: empty API key")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	proxies, err := fetchPage(context.Background(), client, apiURL, apiKey)
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
	return newWebsharePool(result, slog.Default()), nil
}

// buildRotatingFromAPI fetches credentials from one API page, then builds rotating URLs.
func buildRotatingFromAPI(apiKey string, cfg WebshareConfig, logger *slog.Logger) (*Webshare, error) {
	fetch := func(ctx context.Context) ([]string, error) {
		page, err := fetchPage(ctx, cfg.HTTPClient, buildBaseURL(cfg.BaseURL)+"?mode=backbone&page_size=1", apiKey)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return nil, fmt.Errorf("proxy: webshare returned 0 proxies for rotating credentials")
		}
		return rotatingURLs(page[0].Username, page[0].Password, cfg.Countries), nil
	}

	proxies, err := fetch(context.Background())
	if err != nil {
		return nil, err
	}

	logger.Info("proxy pool initialized",
		slog.Int("count", len(proxies)),
		slog.Any("countries", cfg.Countries),
		slog.String("mode", "rotating"),
	)
	w := newWebsharePool(proxies, logger)
	w.enableRefresh(cfg, fetch)
	return w, nil
}

// rotatingURLs builds one username-CC-rotate URL per country for the
// shared-gateway rotating endpoint.
func rotatingURLs(username, password string, countries []string) []string {
	urls := make([]string, 0, len(countries))
	for _, c := range countries {
		urls = append(urls, fmt.Sprintf("http://%s-%s-rotate:%s@%s:80", username, c, password, webshareDefaultHost))
	}
	return urls
}

// newWebsharePool stores the initial list atomically and attaches a logger.
func newWebsharePool(list []string, logger *slog.Logger) *Webshare {
	if logger == nil {
		logger = slog.Default()
	}
	w := &Webshare{logger: logger, ctx: context.Background()}
	w.setProxies(list)
	return w
}

func (w *Webshare) setProxies(list []string) { w.proxies.Store(&list) }

func (w *Webshare) loadProxies() []string {
	if p := w.proxies.Load(); p != nil {
		return *p
	}
	return nil
}

// Next returns the next proxy URL in round-robin order.
func (w *Webshare) Next() string {
	list := w.loadProxies()
	if len(list) == 0 {
		return ""
	}
	idx := w.counter.Add(1) % uint64(len(list))
	return list[idx]
}

// Len returns the number of proxies in the pool.
func (w *Webshare) Len() int {
	return len(w.loadProxies())
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
