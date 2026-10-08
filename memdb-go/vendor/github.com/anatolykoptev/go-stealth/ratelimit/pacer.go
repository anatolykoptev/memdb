// Package ratelimit provides token bucket rate limiters and per-key pacing.
//
// The KeyedPacer type and its helpers are thin aliases over go-kit/pacing,
// preserving this package's API for existing consumers (go-twitter, go-engine)
// while consolidating the implementation in go-kit/pacing. New code should
// import go-kit/pacing directly.
package ratelimit

import (
	"time"

	"github.com/anatolykoptev/go-kit/pacing"
)

// KeyedPacer is an alias for pacing.KeyedPacer, preserving the stealth
// ratelimit package API. See pacing.KeyedPacer for documentation.
type KeyedPacer = pacing.KeyedPacer

// PacerOption is an alias for pacing.PacerOption.
type PacerOption = pacing.PacerOption

// WithPacerClock is an alias for pacing.WithPacerClock.
func WithPacerClock(clock func() time.Time) PacerOption {
	return pacing.WithPacerClock(clock)
}

// NewKeyedPacer delegates to pacing.NewKeyedPacer, preserving the stealth
// ratelimit package API for existing consumers.
func NewKeyedPacer(minDelay, randomDelay time.Duration, opts ...PacerOption) *KeyedPacer {
	return pacing.NewKeyedPacer(minDelay, randomDelay, opts...)
}
