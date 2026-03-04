package stealth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// stdDoer wraps net/http as an HTTPDoer. No TLS fingerprinting —
// useful for testing and CGO-free environments.
type stdDoer struct {
	client *http.Client
	jar    http.CookieJar
}

// newStdBackend creates an HTTPDoer backed by standard net/http.
func newStdBackend(cfg BackendConfig) (HTTPDoer, error) {
	jar, _ := cookiejar.New(nil)

	transport := &http.Transport{}
	if cfg.ProxyURL != "" {
		proxyURL, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	client := &http.Client{
		Jar:       jar,
		Transport: transport,
		Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
	}
	if !cfg.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	return &stdDoer{client: client, jar: jar}, nil
}

func (s *stdDoer) Do(req *Request) (*Response, error) {
	httpReq, err := http.NewRequest(req.Method, req.URL, req.Body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	// HeaderOrder is ignored — net/http doesn't support header ordering.

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	rawData, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Response{StatusCode: resp.StatusCode}, fmt.Errorf("read body: %w", err)
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		lk := strings.ToLower(k)
		if lk == "set-cookie" {
			respHeaders["set-cookie"] = strings.Join(v, "; ")
		} else if len(v) > 0 {
			respHeaders[lk] = v[0]
		}
	}

	data, err := decompressBody(rawData, respHeaders["content-encoding"])
	if err != nil {
		return &Response{StatusCode: resp.StatusCode}, fmt.Errorf("decompress body: %w", err)
	}

	return &Response{Body: data, Headers: respHeaders, StatusCode: resp.StatusCode}, nil
}

func (s *stdDoer) SetProxy(proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}
	s.client.Transport.(*http.Transport).Proxy = http.ProxyURL(u)
	return nil
}

func (s *stdDoer) GetCookieValue(rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, c := range s.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
