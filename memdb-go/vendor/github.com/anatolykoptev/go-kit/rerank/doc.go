// Package rerank provides a Cohere-compatible cross-encoder rerank client.
//
// Compatible with:
//   - embed-server self-hosted (http://embed-server:8082/v1/rerank)
//   - HuggingFace text-embeddings-inference (TEI)
//   - Cohere hosted (https://api.cohere.com/v1/rerank, APIKey required)
//   - Jina AI, Voyage AI, Mixedbread AI (APIKey required)
//
// The client is best-effort: any error (timeout, non-2xx, decode) returns
// the input unchanged with a slog.Warn. Pipelines using this package MUST
// tolerate the reranker being absent.
//
// Zero-value URL in Config disables the client entirely — Rerank returns
// input unchanged, Available returns false.
package rerank
