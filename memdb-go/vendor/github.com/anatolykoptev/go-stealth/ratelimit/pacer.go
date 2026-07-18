package ratelimit

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// pacerPollInterval is how often Wait re-checks readiness. At the production
// per-account spacing (sub-second to a few seconds) this quantizes realized
// spacing to the poll period, which is negligible for human-pace stealth.
const pacerPollInterval = 25 * time.Millisecond

// KeyedPacer spaces consecutive requests for the SAME key by a minimum delay
// plus optional random jitter. Pacing is independent per key: a recent request
// on key A never delays key B. This is the per-account stealth pacer — keyed by
// account ID after the pool selects an account, so each account self-paces its
// own request rhythm without a single global gate that would starve a
// low-frequency caller. It deliberately carries NO window/rate limiter: the
// per-account-per-endpoint ratelimit.Limiter is the authoritative throughput
// ceiling; this only adds human-like spacing under that ceiling.
type KeyedPacer struct {
	minDelay    time.Duration
	randomDelay time.Duration
	clock       func() time.Time

	mu sync.Mutex
	// nextAllowed[key] is the earliest time the key may fire again. It is set
	// once when a request is granted (sampling the random jitter exactly once),
	// so realized spacing is uniform over [minDelay, minDelay+randomDelay) rather
	// than biased toward the low end by re-rolling on every poll.
	nextAllowed map[string]time.Time
}

// PacerOption configures a KeyedPacer.
type PacerOption func(*KeyedPacer)

// WithPacerClock injects the time source (default time.Now) so tests can
// advance time deterministically instead of sleeping.
func WithPacerClock(clock func() time.Time) PacerOption {
	return func(p *KeyedPacer) {
		if clock != nil {
			p.clock = clock
		}
	}
}

// NewKeyedPacer creates a per-key pacer. minDelay is the hard floor between
// consecutive same-key requests; randomDelay adds [0, randomDelay) jitter on
// top so realized spacing is human-variable. Both zero ⇒ pacing disabled
// (Allow always true, Wait always immediate).
func NewKeyedPacer(minDelay, randomDelay time.Duration, opts ...PacerOption) *KeyedPacer {
	p := &KeyedPacer{
		minDelay:    minDelay,
		randomDelay: randomDelay,
		clock:       time.Now,
		nextAllowed: make(map[string]time.Time),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// disabled reports whether no spacing is configured.
func (p *KeyedPacer) disabled() bool {
	return p.minDelay <= 0 && p.randomDelay <= 0
}

// Allow reports whether a request for key may proceed now. When it returns true
// it arms the key's next-allowed time by sampling MinDelay+jitter ONCE, so the
// jitter is rolled exactly once per granted request (faithful spacing
// distribution), not re-rolled on every poll. The first request for any key is
// always allowed (no prior grant to space against).
func (p *KeyedPacer) Allow(key string) bool {
	if p.disabled() {
		return true
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clock()
	if next, ok := p.nextAllowed[key]; ok && now.Before(next) {
		return false
	}

	delay := p.minDelay
	if p.randomDelay > 0 {
		delay += time.Duration(rand.Int64N(int64(p.randomDelay)))
	}
	p.nextAllowed[key] = now.Add(delay)
	return true
}

// Wait blocks until a request for key is allowed or ctx is cancelled. It polls
// Allow at pacerPollInterval. Returns ctx.Err() if the context is cancelled
// before the key becomes available.
func (p *KeyedPacer) Wait(ctx context.Context, key string) error {
	if p.disabled() {
		return nil
	}
	for {
		if p.Allow(key) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pacerPollInterval):
		}
	}
}
