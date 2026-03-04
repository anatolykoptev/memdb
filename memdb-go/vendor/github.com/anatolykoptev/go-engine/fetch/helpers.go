package fetch

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"

	stealth "github.com/anatolykoptev/go-stealth"
)

// ReadResponseBody reads the response body, handling gzip decompression.
func ReadResponseBody(resp *http.Response) ([]byte, error) {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(gz)
	}
	return io.ReadAll(resp.Body)
}

// ChromeHeaders returns common Chrome browser headers.
func ChromeHeaders() map[string]string {
	return stealth.ChromeHeaders()
}

// RandomUserAgent returns a random Chrome-like User-Agent string.
func RandomUserAgent() string {
	return stealth.RandomUserAgent()
}

// RetryDo retries fn up to MaxRetries times with exponential backoff.
func RetryDo[T any](ctx context.Context, rc RetryConfig, fn func() (T, error)) (T, error) {
	return stealth.RetryDo(ctx, rc, fn)
}

// RetryHTTP executes an HTTP request function with retry logic.
func RetryHTTP(ctx context.Context, rc RetryConfig, fn func() (*http.Response, error)) (*http.Response, error) {
	return stealth.RetryHTTP(ctx, rc, fn)
}
