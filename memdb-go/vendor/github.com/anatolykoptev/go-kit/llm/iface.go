// Package llm — Completer is the minimum contract for callers that only
// need single-shot chat completion. *Client satisfies it; consumers that
// want to swap in a no-op or stub can depend on this interface instead of
// the concrete type.
package llm

import "context"

type Completer interface {
	Complete(ctx context.Context, system, user string, opts ...ChatOption) (string, error)
}
