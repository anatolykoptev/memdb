package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/rpc"
)

// newMockPython creates a mock Python backend that returns a fixed proxied response.
// The caller must call Close() on the returned server when done.
func newMockPython(t *testing.T) (*httptest.Server, *rpc.PythonClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "proxied",
			"data":    map[string]any{},
		})
	}))
	client := rpc.NewPythonClient(srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return srv, client
}

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// assertServiceUnavailable checks that the response has status 503.
func assertServiceUnavailable(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 503, got %d; body: %s", resp.StatusCode, string(body))
	}
}

// assertValidationError checks that the response has status 400 and the body
// contains the expected substring.
func assertValidationError(t *testing.T, w *httptest.ResponseRecorder, wantSubstr string) {
	t.Helper()
	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d; body: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, wantSubstr) {
		t.Fatalf("expected message containing %q, got %q", wantSubstr, msg)
	}
}

// TestNativeSearch_NoEmbedder verifies that when the searchService is nil,
// a valid search request returns 503 (no proxy fallback).
func TestNativeSearch_NoEmbedder(t *testing.T) {
	h := &Handler{logger: discardLogger()}

	body := `{"query":"test","user_id":"memos","top_k":6}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.NativeSearch(w, req)

	assertServiceUnavailable(t, w)
}

// TestNativeSearch_NoPostgres verifies that when postgres is nil (searchService
// can't search), a valid search request returns 503.
func TestNativeSearch_NoPostgres(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	h.SetEmbedder(embedder.NewVoyageClient("fake-key", "voyage-4-lite", discardLogger()))

	body := `{"query":"test","user_id":"memos","top_k":6}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.NativeSearch(w, req)

	assertServiceUnavailable(t, w)
}

// TestNativeSearch_FineMode_NoService verifies that fine mode without a
// searchService returns 503 (not proxied).
func TestNativeSearch_FineMode(t *testing.T) {
	h := &Handler{logger: discardLogger()}

	body := `{"query":"test","user_id":"memos","top_k":6,"mode":"fine"}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.NativeSearch(w, req)

	assertServiceUnavailable(t, w)
}

// TestNativeSearch_InternetSearch_NoService verifies that internet_search=true
// without a searchService returns 503 (not proxied).
func TestNativeSearch_InternetSearch(t *testing.T) {
	h := &Handler{logger: discardLogger()}

	body := `{"query":"test","user_id":"memos","top_k":6,"internet_search":true}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.NativeSearch(w, req)

	assertServiceUnavailable(t, w)
}

// TestNativeSearch_ValidationError verifies that an empty query returns a 400
// validation error.
func TestNativeSearch_ValidationError(t *testing.T) {
	h := &Handler{logger: discardLogger()}

	body := `{"query":"","user_id":"memos","top_k":6}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.NativeSearch(w, req)

	assertValidationError(t, w, "query is required")
}

// TestNativeSearch_MissingUserID verifies that a request without user_id
// returns a 400 validation error.
func TestNativeSearch_MissingUserID(t *testing.T) {
	h := &Handler{logger: discardLogger()}

	body := `{"query":"test","top_k":6}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.NativeSearch(w, req)

	assertValidationError(t, w, "user_id is required")
}
