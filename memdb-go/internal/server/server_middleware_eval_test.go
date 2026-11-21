package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	mw "github.com/anatolykoptev/memdb/memdb-go/internal/server/middleware"
)

// TestApplyMiddleware_EvalMarkerRejectedInProd drives the real middleware
// stack: a prod instance must refuse a harness-marked write before it reaches
// any handler, and an eval instance must let it through.
func TestApplyMiddleware_EvalMarkerRejectedInProd(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	for _, tc := range []struct {
		env       string
		wantCode  int
		wantReach bool
	}{
		{config.EnvProd, http.StatusForbidden, false},
		{config.EnvEval, http.StatusOK, true},
	} {
		reached = false
		h := applyMiddleware(next, &config.Config{Env: tc.env}, nil, logger)
		req := httptest.NewRequest(http.MethodPost, "/product/add", nil)
		req.Header.Set(mw.EvalHarnessHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.wantCode || reached != tc.wantReach {
			t.Errorf("env=%s: code=%d reached=%v, want code=%d reached=%v",
				tc.env, w.Code, reached, tc.wantCode, tc.wantReach)
		}
	}
}
