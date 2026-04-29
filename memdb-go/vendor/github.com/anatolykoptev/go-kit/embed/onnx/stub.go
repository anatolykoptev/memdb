//go:build !cgo

package onnx

import (
	"context"
	"errors"
	"log/slog"
	"time"

	parentembed "github.com/anatolykoptev/go-kit/embed"
)

// Embedder is a no-cgo stub that always returns ErrNoCGO.
// Mirrors the memdb-go onnx_stub.go behaviour so non-cgo builds still link.
type Embedder struct{}

// ErrNoCGO is returned by every method when the package is built without cgo.
var ErrNoCGO = errors.New("onnx embedder requires CGO (libtokenizers + libonnxruntime)")

// New returns ErrNoCGO when CGO is disabled.
func New(_ Config, _ *slog.Logger) (*Embedder, error) {
	return nil, ErrNoCGO
}

// Embed records the failed call in metrics and returns ErrNoCGO.
func (e *Embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	recordEmbed("onnx", "error", len(texts), 0*time.Second)
	return nil, ErrNoCGO
}

// EmbedQuery returns ErrNoCGO.
func (e *Embedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return nil, ErrNoCGO
}

// Dimension returns 0 — the stub has no model loaded.
func (e *Embedder) Dimension() int { return 0 }

// Close is a no-op for the stub.
func (e *Embedder) Close() error { return nil }

// Compile-time interface check: stub Embedder satisfies parent embed.Embedder.
var _ parentembed.Embedder = (*Embedder)(nil)
