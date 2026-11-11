package embed

import "context"

// Embedder generates text embeddings.
type Embedder interface {
	// Embed returns embeddings for the given texts (document/storage use case).
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedQuery embeds a single query string (search/retrieval use case).
	// Implementations may apply query-specific prefixes or instructions.
	// Default: delegates to Embed.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	// Dimension returns the embedding vector dimension.
	Dimension() int
	// Close releases resources (model, tokenizer, HTTP clients).
	Close() error
}

// EmbedQueryViaEmbed is a helper that implements EmbedQuery by delegating to Embed.
// Use it in embedder implementations that don't need query-specific behaviour.
func EmbedQueryViaEmbed(ctx context.Context, e Embedder, text string) ([]float32, error) {
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, nil
	}
	return vecs[0], nil
}

// BatchSizer is an optional interface implemented by embedder backends that
// know their own optimal batch size — the maximum number of texts the backend
// accepts in a single Embed call without erroring or degrading.
//
// When the inner embedder of a *Client implements BatchSizer, the client's
// chunking layer uses MaxBatchSize() as the default chunk size instead of the
// hardcoded defaultChunkSize (32). Explicit overrides (WithChunkSize option,
// GOKIT_EMBED_CHUNK_SIZE env) still take priority — the backend's self-reported
// optimum is a default, not a ceiling.
//
// Backends that don't implement BatchSizer fall back to defaultChunkSize,
// preserving existing behaviour.
type BatchSizer interface {
	MaxBatchSize() int
}
