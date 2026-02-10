//go:build cgo

package embedder

// Compile-time interface check: verify ONNXEmbedder satisfies Embedder.
var _ Embedder = (*ONNXEmbedder)(nil)
