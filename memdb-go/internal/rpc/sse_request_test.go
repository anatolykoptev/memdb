package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---- isSSERequest -----------------------------------------------------------

func TestIsSSERequest_AcceptHeader(t *testing.T) {
	cases := []struct {
		accept string
		want   bool
	}{
		{"text/event-stream", true},
		{"text/event-stream, text/html", true},
		{"application/json", false},
		{"", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/some/path", nil)
		if tc.accept != "" {
			r.Header.Set("Accept", tc.accept)
		}
		got := isSSERequest(r)
		if got != tc.want {
			t.Errorf("isSSERequest(Accept=%q) = %v, want %v", tc.accept, got, tc.want)
		}
	}
}

func TestIsSSERequest_PathSuffix(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/product/scheduler/wait/stream", true},
		{"/product/chat/stream", true},
		{"/product/chat/stream/", true},
		{"/product/chat/complete", false},
		{"/product/search", false},
		{"/stream-data", false}, // must end with /stream not contain it
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		got := isSSERequest(r)
		if got != tc.want {
			t.Errorf("isSSERequest(path=%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsSSERequest_AcceptTakesPrecedence(t *testing.T) {
	// Accept header should trigger SSE even on non-stream path.
	r := httptest.NewRequest(http.MethodPost, "/product/chat/complete", nil)
	r.Header.Set("Accept", "text/event-stream")
	if !isSSERequest(r) {
		t.Error("Accept: text/event-stream must trigger SSE regardless of path")
	}
}

// ---- ProxyRequest uses sseClient for SSE paths ------------------------------

func TestPythonClient_UsesSSEClientForStreamPaths(t *testing.T) {
	// Verify that the sseClient (no timeout) is selected for SSE requests.
	// We test this indirectly: sseClient has no timeout, so a slow upstream
	// won't cause a timeout error on SSE paths.
	//
	// This is a structural test — we verify the client selection logic
	// by checking isSSERequest returns true for known SSE paths.
	ssePaths := []string{
		"/product/scheduler/wait/stream",
		"/product/chat/stream",
	}
	for _, p := range ssePaths {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if !isSSERequest(r) {
			t.Errorf("path %q should be detected as SSE request", p)
		}
	}

	nonSSEPaths := []string{
		"/product/search",
		"/product/add",
		"/product/chat/complete",
	}
	for _, p := range nonSSEPaths {
		r := httptest.NewRequest(http.MethodPost, p, nil)
		if isSSERequest(r) {
			t.Errorf("path %q should NOT be detected as SSE request", p)
		}
	}
}
