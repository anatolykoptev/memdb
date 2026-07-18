package stealth

import (
	"context"
	"io"
)

type doResult struct {
	body    []byte
	headers map[string]string
	status  int
	err     error
}

// DoCtx executes an HTTP request with context cancellation/deadline support.
// If ctx is already cancelled, returns ctx.Err() immediately.
// Otherwise runs Do in a goroutine and returns when the request completes or ctx is done.
func (bc *BrowserClient) DoCtx(ctx context.Context, method, urlStr string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}

	ch := make(chan doResult, 1)
	go func() {
		b, h, s, err := bc.Do(method, urlStr, headers, body)
		ch <- doResult{b, h, s, err}
	}()

	select {
	case <-ctx.Done():
		return nil, nil, 0, ctx.Err()
	case r := <-ch:
		return r.body, r.headers, r.status, r.err
	}
}

// DoWithHeaderOrderCtx executes a request with a custom header order and context support.
// If ctx is already cancelled, returns ctx.Err() immediately.
// Otherwise runs DoWithHeaderOrder in a goroutine and returns when the request completes or ctx is done.
func (bc *BrowserClient) DoWithHeaderOrderCtx(ctx context.Context, method, urlStr string, headers map[string]string, body io.Reader, order []string) ([]byte, map[string]string, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}

	ch := make(chan doResult, 1)
	go func() {
		b, h, s, err := bc.DoWithHeaderOrder(method, urlStr, headers, body, order)
		ch <- doResult{b, h, s, err}
	}()

	select {
	case <-ctx.Done():
		return nil, nil, 0, ctx.Err()
	case r := <-ch:
		return r.body, r.headers, r.status, r.err
	}
}
