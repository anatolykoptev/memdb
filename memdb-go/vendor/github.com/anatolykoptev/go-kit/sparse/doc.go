// Package sparse provides SPLADE sparse-embedding clients with a unified interface.
//
// SPLADE (Sparse Lexical and Expansion model for first-stage Ranking) produces
// term-weight vectors over a model's BPE/WordPiece vocabulary instead of dense
// floats. Each output is a pair of parallel arrays (Indices, Values): indices
// are vocab token ids, values are post-ReLU log-saturated weights. Empty
// vectors are valid (e.g. a query that tokenises to stopwords).
//
// Use sparse for:
//
//   - Lexical retrieval over a Postgres pgvector sparsevec column or Qdrant
//     sparse vector — symmetric to dense embedding retrieval but trades
//     semantic generalisation for term-match precision.
//   - Hybrid retrieval pipelines: combine sparse top-k with dense top-k via
//     RRF / weighted fusion (see github.com/anatolykoptev/go-kit/rerank for
//     fusion helpers).
//
// Use dense embeddings (github.com/anatolykoptev/go-kit/embed) when paraphrase
// recall matters more than exact-term match. Use rerank
// (github.com/anatolykoptev/go-kit/rerank) for the second-stage cross-encoder
// pass over fused candidates.
//
// Backends:
//
//   - HTTPSparseEmbedder — POSTs /embed_sparse on the self-hosted Rust
//     embed-server sidecar (the only backend in v1). The endpoint is
//     TEI-style (no /v1/ prefix) and Qdrant-shaped on the response side.
//
// ONNX-local sparse inference is intentionally out of scope for v1 — parallel
// to the embed/ package, an embed/sparse/onnx subpackage would gate behind
// cgo + libonnxruntime + libtokenizers and is deferred until a caller needs
// it. All v1 traffic terminates against embed-server.
//
// All backends share the SparseEmbedder interface (EmbedSparse /
// EmbedSparseQuery / VocabSize / Close), shared retry/backoff (transient
// errors, 429, 5xx), and shared Prometheus metrics under the gokit_sparse_*
// namespace.
//
// Use New for env-driven backend selection. Use NewHTTPSparseEmbedder
// directly for explicit construction. NewClient is the v2 entry point that
// stacks observer hooks, retry, optional cache, optional circuit breaker,
// and optional fallback on top of the underlying backend.
package sparse
