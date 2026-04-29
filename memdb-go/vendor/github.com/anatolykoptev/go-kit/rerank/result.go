package rerank

// Status describes the outcome of a RerankWithResult call.
type Status uint8

const (
	// StatusOk means the request succeeded and scores are valid.
	StatusOk Status = iota
	// StatusDegraded means the request failed; Scored contains input order with Score=0.
	StatusDegraded
	// StatusFallback means the primary client failed and a secondary client succeeded.
	// Populated by G1 fallback path; G0 never produces this status.
	StatusFallback
	// StatusSkipped means the client was unavailable (URL empty) or docs was empty.
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

// Result is the typed return value of RerankWithResult.
// Callers should inspect Status before using Scored.
type Result struct {
	// Scored holds the ranked documents. On StatusDegraded/StatusSkipped, docs
	// are returned in their original order with Score=0.
	Scored []Scored
	// Status indicates whether the rerank call succeeded, was skipped, or degraded.
	Status Status
	// Model reports which model produced the scores (informational; may be empty).
	Model string
	// Err is non-nil iff Status == StatusDegraded.
	Err error
}
