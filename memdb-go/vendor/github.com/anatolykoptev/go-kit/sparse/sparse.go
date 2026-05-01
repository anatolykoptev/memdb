package sparse

import "context"

// SparseVector is the (Indices, Values) representation of a single SPLADE
// output. The two slices are aligned by position: Indices[i] is the
// vocabulary token id whose weight is Values[i]. Length is variable per
// input — empty vectors are legal (e.g. a query of pure stopwords).
//
// Both slices are populated and owned by the SparseEmbedder; callers must
// not mutate them. Defensive copies are the caller's responsibility when
// downstream code needs ordering invariants (e.g. pgvector's sparsevec
// literal requires sorted ascending indices — see memdb-go's
// FormatSparseVector helper).
type SparseVector struct {
	Indices []uint32
	Values  []float32
}

// IsEmpty reports whether the vector has no terms. Both slices empty
// (or nil) returns true. A vector where one slice is nil and the other is
// non-empty is malformed and reported as not-empty so the caller surfaces
// the error rather than silently dropping it.
func (v SparseVector) IsEmpty() bool {
	return len(v.Indices) == 0 && len(v.Values) == 0
}

// Len returns the number of (index, value) pairs in the vector. When the
// two slices have different lengths the smaller is returned — callers that
// care about the malformed case should compare len(v.Indices) and
// len(v.Values) directly.
func (v SparseVector) Len() int {
	n := len(v.Indices)
	if m := len(v.Values); m < n {
		n = m
	}
	return n
}

// SparseEmbedder generates sparse term-weight vectors for text inputs.
type SparseEmbedder interface {
	// EmbedSparse returns one SparseVector per input text, in input order.
	// Document/storage use case. Empty input returns (nil, nil) without
	// hitting the backend.
	EmbedSparse(ctx context.Context, texts []string) ([]SparseVector, error)
	// EmbedSparseQuery embeds a single query string. Search/retrieval use
	// case. Implementations may apply query-specific prefixes or
	// instructions; default delegates to EmbedSparse.
	EmbedSparseQuery(ctx context.Context, text string) (SparseVector, error)
	// VocabSize returns the model's vocabulary size (the dimension of the
	// sparse space — e.g. 30522 for BERT-base SPLADE). Used by callers that
	// need to format pgvector sparsevec literals or allocate dim-sized
	// buffers. The value is configured at construction; the backend does
	// not validate it against the model's actual head.
	VocabSize() int
	// Close releases resources (HTTP clients, model handles).
	Close() error
}

// EmbedSparseQueryViaEmbed is a helper that implements EmbedSparseQuery by
// delegating to EmbedSparse. Use it in SparseEmbedder implementations that
// don't need query-specific behaviour.
func EmbedSparseQueryViaEmbed(ctx context.Context, e SparseEmbedder, text string) (SparseVector, error) {
	vecs, err := e.EmbedSparse(ctx, []string{text})
	if err != nil {
		return SparseVector{}, err
	}
	if len(vecs) == 0 {
		return SparseVector{}, nil
	}
	return vecs[0], nil
}
