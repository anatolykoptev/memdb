// Package llm — parse_429_test.go: tests for the 429-retry error format
// (BUG C) and the typed StatusError (BUG A).
//
// BUG C: the error string was "llm: status 429 after 429 retry" — the literal
// "429" in the format string occupied the position where the retry/attempt
// count belongs. Fix: render the actual retry count.
//
// BUG A: fetchChatContent must return a typed *StatusError so
// shouldFallbackToFast (handlers/add_fine.go) can detect it via errors.As
// instead of fragile string matching, enabling the fine→fast degraded
// fallback that prevents data loss on ladder exhaustion.
package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchChatContent_429RetryExhaustion_ErrorFormat verifies BUG C: the
// error message after a 429-retry-exhaustion must show the real retry count
// (1), not the status code (429) in the retry-count position.
//
// Pre-fix the message read "llm: status 429 after 429 retry" (two 429s —
// the second was a literal in the format string, not the attempt count).
// Post-fix it reads "llm: status 429 after 1 retry (429 backoff)".
func TestFetchChatContent_429RetryExhaustion_ErrorFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewSimpleClient(srv.URL, "", "test-model")
	o := resolveStructuredOpts(nil)

	_, _, _, err := fetchChatContent(context.Background(), c, []Message{
		{Role: "user", Content: "hi"},
	}, o)
	if err == nil {
		t.Fatal("expected error from 429-retry-exhaustion, got nil")
	}
	// BUG C: the message must NOT contain "after 429 retry" (status code
	// in the retry-count position). It MUST contain "after 1 retry" (the
	// actual retry count).
	if strings.Contains(err.Error(), "after 429 retry") {
		t.Errorf("BUG C not fixed: error still has status code as retry count: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "after 1 retry") {
		t.Errorf("expected 'after 1 retry' in error message, got: %q", err.Error())
	}
}

// TestFetchChatContent_429RetryExhaustion_ReturnsStatusError verifies BUG A:
// the error from a 429-retry-exhaustion must be a typed *StatusError so
// shouldFallbackToFast can detect it via errors.As.
func TestFetchChatContent_429RetryExhaustion_ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewSimpleClient(srv.URL, "", "test-model")
	o := resolveStructuredOpts(nil)

	_, _, _, err := fetchChatContent(context.Background(), c, []Message{
		{Role: "user", Content: "hi"},
	}, o)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.Code != http.StatusTooManyRequests {
		t.Errorf("StatusError.Code = %d, want %d", se.Code, http.StatusTooManyRequests)
	}
	if se.Retries != 1 {
		t.Errorf("StatusError.Retries = %d, want 1", se.Retries)
	}
}

// TestFetchChatContent_Non429HTTPError_ReturnsStatusError verifies that a
// non-429 HTTP error (e.g. 502) also returns a typed *StatusError.
func TestFetchChatContent_Non429HTTPError_ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewSimpleClient(srv.URL, "", "test-model")
	o := resolveStructuredOpts(nil)

	_, _, _, err := fetchChatContent(context.Background(), c, []Message{
		{Role: "user", Content: "hi"},
	}, o)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.Code != http.StatusBadGateway {
		t.Errorf("StatusError.Code = %d, want %d", se.Code, http.StatusBadGateway)
	}
	if se.Retries != 0 {
		t.Errorf("StatusError.Retries = %d, want 0 (no 429-retry for 502)", se.Retries)
	}
}

// TestStatusError_ErrorFormat verifies the Error() method renders the retry
// count correctly for both the 429-retry case and the plain HTTP error case.
func TestStatusError_ErrorFormat(t *testing.T) {
	cases := []struct {
		name string
		err  *StatusError
		want string
	}{
		{"429 after 1 retry", &StatusError{Code: 429, Retries: 1}, "llm: status 429 after 1 retry (429 backoff)"},
		{"502 no retry", &StatusError{Code: 502, Retries: 0}, "llm: status 502"},
		{"500 no retry", &StatusError{Code: 500, Retries: 0}, "llm: status 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}
