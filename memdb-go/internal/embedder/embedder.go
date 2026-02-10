// Package embedder provides text embedding backends.
package embedder

import "context"

// Embedder generates text embeddings.
type Embedder interface {
	// Embed returns embeddings for the given texts.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension returns the embedding vector dimension.
	Dimension() int
	// Close releases resources (model, tokenizer, HTTP clients).
	Close() error
}
