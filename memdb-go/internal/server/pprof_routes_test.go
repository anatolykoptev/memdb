package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

// TestPprofHandler_LegacyHeader verifies that X-Internal-Service is also accepted.
func TestPprofHandler_LegacyHeader(t *testing.T) {
	h := pprofHandler(testPprofSecret)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("X-Internal-Service", testPprofSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 with X-Internal-Service header, got %d", w.Code)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
