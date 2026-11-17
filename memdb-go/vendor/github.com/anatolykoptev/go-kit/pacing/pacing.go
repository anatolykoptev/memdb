// Package pacing provides anti-fingerprint request spacing primitives for
// stealth HTTP clients: uniform random jitter, per-key pacing, heavy-tailed
// human-like gap spreads (Spread), and symmetric percentage jitter for backoff. These are the canonical implementations for
// the go-* fleet — go-stealth, go-twitter, and go-kit's own retry/breaker
// packages delegate here instead of carrying ad-hoc copies.
//
// Pacing is distinct from ratelimit: a rate limiter is the authoritative
// throughput ceiling; pacing adds human-like spacing under that ceiling to
// evade fingerprinting on request timing. Pacing never blocks a request that
// the rate limiter would allow — it only delays it.
package pacing

import (
	"context"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// Jitter defines a uniform random delay range for anti-fingerprinting.
// A request spaced by Jitter fires at a uniformly random point in [Min, Max),
// which avoids the periodic burst pattern that a fixed delay produces.
type Jitter struct {
	Min time.Duration
	Max time.Duration
}

// DefaultJitter is 500ms–2.5s, suitable for most page fetching. Callers with tighter
// or looser human-pace requirements should construct their own Jitter.
var DefaultJitter = Jitter{
	Min: 500 * time.Millisecond,
	Max: 2500 * time.Millisecond,
}

// Duration returns a uniform random duration in [Min, Max). Returns 0 if
// Max <= Min (degenerate range).
func (j Jitter) Duration() time.Duration {
	if j.Max <= j.Min {
		return 0
	}
	return j.Min + time.Duration(rand.Int64N(int64(j.Max-j.Min)))
}

// Sleep pauses for a uniform random duration in [Min, Max). Returns ctx.Err()
// if the context is cancelled during the wait. A degenerate range (Max <= Min)
// returns immediately without sleeping.
func (j Jitter) Sleep(ctx context.Context) error {
	d := j.Duration()
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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

// SymmetricJitter applies ±pct% random variation to a base duration, returning
// a duration in [base*(1-pct), base*(1+pct)]. pct is a fraction in [0,1]:
// 0.25 means ±25%. Returns base unchanged when pct <= 0 or base <= 0. The
// result is clamped to be non-negative.
//
// This is the canonical symmetric jitter for backoff/retry delay variation
// across the fleet — go-kit/retry, go-kit/breaker, and go-stealth/BackoffConfig
// all delegate here.
func SymmetricJitter(base time.Duration, pct float64) time.Duration {
	if base <= 0 || pct <= 0 {
		return base
	}
	// range01 ∈ [-1, 1)
	range01 := rand.Float64()*2 - 1
	jittered := float64(base) * (1 + pct*range01)
	result := time.Duration(jittered)
	if result < 0 {
		return 0
	}
	return result
}

// ExponentialBackoff returns the backoff delay for the given attempt (0-indexed)
// with symmetric jitter applied. Formula: initial * mult^attempt, capped at max,
// then ±pct jitter. This is the canonical exponential backoff with jitter —
// go-stealth/BackoffConfig.Duration and go-kit/breaker.computeBackoff delegate
// here.
func ExponentialBackoff(initial, max time.Duration, multiplier, jitterPct float64, attempt int) time.Duration {
	base := float64(initial) * math.Pow(multiplier, float64(attempt))
	if max > 0 && base > float64(max) {
		base = float64(max)
	}
	return SymmetricJitter(time.Duration(base), jitterPct)
}

// DefaultSpreadSigma is the log-space spread Spread uses when Sigma is zero,
// negative, NaN or infinite.
const DefaultSpreadSigma = 1.0

// maxSpreadSigma bounds Sigma so sigma*z cannot reach Inf (and Inf*0 = NaN)
// inside the exponent; beyond this the clamp to [Min, Max] dominates anyway.
const maxSpreadSigma = 10.0

// Spread draws human-like gaps: log-normal around a target mean, clamped to
// [Min, Max]. Unlike Jitter (uniform, so every gap sits in one narrow band),
// the log-normal is heavy-tailed: most gaps are shorter than Mean, a minority
// are several times longer. A caller spacing N actions per day with
// Mean = 24h/N therefore sees both short gaps and long pauses.
//
// mu is chosen so the UNCLAMPED mean equals Mean (mu = ln(Mean) - sigma^2/2).
// Clamping shifts the realized mean: the Min floor lifts it, the Max cap
// lowers it. Because the distribution's mass is in the right tail, the cap
// usually dominates, so with a tight Max the realized mean lands BELOW Mean
// (the median is always below Mean: Mean*exp(-sigma^2/2)).
//
// Degenerate inputs never panic and never yield a negative value:
//   - Mean <= 0: no distribution to sample; returns the floor (Min, or 0).
//   - Min < 0: treated as 0. Max <= 0: no cap.
//   - Min > Max (Max > 0): the floor wins; every draw is Min.
//   - Sigma zero, negative, NaN or Inf: DefaultSpreadSigma. Sigma above 10 is
//     reduced to 10.
//   - Overflow (huge Mean/Sigma): saturates at Max, or math.MaxInt64 ns.
type Spread struct {
	Mean  time.Duration // target average gap (e.g. 24h/F)
	Sigma float64       // log-space spread; 0 = DefaultSpreadSigma; bigger = burstier
	Min   time.Duration // hard floor
	Max   time.Duration // hard cap (0 = no cap)
}

// Duration draws one gap using the package-global random source.
func (s Spread) Duration() time.Duration {
	return s.DurationFrom(nil)
}

// DurationFrom draws one gap from r, so tests can seed it for deterministic
// output. A nil r uses the package-global source.
func (s Spread) DurationFrom(r *rand.Rand) time.Duration {
	lo, hi := s.bounds()
	if s.Mean <= 0 {
		return lo
	}
	sigma := s.Sigma
	if sigma <= 0 || math.IsNaN(sigma) || math.IsInf(sigma, 0) {
		sigma = DefaultSpreadSigma
	}
	sigma = min(sigma, maxSpreadSigma)

	var z float64
	if r != nil {
		z = r.NormFloat64()
	} else {
		z = rand.NormFloat64()
	}
	mu := math.Log(float64(s.Mean)) - sigma*sigma/2
	x := math.Exp(mu + sigma*z)

	var d time.Duration
	switch {
	case math.IsNaN(x):
		d = s.Mean
	case x >= float64(math.MaxInt64):
		d = math.MaxInt64
	default:
		d = time.Duration(x)
	}
	if d < lo {
		d = lo
	}
	if hi > 0 && d > hi {
		d = hi
	}
	return d
}

// bounds returns the effective floor (>= 0) and cap (0 = none, else >= floor).
func (s Spread) bounds() (lo, hi time.Duration) {
	lo = max(s.Min, 0)
	hi = s.Max
	if hi > 0 && hi < lo {
		hi = lo
	}
	return lo, hi
}
