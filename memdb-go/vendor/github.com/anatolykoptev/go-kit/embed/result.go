package embed

// Status describes the outcome of an Embed call.
type Status uint8

const (
	// StatusOk means the request succeeded and vectors are valid.
	StatusOk Status = iota
	// StatusDegraded means the request failed; vectors are zero-length placeholders.
	StatusDegraded
	// StatusFallback means the primary backend failed and a secondary succeeded.
	// Populated by E1 fallback path; E0 never produces this status.
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

// Vector is the per-text result from EmbedWithResult.
// TokenCount is 0 when the backend does not expose usage; populated by E4.
type Vector struct {
	Embedding  []float32
	Dim        int    // == len(Embedding) at construction time
	TokenCount int    // 0 when backend doesn't expose
	Status     Status // per-text — usually StatusOk; for partial-batch failures
}

// Result is the typed return value of EmbedWithResult.
// Callers should inspect Status before using Vectors.
type Result struct {
	// Vectors holds one entry per input text. On StatusDegraded/StatusSkipped,
	// entries are zero-length placeholders with their own Status set.
	Vectors []*Vector
	// Status indicates whether the embed call succeeded, was skipped, or degraded.
	Status Status
	// Model reports which model produced the embeddings (may be empty).
	Model string
	// TokensUsed is the total token count across all texts (0 when unavailable).
	// Populated by E4 when backend exposes usage.
	TokensUsed int
	// Err is non-nil iff Status == StatusDegraded.
	Err error
}
