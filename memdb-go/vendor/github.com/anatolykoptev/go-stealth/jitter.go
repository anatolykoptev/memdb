package stealth

import (
	"context"
	"math/rand"
	"time"
)

// Jitter defines a random delay range for anti-fingerprinting.
type Jitter struct {
	Min time.Duration
	Max time.Duration
}

// DefaultJitter is 500ms-2.5s, suitable for most scraping.
var DefaultJitter = Jitter{
	Min: 500 * time.Millisecond,
	Max: 2500 * time.Millisecond,
}

// Sleep pauses for a random duration between Min and Max.
// Returns ctx.Err() if the context is cancelled during the wait.
func (j Jitter) Sleep(ctx context.Context) error {
	d := j.Min + time.Duration(rand.Int63n(int64(j.Max-j.Min)))
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
