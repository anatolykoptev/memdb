package stealth

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// RoundTrip implements the http.RoundTripper interface.
// This allows BrowserClient to be used as a Transport for any standard net/http consumer
// (resty, go-retryablehttp, etc.) while maintaining TLS fingerprint impersonation.
//
// Middleware added via Use() is applied. Proxy rotation is applied.
func (bc *BrowserClient) RoundTrip(req *http.Request) (*http.Response, error) {
	// Convert http.Request headers to map
	headers := make(map[string]string, len(req.Header))
	for k, v := range req.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	body, respHeaders, status, err := bc.Do(req.Method, req.URL.String(), headers, req.Body)
	if err != nil {
		return nil, err
	}

	// Convert back to http.Response
	resp := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}

	for k, v := range respHeaders {
		resp.Header.Set(k, v)
	}

	// Restore multi-value Set-Cookie if it was joined
	if cookies, ok := respHeaders["set-cookie"]; ok && strings.Contains(cookies, "; ") {
		resp.Header.Del("Set-Cookie")
		for _, c := range strings.Split(cookies, "; ") {
			resp.Header.Add("Set-Cookie", c)
		}
	}

	return resp, nil
}

// StdClient returns a standard *http.Client that uses this BrowserClient as transport.
// The returned client preserves TLS fingerprinting, proxy rotation, and middleware.
func (bc *BrowserClient) StdClient() *http.Client {
	return &http.Client{Transport: bc}
}
