package embed

import (
	"context"
	"fmt"
)

// CorpusInspector is implemented by a vector store that can report the
// dimension of vectors already stored in its corpus. Used by
// [CheckCorpusDim] to detect dimension drift after an embedder model swap.
//
// The interface is intentionally minimal — one method — so it is trivial to
// implement for any storage backend: pgvector (query vector_dims() or
// pgvector's vector_dims(column) function), SQLite (inspect a row), an
// in-memory HNSW index (return Index.Dim()), a file-based store (read the
// first record), etc.
//
// StoredDim must return 0 when the corpus is empty — an empty corpus has no
// established dimension and accepts any embedder without drift. A non-zero
// return establishes the corpus dimension; CheckCorpusDim compares it against
// the active embedder's Dimension().
type CorpusInspector interface {
	// StoredDim returns the dimension of vectors already in the corpus,
	// or 0 if the corpus is empty (no drift possible on an empty corpus).
	// Implementations should return an error only when the dimension cannot
	// be determined (storage failure, corruption) — not when the corpus is
	// empty.
	StoredDim(ctx context.Context) (int, error)
}

// ErrCorpusDimMismatch is returned by [CheckCorpusDim] when the active
// embedder's dimension does not match the dimension of vectors already stored
// in the corpus. This is the corpus-level analogue of [ErrDimMismatch]
// (which is per-request: backend response vs WithDim).
//
// The failure mode this guards against: an operator swaps the embedding model
// (e.g. 768-dim → 1024-dim) without re-embedding the existing corpus. New
// vectors are written in the new dimension; old vectors remain in the old
// dimension. Depending on the storage backend, this either:
//   - causes a hard error at write time (pgvector vector(N) column), or
//   - silently splits the corpus into two mutually invisible embedding
//     spaces (in-memory HNSW, file-based stores without strict typing) —
//     cosine similarity between different-dim vectors is 0, so old and new
//     vectors never surface in each other's search results.
//
// CheckCorpusDim catches this at startup, before any write or search —
// giving the operator an early, actionable signal to re-embed the corpus
// rather than discovering the split through degraded retrieval quality.
type ErrCorpusDimMismatch struct {
	EmbedderDim int // dimension reported by the active embedder
	CorpusDim   int // dimension of vectors already in the corpus
}

func (e *ErrCorpusDimMismatch) Error() string {
	return fmt.Sprintf("embed: corpus dimension drift — embedder produces %d-dim vectors but corpus holds %d-dim vectors; re-embed the corpus with the active model before writing new vectors",
		e.EmbedderDim, e.CorpusDim)
}

// CheckCorpusDim compares the embedder's dimension against the corpus's
// stored dimension. Returns nil when:
//   - the embedder's dimension is 0 (auto-detect mode, no invariant to check), or
//   - the corpus is empty (StoredDim returns 0 — no drift possible), or
//   - the dimensions match.
//
// Returns [*ErrCorpusDimMismatch] when the dimensions differ — the caller
// should log this at Error level and surface a re-embedding instruction to
// the operator. The error is non-fatal: the caller decides whether to refuse
// writes (strict mode) or log-and-continue (lenient mode).
//
// Example usage at startup:
//
//	if err := embed.CheckCorpusDim(ctx, embedder, store); err != nil {
//	    var dimErr *embed.ErrCorpusDimMismatch
//	    if errors.As(err, &dimErr) {
//	        slog.Error("corpus dimension drift detected — new vectors will not match existing corpus",
//	            "embedder_dim", dimErr.EmbedderDim,
//	            "corpus_dim", dimErr.CorpusDim)
//	        fmt.Fprintf(os.Stderr, "  ⚠  Re-embed the corpus with the active model before writing new vectors.\n")
//	    }
//	}
//
// See issue #259 for the prefix-convention context that motivated this check;
// the same model-swap scenario that changes prefix conventions also changes
// dimensions, and both cause silent retrieval degradation.
func CheckCorpusDim(ctx context.Context, e Embedder, c CorpusInspector) error {
	if e == nil || c == nil {
		return nil
	}
	embedderDim := e.Dimension()
	if embedderDim <= 0 {
		// Auto-detect mode or nil-dim embedder — no invariant to check.
		return nil
	}
	corpusDim, err := c.StoredDim(ctx)
	if err != nil {
		return fmt.Errorf("embed: check corpus dim: %w", err)
	}
	if corpusDim <= 0 {
		// Empty corpus — first write establishes the dimension.
		return nil
	}
	if corpusDim != embedderDim {
		return &ErrCorpusDimMismatch{
			EmbedderDim: embedderDim,
			CorpusDim:   corpusDim,
		}
	}
	return nil
}
