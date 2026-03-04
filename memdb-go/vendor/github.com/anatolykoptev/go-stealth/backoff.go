package stealth

import (
	"math"
	"math/rand"
	"time"
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

// Duration returns the backoff delay for the given attempt (0-indexed).
func (b BackoffConfig) Duration(attempt int) time.Duration {
	base := float64(b.InitialWait) * math.Pow(b.Multiplier, float64(attempt))
	if base > float64(b.MaxWait) {
		base = float64(b.MaxWait)
	}
	jitter := base * b.JitterPct * (2*rand.Float64() - 1)
	return max(time.Duration(base+jitter), 0)
}
