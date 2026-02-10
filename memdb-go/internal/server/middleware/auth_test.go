package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func testHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAuth_Disabled(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: false})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuth_HealthSkipped(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("secret")})
	handler := mw(testHandler())

	for _, path := range []string{"/health", "/ready"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", path, w.Code)
		}
	}
}

func TestAuth_OptionsSkipped(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("secret")})
	handler := mw(testHandler())

	req := httptest.NewRequest("OPTIONS", "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("secret")})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuth_InvalidFormat(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("secret")})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuth_WrongToken(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("correct-key")})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestAuth_ValidToken(t *testing.T) {
	key := "my-api-key-12345"
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey(key)})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuth_ServiceSecret(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{
		Enabled:       true,
		MasterKeyHash: hashKey("user-key"),
		ServiceSecret: "internal-secret-123",
	})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	req.Header.Set("X-Service-Secret", "internal-secret-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuth_InvalidServiceSecret(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{
		Enabled:       true,
		MasterKeyHash: hashKey("user-key"),
		ServiceSecret: "internal-secret-123",
	})
	handler := mw(testHandler())

	req := httptest.NewRequest("POST", "/product/search", nil)
	req.Header.Set("X-Service-Secret", "wrong-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should fall through to check Bearer token, which is missing → 401
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
