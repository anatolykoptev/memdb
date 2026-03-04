package stealth

import "context"

// RetryHookFunc is called before each retry attempt.
// attempt is 1-based; err is the error from the previous attempt.
type RetryHookFunc func(ctx context.Context, attempt int, maxAttempts int, err error)

type retryHookKey struct{}

// WithRetryHook returns a context carrying the given retry hook.
func WithRetryHook(ctx context.Context, fn RetryHookFunc) context.Context {
	return context.WithValue(ctx, retryHookKey{}, fn)
}

// retryHookFromContext extracts the retry hook, or nil.
func retryHookFromContext(ctx context.Context) RetryHookFunc {
	fn, _ := ctx.Value(retryHookKey{}).(RetryHookFunc)
	return fn
}
