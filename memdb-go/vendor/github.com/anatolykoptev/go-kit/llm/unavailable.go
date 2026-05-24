package llm

import "errors"

// ErrUnavailable is returned by Completers that are intentionally inert —
// e.g. NoOp when no API key is configured, or a wrapped client whose
// fallback chain is exhausted. Callers MUST treat this as a non-fatal
// signal and degrade according to their own policy. All other errors are
// real failures and should propagate.
var ErrUnavailable = errors.New("llm: unavailable")
