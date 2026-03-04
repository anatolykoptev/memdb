package search

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"golang.org/x/time/rate"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
)

// BrowserDoer performs HTTP requests with browser-like TLS fingerprint.
// *stealth.BrowserClient satisfies this interface.
type BrowserDoer interface {
	Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}

// DirectConfig controls the SearchDirect fan-out behavior.
type DirectConfig struct {
	Browser          BrowserDoer
	DDG              bool
	Startpage        bool
	Retry            fetch.RetryConfig
	Metrics          *metrics.Registry
	DDGLimiter       *rate.Limiter
	StartpageLimiter *rate.Limiter
}

// runDDG waits on the optional rate limiter then fetches DDG results.
// Returns nil on limiter cancellation.
func runDDG(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.DDGLimiter != nil {
		if err := cfg.DDGLimiter.Wait(ctx); err != nil {
			slog.Debug("ddg rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchDDGDirect(ctx, cfg.Browser, query, "wt-wt", cfg.Metrics)
	})
}

// runStartpage waits on the optional rate limiter then fetches Startpage results.
// Returns nil on limiter cancellation.
func runStartpage(ctx context.Context, cfg DirectConfig, query, language string) ([]sources.Result, error) {
	if cfg.StartpageLimiter != nil {
		if err := cfg.StartpageLimiter.Wait(ctx); err != nil {
			slog.Debug("startpage rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchStartpageDirect(ctx, cfg.Browser, query, language, cfg.Metrics)
	})
}

// SearchDirect queries enabled direct scrapers in parallel.
// Returns merged results from all direct sources. Failures are non-fatal.
func SearchDirect(ctx context.Context, cfg DirectConfig, query, language string) []sources.Result {
	if cfg.Browser == nil {
		return nil
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []sources.Result

	collect := func(results []sources.Result, err error, label string) {
		if err != nil {
			slog.Debug(label+" direct failed", slog.Any("error", err))
			return
		}
		slog.Debug(label+" direct results", slog.Int("count", len(results)))
		mu.Lock()
		all = append(all, results...)
		mu.Unlock()
	}

	if cfg.DDG {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runDDG(ctx, cfg, query)
			collect(results, err, "ddg")
		}()
	}

	if cfg.Startpage {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runStartpage(ctx, cfg, query, language)
			collect(results, err, "startpage")
		}()
	}

	wg.Wait()
	return all
}
