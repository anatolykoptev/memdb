package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gzipMagic is the two-byte magic number at the start of every gzip stream.
// pprof binary profiles are gzip-compressed.
var gzipMagic = []byte{0x1f, 0x8b}

const testPprofSecret = "test-pprof-secret-xyz"

// TestPprofHandler_NoAuth verifies that /debug/pprof/ returns 401 without X-Service-Secret.
func TestPprofHandler_NoAuth(t *testing.T) {
	h := pprofHandler(testPprofSecret)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

// TestPprofHandler_WrongSecret verifies that /debug/pprof/ returns 401 for a wrong secret.
func TestPprofHandler_WrongSecret(t *testing.T) {
	h := pprofHandler(testPprofSecret)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("X-Service-Secret", "wrong-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

// TestPprofHandler_ValidSecret verifies that /debug/pprof/ returns 200 with a valid X-Service-Secret.
func TestPprofHandler_ValidSecret(t *testing.T) {
	h := pprofHandler(testPprofSecret)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("X-Service-Secret", testPprofSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Profile Descriptions") {
		t.Errorf("want pprof index HTML containing 'Profile Descriptions', got: %s", body[:min(len(body), 200)])
	}
}

// TestPprofHandler_LegacyHeader verifies that X-Internal-Service is also accepted
// and that the response actually reaches pprofHandler (not an early rejection).
func TestPprofHandler_LegacyHeader(t *testing.T) {
	h := pprofHandler(testPprofSecret)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("X-Internal-Service", testPprofSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 with X-Internal-Service header, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Profile Descriptions") {
		t.Error("legacy header reached pprofHandler but didn't fall through to pprof index")
	}
}

// TestPprofHandler_SubRoute_Heap verifies that the trailing-slash subtree match
// works for actual pprof endpoints (not just the /debug/pprof/ index).
// Without this, a future change from "/debug/pprof/" to "/debug/pprof" would
// silently break /heap, /goroutine, /profile.
func TestPprofHandler_SubRoute_Heap(t *testing.T) {
	h := pprofHandler(testPprofSecret)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil)
	req.Header.Set("X-Service-Secret", testPprofSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for /debug/pprof/heap, got %d; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("want non-empty body for heap profile")
	}
	if len(body) < 2 || body[0] != gzipMagic[0] || body[1] != gzipMagic[1] {
		t.Errorf("want gzip-compressed pprof profile (magic \\x1f\\x8b), got first bytes: %#v", body[:min(2, len(body))])
	}
}

// TestPprofHandler_NoSecret verifies 503 when INTERNAL_SERVICE_SECRET is not configured.
func TestPprofHandler_NoSecret(t *testing.T) {
	h := pprofHandler("") // empty secret = disabled

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when secret not configured, got %d", w.Code)
	}
}

// TestRegisterRoutes_PprofRegistered verifies /debug/pprof/ is registered in the mux.
func TestRegisterRoutes_PprofRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil, testPprofSecret)

	// Without auth → 401 (registered + gated)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("/debug/pprof/ without auth: want 401, got %d", w.Code)
	}

	// With auth → 200
	req2 := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req2.Header.Set("X-Service-Secret", testPprofSecret)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("/debug/pprof/ with auth: want 200, got %d", w2.Code)
	}
}
