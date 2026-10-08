package stealth

import (
	"time"

	"github.com/anatolykoptev/go-kit/pacing"
)

// BackoffConfig defines exponential backoff parameters with jitter.
type BackoffConfig struct {
	InitialWait time.Duration // base delay for attempt 0
	MaxWait     time.Duration // ceiling
	Multiplier  float64       // growth factor per attempt
	JitterPct   float64       // random variation (0.3 = +/-30%)
}

// DefaultBackoff is the standard backoff for API retry loops.
var DefaultBackoff = BackoffConfig{
	InitialWait: 500 * time.Millisecond,
	MaxWait:     10 * time.Second,
	Multiplier:  2.0,
	JitterPct:   0.3,
}

// Duration returns the backoff delay for the given attempt (0-indexed),
// delegating to pacing.ExponentialBackoff for the canonical jitter calculation.
func (b BackoffConfig) Duration(attempt int) time.Duration {
	return pacing.ExponentialBackoff(b.InitialWait, b.MaxWait, b.Multiplier, b.JitterPct, attempt)
}
