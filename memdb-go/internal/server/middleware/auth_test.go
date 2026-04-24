package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func testHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAuth_Disabled(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: false})
	handler := mw(testHandler())

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
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
		req := httptest.NewRequest(http.MethodGet, path, nil)
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

	req := httptest.NewRequest(http.MethodOptions, "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("secret")})
	handler := mw(testHandler())

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuth_InvalidFormat(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{Enabled: true, MasterKeyHash: hashKey("secret")})
	handler := mw(testHandler())

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
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

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
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

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
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

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
	req.Header.Set("X-Service-Secret", "internal-secret-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuth_ServiceSecretLegacyHeader(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{
		Enabled:       true,
		MasterKeyHash: hashKey("user-key"),
		ServiceSecret: "internal-secret-123",
	})
	handler := mw(testHandler())

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
	req.Header.Set("X-Internal-Service", "internal-secret-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with X-Internal-Service header, got %d", w.Code)
	}
}

func TestAuth_InvalidServiceSecret(t *testing.T) {
	mw := Auth(testLogger(), AuthConfig{
		Enabled:       true,
		MasterKeyHash: hashKey("user-key"),
		ServiceSecret: "internal-secret-123",
	})
	handler := mw(testHandler())

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
	req.Header.Set("X-Service-Secret", "wrong-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should fall through to check Bearer token, which is missing → 401
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ── CheckServiceSecret unit tests ─────────────────────────────────────────────

func TestCheckServiceSecret_NotPresented(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	presented, valid := CheckServiceSecret(req, "secret")
	if presented {
		t.Error("expected presented=false when no header is set")
	}
	if valid {
		t.Error("expected valid=false when no header is set")
	}
}

func TestCheckServiceSecret_EmptyExpected(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderServiceSecret, "anything")
	presented, valid := CheckServiceSecret(req, "")
	if presented {
		t.Error("expected presented=false when expected is empty")
	}
	if valid {
		t.Error("expected valid=false when expected is empty")
	}
}

func TestCheckServiceSecret_Valid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderServiceSecret, "my-secret")
	presented, valid := CheckServiceSecret(req, "my-secret")
	if !presented {
		t.Error("expected presented=true")
	}
	if !valid {
		t.Error("expected valid=true for correct secret")
	}
}

func TestCheckServiceSecret_WrongValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderServiceSecret, "wrong")
	presented, valid := CheckServiceSecret(req, "correct")
	if !presented {
		t.Error("expected presented=true (header was set)")
	}
	if valid {
		t.Error("expected valid=false for wrong secret")
	}
}

func TestCheckServiceSecret_LegacyHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderInternalServiceLeg, "legacy-secret")
	presented, valid := CheckServiceSecret(req, "legacy-secret")
	if !presented {
		t.Error("expected presented=true via legacy header")
	}
	if !valid {
		t.Error("expected valid=true via legacy header")
	}
}

func TestCheckServiceSecret_CanonicalWinsOverLegacy(t *testing.T) {
	// When both headers are set, canonical header value is used.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderServiceSecret, "canonical")
	req.Header.Set(HeaderInternalServiceLeg, "legacy")
	presented, valid := CheckServiceSecret(req, "canonical")
	if !presented || !valid {
		t.Error("canonical header should win when both are set")
	}
}

// ── WriteAuthError unit tests ──────────────────────────────────────────────────

func TestWriteAuthError_StatusAndBody(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAuthError(w, http.StatusUnauthorized, "missing token")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":401`) {
		t.Errorf("body missing code field: %s", body)
	}
	if !strings.Contains(body, `missing token`) {
		t.Errorf("body missing message: %s", body)
	}
	if !strings.Contains(body, `"data":null`) {
		t.Errorf("body missing data:null: %s", body)
	}
}

func TestWriteAuthError_403(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAuthError(w, http.StatusForbidden, "invalid API key")

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"code":403`) {
		t.Errorf("body missing code 403: %s", w.Body.String())
	}
}
