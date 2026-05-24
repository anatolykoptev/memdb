package llm

import "context"

// NoOp is a Completer that always returns ErrUnavailable. It exists so
// callers can wire a non-nil Completer when no API key is configured,
// avoiding nil-checks at every call site.
type NoOp struct{}

func (NoOp) Complete(context.Context, string, string, ...ChatOption) (string, error) {
	return "", ErrUnavailable
}

// NewOptional returns a real *Client when apiKey is non-empty, otherwise
// NoOp{}. The bool is true when a real client was constructed, false when
// NoOp was returned — useful for startup logging and metric labels.
//
// The gating condition is apiKey only: an empty baseURL or empty model is
// passed through to NewClient as-is (NewClient's own pre-existing behaviour
// handles those). NewOptional never returns nil.
func NewOptional(baseURL, apiKey, model string, opts ...Option) (Completer, bool) {
	if apiKey == "" {
		return NoOp{}, false
	}
	return NewClient(baseURL, apiKey, model, opts...), true
}
