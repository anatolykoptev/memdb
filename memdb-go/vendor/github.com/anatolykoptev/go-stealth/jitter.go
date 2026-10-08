package stealth

import (
	"github.com/anatolykoptev/go-kit/pacing"
)

// Jitter is an alias for pacing.Jitter, preserving the stealth-package API for
// existing callers (go-twitter vendor). New code should import pacing directly.
type Jitter = pacing.Jitter

// DefaultJitter is the canonical anti-fingerprint jitter (500ms–2.5s), backed
// by pacing.DefaultJitter. Callers that need a different range should construct
// their own pacing.Jitter.
var DefaultJitter = pacing.DefaultJitter
