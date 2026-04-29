// Package rerank provides cross-encoder reranking and rank-fusion primitives
// for hybrid search.
//
// # Cross-encoder reranking
//
// Cohere-compatible client. Compatible with:
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
//
// # Fusion algorithms
//
// Four canonical fusion methods cover the rank-only / score-aware × uniform /
// weighted quadrants:
//
//   - RRF / WeightedRRF — rank-only fusion (Cormack-Clarke 2009).
//     Equivalent to Qdrant's `rrf` and Elasticsearch's `rrf_retriever`.
//
//   - DBSF — Distribution-Based Score Fusion (Qdrant 1.11+ convention).
//     z-score normalize each list (population stddev), clip ±3σ, sum.
//     Recommended ≥10 items per list for stable σ.
//
//   - LinearMinMax — MinMax-normalized weighted sum.
//     Equivalent to Elasticsearch `linear_retriever` and Weaviate's
//     `relativeScoreFusion` when weights are uniform.
//
// # Choosing a fusion method
//
//   - Zero-shot, no tuning data, mismatched score scales → RRF
//   - Known per-retriever reliability → WeightedRRF
//   - Score magnitudes carry signal (BM25 confidence, etc.) → DBSF
//   - Calibrated weights from grid search → LinearMinMax
//
// # Construction
//
// Use package-level functions (RRF, WeightedRRF, DBSF, LinearMinMax) for
// one-shot calls with hand-coded args. Use constructors (NewRRF,
// NewWeightedRRF, NewDBSF, NewLinearMinMax) when configuration comes from
// env / config files / a database — constructors return errors instead of
// panicking, and accept WithTopK for output capping.
package rerank
