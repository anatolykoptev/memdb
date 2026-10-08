package proxypool

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/pacing"
)

const (
	defaultRefreshInterval = 15 * time.Minute
	defaultRefreshMinGap   = time.Minute
)

// AuthFailureReporter is implemented by pools that can react to a proxy auth
// failure (HTTP 407). *Webshare reports trigger a rate-limited credential
// refresh; the interface lets consumers detect support without a concrete type.
type AuthFailureReporter interface {
	ReportAuthFailure()
}

// WebshareStats is a snapshot of the pool's auth-failure and refresh counters.
type WebshareStats struct {
	AuthFailures     uint64
	RefreshSuccesses uint64
	RefreshFailures  uint64
	LastRefreshAt    time.Time
	PoolSize         int
}

// enableRefresh wires credential refresh on an API-backed pool: a periodic
// jittered refresh (interval > 0) and the 407-triggered path via
// ReportAuthFailure. fetch must re-fetch the proxy list and return the new URL
// slice; a failed fetch never touches the live list.
func (w *Webshare) enableRefresh(cfg WebshareConfig, fetch func(ctx context.Context) ([]string, error)) {
	w.fetch = fetch
	w.interval = cfg.RefreshInterval
	w.minGap = cfg.RefreshMinGap

	ctx, cancel := context.WithCancel(context.Background())
	w.stop = cancel
	w.ctx = ctx

	if w.interval > 0 {
		w.wg.Add(1)
		go w.refreshLoop(ctx)
	}
}

// refreshLoop sleeps a jittered interval between refreshes so a fleet of
// long-running processes does not hit the Webshare API in lockstep.
func (w *Webshare) refreshLoop(ctx context.Context) {
	defer w.wg.Done()
	spread := max(w.interval/4, time.Millisecond)
	j := pacing.Jitter{Min: w.interval - spread, Max: w.interval + spread}
	for {
		if err := j.Sleep(ctx); err != nil {
			return
		}
		w.refresh("periodic")
	}
}

// refresh runs one fetch through the singleflight group: a 407 trigger firing
// while a periodic refresh is in flight joins the same API call instead of
// doubling it. On success the URL list is swapped atomically; readers holding
// an entry from the old list finish their request (strings are immutable).
func (w *Webshare) refresh(reason string) {
	_, _, _ = w.sf.Do("credentials", func() (any, error) {
		list, err := w.fetch(w.ctx)
		if err != nil {
			w.refreshErrs.Add(1)
			w.logger.Warn("proxy: credential refresh failed",
				slog.String("reason", reason), slog.Any("error", err))
			return nil, err
		}
		w.setProxies(list)
		w.refreshOK.Add(1)
		w.lastRefreshNano.Store(time.Now().UnixNano())
		w.logger.Info("proxy: credentials refreshed",
			slog.String("reason", reason), slog.Int("count", len(list)))
		return nil, nil
	})
}

// ReportAuthFailure records a proxy auth failure (HTTP 407) and triggers a
// credential refresh. Reports are rate-limited: at most one refresh is
// triggered per RefreshMinGap window, and concurrent triggers coalesce via
// singleflight. Pools without API access (NewWebshareRotating) only count.
func (w *Webshare) ReportAuthFailure() {
	w.authFailures.Add(1)
	if w.fetch == nil {
		return
	}
	w.triggerMu.Lock()
	defer w.triggerMu.Unlock()
	if w.closed || (!w.lastTrigger.IsZero() && time.Since(w.lastTrigger) < w.minGap) {
		return
	}
	w.lastTrigger = time.Now()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.refresh("auth_failure")
	}()
}

// Stats returns a snapshot of the pool's auth-failure and refresh counters.
func (w *Webshare) Stats() WebshareStats {
	s := WebshareStats{
		AuthFailures:     w.authFailures.Load(),
		RefreshSuccesses: w.refreshOK.Load(),
		RefreshFailures:  w.refreshErrs.Load(),
		PoolSize:         w.Len(),
	}
	if n := w.lastRefreshNano.Load(); n != 0 {
		s.LastRefreshAt = time.Unix(0, n)
	}
	return s
}

// Close stops the periodic refresher and waits for in-flight refreshes.
// Idempotent; the pool keeps serving its last list afterwards.
func (w *Webshare) Close() error {
	w.triggerMu.Lock()
	if w.closed {
		w.triggerMu.Unlock()
		return nil
	}
	w.closed = true
	stop := w.stop
	w.triggerMu.Unlock()

	if stop != nil {
		stop()
	}
	w.wg.Wait()
	return nil
}
