// Package embed provides text embedding backends with a unified interface.
//
// Backends:
//
//   - HTTPEmbedder — OpenAI-compatible /v1/embeddings clients (e.g. the
//     self-hosted embed-server sidecar serving multilingual-e5-large +
//     jina-code-v2 on the internal Docker network).
//   - OllamaClient — Ollama /api/embed (batch, ≥ 0.3.6).
//   - VoyageClient — Voyage AI hosted /v1/embeddings.
//   - ONNX (build tag "cgo") — local ONNX Runtime inference; lives in
//     subpackage github.com/anatolykoptev/go-kit/embed/onnx so callers
//     who don't need cgo never link libonnxruntime / libtokenizers.
//
// All backends share the [Embedder] interface (Embed / EmbedQuery /
// Dimension / Close), shared retry/backoff (transient errors, 429, 5xx),
// and shared Prometheus metrics under the embed_* namespace.
//
// Use [New] for env-driven backend selection (Type ∈ {http, ollama, voyage,
// onnx}). Use [NewHTTPEmbedder] / [NewOllamaClient] / [NewVoyageClient]
// directly for explicit construction.
//
// Multi-model wiring uses [Registry], which maps model names (e.g.
// "multilingual-e5-large", "jina-code-v2") to embedders and falls back to
// a designated default when the lookup name is empty.
package embed
