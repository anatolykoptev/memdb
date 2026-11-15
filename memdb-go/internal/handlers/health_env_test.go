package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
)

func healthEnv(t *testing.T, cfg *config.Config) string {
	t.Helper()
	h := NewHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if cfg != nil {
		h.SetConfig(cfg)
	}
	w := httptest.NewRecorder()
	h.Health(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body struct {
		Data struct {
			Env string `json:"env"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	return body.Data.Env
}

func TestHealth_ReportsEnv(t *testing.T) {
	if got := healthEnv(t, nil); got != config.EnvProd {
		t.Errorf("no config: env = %q, want prod (fail-closed default)", got)
	}
	if got := healthEnv(t, &config.Config{Env: config.EnvProd}); got != config.EnvProd {
		t.Errorf("prod config: env = %q", got)
	}
	if got := healthEnv(t, &config.Config{Env: config.EnvEval}); got != config.EnvEval {
		t.Errorf("eval config: env = %q", got)
	}
}
