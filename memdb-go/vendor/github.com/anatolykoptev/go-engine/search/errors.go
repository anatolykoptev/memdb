package search

import (
	"fmt"
	"time"
)

// ErrRateLimited is returned when a search engine blocks the request
// due to rate limiting, CAPTCHA, or IP-based throttling.
type ErrRateLimited struct {
	Engine     string
	RetryAfter time.Duration // 0 = unknown
}

func (e *ErrRateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited by %s (retry after %s)", e.Engine, e.RetryAfter)
	}
	return "rate limited by " + e.Engine
}
