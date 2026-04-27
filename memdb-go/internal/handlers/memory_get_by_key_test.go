package handlers

// memory_get_by_key_test.go — validation-layer tests for NativeGetMemoryByKey.
// Live-Postgres coverage lives in memory_get_by_key_livepg_test.go (build tag livepg).

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newGetByKeyHandler() *Handler {
	// postgres=nil triggers the 503 short-circuit; for validation-only tests we
	// instantiate with a sentinel non-nil pointer would require a real Postgres,
	// so we keep nil and assert validation runs *before* the postgres check.
	// To exercise validation we need postgres to be non-nil; the *db.Postgres
	// pointer can be a zero value here because validation runs before any
	// pool access. We instead assert the 503 branch + use a separate test
	// for raw input shapes.
	return &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func postJSON(t *testing.T, h *Handler, fn http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func TestNativeGetMemoryByKey_PostgresUnavailable(t *testing.T) {
	h := newGetByKeyHandler()
	w := postJSON(t, h, h.NativeGetMemoryByKey, map[string]any{
		"cube_id": "c", "user_id": "u", "key": "/memories/foo",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// validateKey is exercised directly to avoid needing a live Postgres for the
// validation paths.
func TestValidateKey(t *testing.T) {
	cases := []struct {
		name    string
		in      *string
		wantErr bool
	}{
		{"nil_ok", nil, false},
		{"empty_ok", strPtr(""), false},
		{"normal_ok", strPtr("/memories/foo.txt"), false},
		{"too_long", strPtr(strings.Repeat("a", 513)), true},
		{"nul_byte", strPtr("foo\x00bar"), true},
		{"max_len", strPtr(strings.Repeat("a", 512)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateKey(tc.in)
			gotErr := len(errs) > 0
			if gotErr != tc.wantErr {
				t.Fatalf("want err=%v, got %v (errs=%v)", tc.wantErr, gotErr, errs)
			}
		})
	}
}

