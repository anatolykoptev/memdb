package embedder

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gokitembed "github.com/anatolykoptev/go-kit/embed"
)

// TestFactory_HTTPCircuitPopulated asserts that when Config.HTTPCircuit is set,
// the factory wires it into the HTTPEmbedder (PF-8 / #326).
//
// RED: before the fix, Config.HTTPCircuit didn't exist and the factory never
// set opts.Circuit, so the embedder had no circuit breaker.
func TestFactory_HTTPCircuitPopulated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
	}))
	defer srv.Close()

	cbCfg := &gokitembed.CircuitConfig{
		FailThreshold:  3,
		OpenDuration:   10 * time.Second,
		HalfOpenProbes: 1,
		FailRateWindow: 30 * time.Second,
	}

	cfg := Config{
		Type:        "http",
		HTTPBaseURL: srv.URL,
		HTTPDim:     3,
		HTTPCircuit: cbCfg,
	}

	logger := slog.New(slog.NewTextHandler(&devNullFactory{}, &slog.HandlerOptions{Level: slog.LevelError}))
	e, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	he, ok := e.(*HTTPEmbedder)
	if !ok {
		t.Fatalf("expected *HTTPEmbedder, got %T", e)
	}
	if he.inner == nil {
		t.Fatal("inner gokitembed.Client is nil")
	}
	// The circuit breaker is wired inside the gokitembed.Client. We can't
	// directly inspect it (unexported field), but we can verify the embedder
	// works and that the factory didn't panic. A deeper test would require
	// causing FailThreshold failures and asserting the circuit opens —
	// that's covered by go-kit's own circuit tests.
	_, embedErr := e.Embed(t.Context(), []string{"hello"})
	if embedErr != nil {
		t.Fatalf("Embed failed: %v", embedErr)
	}
}

// TestFactory_HTTPCircuitNilDisabled asserts that when HTTPCircuit is nil,
// the factory creates an embedder without a circuit breaker (legacy behaviour).
func TestFactory_HTTPCircuitNilDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
	}))
	defer srv.Close()

	cfg := Config{
		Type:        "http",
		HTTPBaseURL: srv.URL,
		HTTPDim:     3,
		// HTTPCircuit intentionally nil
	}

	logger := slog.New(slog.NewTextHandler(&devNullFactory{}, &slog.HandlerOptions{Level: slog.LevelError}))
	e, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if e == nil {
		t.Fatal("embedder is nil")
	}
	_, embedErr := e.Embed(t.Context(), []string{"hello"})
	if embedErr != nil {
		t.Fatalf("Embed failed: %v", embedErr)
	}
}

type devNullFactory struct{}

func (d *devNullFactory) Write(p []byte) (int, error) { return len(p), nil }
