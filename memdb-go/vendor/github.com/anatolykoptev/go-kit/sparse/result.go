package sparse

// Status describes the outcome of an EmbedSparse call. Mirrors embed.Status
// to keep telemetry consumers symmetric across the three encoder families.
type Status uint8

const (
	// StatusOk means the request succeeded and vectors are valid.
	StatusOk Status = iota
	// StatusDegraded means the request failed; vectors are empty placeholders.
	StatusDegraded
	// StatusFallback means the primary backend failed and a secondary succeeded.
	StatusFallback
	// StatusSkipped means the embedder was nil, texts was empty, or DryRun was set.
	StatusSkipped
)

// String returns the human-readable status label.
func (s Status) String() string {
	switch s {
	case StatusOk:
		return "ok"
	case StatusDegraded:
		return "degraded"
	case StatusFallback:
		return "fallback"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Vector is the per-text result from EmbedSparseWithResult. Status is
// per-text — usually StatusOk; for partial-batch failures (not produced by
// the v1 HTTP backend, which fails the whole batch atomically) entries can
// carry their own Status.
type Vector struct {
	Sparse SparseVector
	Status Status
}

// Result is the typed return value of EmbedSparseWithResult. Callers should
// inspect Status before using Vectors.
type Result struct {
	// Vectors holds one entry per input text. On StatusDegraded/StatusSkipped,
	// entries are empty placeholders with their own Status set.
	Vectors []*Vector
	// Status indicates whether the call succeeded, was skipped, or degraded.
	Status Status
	// Model reports which SPLADE model produced the vectors (may be empty).
	Model string
	// Err is non-nil iff Status == StatusDegraded.
	Err error
}
