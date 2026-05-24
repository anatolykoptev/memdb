package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// APIError is a structured error returned by LLM API calls.
// Use errors.As to extract status code, body, and error type from callers.
type APIError struct {
	StatusCode int
	Body       string
	Type       string // parsed from JSON response if available (e.g. "rate_limit_error")
	Retryable  bool
	// RetryAfter is the server-suggested delay before retry, parsed from
	// the HTTP Retry-After response header (RFC 7231 §7.1.3). Zero when
	// the header is absent or unparseable. Callers retrying APIError
	// should honour this value when non-zero instead of their own
	// backoff schedule.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("llm: HTTP %d (%s): %s", e.StatusCode, e.Type, e.Body)
	}
	return fmt.Sprintf("llm: HTTP %d: %s", e.StatusCode, e.Body)
}

func newAPIError(statusCode int, body string, retryable bool, retryAfter time.Duration) *APIError {
	e := &APIError{
		StatusCode: statusCode,
		Body:       body,
		Retryable:  retryable,
		RetryAfter: retryAfter,
	}
	// Try to extract error type from JSON body (OpenAI/Anthropic format).
	var parsed struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil && parsed.Error.Type != "" {
		e.Type = parsed.Error.Type
	}
	return e
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func asRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

// parseRetryAfter parses the HTTP Retry-After header per RFC 7231. The
// value can be either a non-negative integer number of seconds or an
// HTTP-date. Returns 0 on empty or unparseable input.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	// Seconds form.
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form.
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
