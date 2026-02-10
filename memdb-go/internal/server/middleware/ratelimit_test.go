package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// testLogger and testHandler are defined in auth_test.go

func TestRateLimit_Disabled(t *testing.T) {
	handler := RateLimit(testLogger(), RateLimitConfig{Enabled: false})(testHandler())

	for i := 0; i < 200; i++ {
		req := httptest.NewRequest("POST", "/product/search", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}
}

func TestRateLimit_SkipHealth(t *testing.T) {
	handler := RateLimit(testLogger(), RateLimitConfig{
		Enabled: true, RPS: 1, Burst: 1,
	})(testHandler())

	// Exhaust the bucket
	req := httptest.NewRequest("GET", "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	req = httptest.NewRequest("GET", "/product/search", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Health should still work
	req = httptest.NewRequest("GET", "/health", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health should bypass rate limit, got %d", w.Code)
	}
}

func TestRateLimit_ServiceSecretBypass(t *testing.T) {
	handler := RateLimit(testLogger(), RateLimitConfig{
		Enabled: true, RPS: 1, Burst: 1, ServiceSecret: "secret123",
	})(testHandler())

	// Exhaust the bucket
	req := httptest.NewRequest("POST", "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	req = httptest.NewRequest("POST", "/product/search", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	// This should be 429

	// With service secret should still work
	req = httptest.NewRequest("POST", "/product/search", nil)
	req.Header.Set("X-Service-Secret", "secret123")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("service secret should bypass rate limit, got %d", w.Code)
	}
}

func TestRateLimit_Enforced(t *testing.T) {
	handler := RateLimit(testLogger(), RateLimitConfig{
		Enabled: true, RPS: 1, Burst: 2,
	})(testHandler())

	// First 2 requests should succeed (burst=2)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/product/search", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d should succeed, got %d", i, w.Code)
		}
	}

	// Third request should be rate limited
	req := httptest.NewRequest("POST", "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header")
	}
}

func TestExtractIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if ip := extractIP(req); ip != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %s", ip)
	}
}

func TestExtractIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "10.0.0.1")
	if ip := extractIP(req); ip != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %s", ip)
	}
}

func TestExtractIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	if ip := extractIP(req); ip != "192.168.1.1" {
		t.Errorf("expected 192.168.1.1, got %s", ip)
	}
}
