// Package embedder — sparse_http.go: thin wrapper around go-kit/sparse.Client v2.
//
// Why a wrapper instead of using gokit/sparse directly across the codebase:
//   - Preserves the local *embedder.SparseEmbedder type that the handlers and
//     server-init code already pass around, so the gokit dependency stays
//     localised in this file.
//   - Lets us layer pgvector-specific helpers (FormatSparseVector below) next
//     to the embedder type that produces them.
//   - Future-proofs: when an ONNX-local sparse path lands in gokit
//     (sparse/onnx subpackage, currently deferred per gokit doc.go), the
//     wrapper picks it up by switching the constructor — no caller changes.
//
// gokit/sparse v0.36.0 already provides:
//   - retry with backoff on 429/5xx/transient
//   - circuit breaker (off by default; opt-in via WithCircuit)
//   - cache adapter slot (SparseCache interface; Redis adapter TBD)
//   - fallback chain to a secondary client
//   - observer hooks (OnBeforeEmbed/OnAfterEmbed/OnRetry/OnCircuitTransition)
//   - Prometheus metrics under gokit_sparse_* namespace
//
// FormatSparseVector stays in this package because pgvector's sparsevec
// literal format ("{i:v,...}/dim" with sorted ascending indices) is not a
// generic concern — gokit returns the raw (Indices, Values) pair and lets
// each storage backend (pgvector, qdrant, ES) format as needed.
package embedder

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gks "github.com/anatolykoptev/go-kit/sparse"
)

// SparseVector is a transparent alias to go-kit/sparse.SparseVector so callers
// that imported embedder.SparseVector keep compiling. Both slices are owned by
// the embedder; callers must not mutate them.
type SparseVector = gks.SparseVector

// SparseEmbedder is a thin wrapper around go-kit/sparse.Client v2.
//
// Concurrent-safe (the underlying gokit Client is). Nil-safe across all
// methods so server.go can guard with `if h.sparseEmbedder != nil` without
// extra ceremony.
type SparseEmbedder struct {
	client *gks.Client
}

const (
	defaultSparseTimeout = 30 * time.Second
	defaultSparseModel   = "splade-v3-distilbert"
)

// NewSparseEmbedder constructs the wrapper.
//
// baseURL must NOT include the path; gokit appends /embed_sparse internally.
// model="" defaults to splade-v3-distilbert (the only SPLADE model loaded by
// embed-server today). logger=nil falls back to slog.Default() inside gokit.
//
// Returns nil if gokit's NewClient fails (e.g. empty baseURL); callers
// already nil-check h.sparseEmbedder, so this preserves the existing
// "sparse disabled, dense-only retrieval" graceful-degradation contract.
func NewSparseEmbedder(baseURL, model string, logger *slog.Logger) *SparseEmbedder {
	if logger == nil {
		logger = slog.Default()
	}
	opts := []gks.Opt{
		gks.WithLogger(logger),
		gks.WithTimeout(defaultSparseTimeout),
	}
	if model == "" {
		model = defaultSparseModel
	}
	opts = append(opts, gks.WithModel(model))

	c, err := gks.NewClient(strings.TrimRight(baseURL, "/"), opts...)
	if err != nil {
		logger.Error("sparse: gokit NewClient failed; SPLADE disabled",
			slog.String("url", baseURL), slog.Any("error", err))
		return nil
	}
	return &SparseEmbedder{client: c}
}

// EmbedSparse embeds documents (ingest path). Returns one SparseVector per
// input text, in input order. Empty input returns (nil, nil) without hitting
// the backend.
func (s *SparseEmbedder) EmbedSparse(ctx context.Context, texts []string) ([]SparseVector, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	return s.client.EmbedSparse(ctx, texts)
}

// EmbedSparseQuery embeds a single query string (retrieval path).
// Implementations may apply query-specific prefixes; the gokit HTTP backend
// currently delegates to EmbedSparse.
func (s *SparseEmbedder) EmbedSparseQuery(ctx context.Context, text string) (SparseVector, error) {
	if s == nil || s.client == nil {
		return SparseVector{}, nil
	}
	return s.client.EmbedSparseQuery(ctx, text)
}

// VocabSize returns the model's vocabulary size (sparse-space dimension).
// Used by FormatSparseVector and by callers that need to allocate dim-sized
// buffers. Returns 0 when the wrapper is nil — callers needing the dim
// should fall back to a constant (30522 for SPLADE-v3-distilbert).
func (s *SparseEmbedder) VocabSize() int {
	if s == nil || s.client == nil {
		return 0
	}
	return s.client.VocabSize()
}

// Model returns the configured SPLADE model name (for telemetry / cache keys).
func (s *SparseEmbedder) Model() string {
	if s == nil || s.client == nil {
		return ""
	}
	return s.client.Model()
}

// Close releases backend resources held by the gokit Client.
func (s *SparseEmbedder) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// FormatSparseVector serializes a SparseVector into pgvector's sparsevec
// literal: "{idx1:val1,idx2:val2,...}/dim". Empty vector returns "{}/dim".
//
// pgvector requires sorted ascending indices. SPLADE may return them in any
// order, so we do an insertion sort here (typically <100 entries — beats
// sort.Slice's allocation cost).
func FormatSparseVector(v SparseVector, dim int) string {
	if len(v.Indices) == 0 {
		return fmt.Sprintf("{}/%d", dim)
	}
	idxs := append([]uint32(nil), v.Indices...)
	vals := append([]float32(nil), v.Values...)
	for i := 1; i < len(idxs); i++ {
		for j := i; j > 0 && idxs[j-1] > idxs[j]; j-- {
			idxs[j-1], idxs[j] = idxs[j], idxs[j-1]
			vals[j-1], vals[j] = vals[j], vals[j-1]
		}
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i, ix := range idxs {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%d:%g", ix, vals[i])
	}
	sb.WriteByte('}')
	fmt.Fprintf(&sb, "/%d", dim)
	return sb.String()
}
