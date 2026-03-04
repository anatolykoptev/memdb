package stealth

import (
	"context"
	"net/http"
)

// RetryDoWithReset is like RetryDo but calls resetFn before each retry
// attempt (not before the first attempt). If resetFn is nil, it behaves
// identically to RetryDo.
func RetryDoWithReset[T any](
	ctx context.Context,
	rc RetryConfig,
	resetFn func(),
	fn func() (T, error),
) (T, error) {
	return retryDo(ctx, rc, resetFn, fn)
}

// RetryHTTPWithReset is like RetryHTTP but calls resetFn before each
// retry attempt. If resetFn is nil, it behaves identically to RetryHTTP.
func RetryHTTPWithReset(
	ctx context.Context,
	rc RetryConfig,
	resetFn func(),
	fn func() (*http.Response, error),
) (*http.Response, error) {
	return RetryDoWithReset(ctx, rc, resetFn, func() (*http.Response, error) {
		resp, err := fn()
		if err != nil {
			return nil, err
		}
		if IsRetryableStatus(resp.StatusCode) {
			resp.Body.Close()
			return nil, &HttpStatusError{StatusCode: resp.StatusCode}
		}
		return resp, nil
	})
}
