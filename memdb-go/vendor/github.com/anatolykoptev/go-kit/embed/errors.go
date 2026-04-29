package embed

import "fmt"

// ErrDimMismatch is returned by [Client.Embed] / [Client.EmbedQuery] /
// [Client.EmbedWithResult] when the backend returns a vector whose length
// does not match the dimension declared via [WithDim].
//
// This guards against silent corruption of downstream pgvector / Qdrant
// schemas when the backend model is swapped (e.g. via env var change)
// without a coordinated [WithDim] update on the consumer side. Without
// this check, a 1024-dim response would be written to a vector(768)
// column and only fail at INSERT time — far from the configuration error.
//
// Behaviour:
//   - Returned only when [WithDim] was set to a non-zero value
//     (cfg.dim == 0 disables validation, preserving auto-detection).
//   - Embed handlers MUST continue serving — do NOT panic; treat as a
//     normal error and propagate.
//   - Each mismatch increments embed_dim_mismatch_total{model} so
//     dashboards can alert on production drift.
type ErrDimMismatch struct {
	// Got is the length of the vector returned by the backend.
	Got int
	// Want is the dimension declared via WithDim.
	Want int
	// Model is the resolved model name (may be empty for opaque backends).
	Model string
}

// Error implements the error interface.
func (e *ErrDimMismatch) Error() string {
	return fmt.Sprintf("embed: dimension mismatch (model=%q got=%d want=%d)", e.Model, e.Got, e.Want)
}
