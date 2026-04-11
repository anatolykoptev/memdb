package server

import (
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	mw "github.com/anatolykoptev/memdb/memdb-go/internal/server/middleware"

	"github.com/anatolykoptev/go-engine/fetch"
	enginesearch "github.com/anatolykoptev/go-engine/search"
	"github.com/anatolykoptev/go-stealth/proxypool"
)

// applyMiddleware wraps the handler with the full middleware stack.
// Order: outermost wrapper first → innermost last.
func applyMiddleware(next http.Handler, cfg *config.Config, cacheClient *cache.Client, logger *slog.Logger) http.Handler {
	h := next
	h = mw.Cache(logger, mw.CacheConfig{Client: cacheClient})(h)
	h = mw.OTel(logger, cfg.OTelEnabled)(h)
	h = mw.RateLimit(logger, mw.RateLimitConfig{
		Enabled:       cfg.RateLimitEnabled,
		RPS:           cfg.RateLimitRPS,
		Burst:         cfg.RateLimitBurst,
		ServiceSecret: cfg.InternalServiceSecret,
	})(h)
	h = mw.Auth(logger, mw.AuthConfig{
		Enabled:       cfg.AuthEnabled,
		MasterKeyHash: cfg.MasterKeyHash,
		ServiceSecret: cfg.InternalServiceSecret,
	})(h)
	h = mw.CORS(h)
	h = mw.Logging(logger)(h)
	h = mw.RequestID(h)
	h = mw.Recovery(logger)(h)
	return h
}

// initBrowserClient creates a stealth browser client with Webshare proxy pool.
// Returns nil if WebshareAPIKey is empty or proxy pool init fails.
func initBrowserClient(cfg *config.Config, logger *slog.Logger) enginesearch.BrowserDoer {
	if cfg.WebshareAPIKey == "" {
		return nil
	}
	pool, err := proxypool.NewWebshare(cfg.WebshareAPIKey)
	if err != nil {
		logger.Warn("failed to init proxy pool, direct scraping disabled", slog.Any("error", err))
		return nil
	}
	f := fetch.New(fetch.WithProxyPool(pool))
	logger.Info("proxy pool initialized", slog.Int("proxies", pool.Len()))
	return f.BrowserClient()
}
