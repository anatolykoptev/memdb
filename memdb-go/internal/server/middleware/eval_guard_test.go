package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, isEval bool, header string) *httptest.ResponseRecorder {
	t.Helper()
	h := EvalHarnessGuard(testLogger(), isEval)(testHandler())
	req := httptest.NewRequest(http.MethodPost, "/product/add", nil)
	if header != "" {
		req.Header.Set(EvalHarnessHeader, header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestEvalHarnessGuard_ProdRejectsMarkedWrite(t *testing.T) {
	if w := serve(t, false, "1"); w.Code != http.StatusForbidden {
		t.Fatalf("prod + eval marker: got %d, want 403", w.Code)
	}
}

func TestEvalHarnessGuard_ProdAcceptsUnmarkedWrite(t *testing.T) {
	if w := serve(t, false, ""); w.Code != http.StatusOK {
		t.Fatalf("prod + no marker: got %d, want 200", w.Code)
	}
}

func TestEvalHarnessGuard_EvalAcceptsMarkedWrite(t *testing.T) {
	if w := serve(t, true, "1"); w.Code != http.StatusOK {
		t.Fatalf("eval + marker: got %d, want 200", w.Code)
	}
}
