package stealth

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// tlsClientDoer wraps bogdanfinn/tls-client as an HTTPDoer.
type tlsClientDoer struct {
	client tls_client.HttpClient
}

// newTLSClientBackend creates an HTTPDoer backed by bogdanfinn/tls-client.
func newTLSClientBackend(cfg BackendConfig) (HTTPDoer, error) {
	profile := mapTLSProfile(cfg.Profile)

	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(cfg.TimeoutSeconds),
		tls_client.WithClientProfile(profile),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithInsecureSkipVerify(),
	}
	if !cfg.FollowRedirects {
		opts = append(opts, tls_client.WithNotFollowRedirects())
	}
	if cfg.ProxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(cfg.ProxyURL))
	}

	client, err := tls_client.NewHttpClient(nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("tls-client init: %w", err)
	}
	return &tlsClientDoer{client: client}, nil
}

func (t *tlsClientDoer) Do(req *Request) (*Response, error) {
	httpReq, err := fhttp.NewRequest(req.Method, req.URL, req.Body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if len(req.HeaderOrder) > 0 {
		httpReq.Header[fhttp.HeaderOrderKey] = req.HeaderOrder
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tls request: %w", err)
	}
	defer resp.Body.Close()

	rawData, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Response{StatusCode: resp.StatusCode}, fmt.Errorf("read body: %w", err)
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		if strings.ToLower(k) == "set-cookie" {
			respHeaders["set-cookie"] = strings.Join(v, "; ")
		} else if len(v) > 0 {
			respHeaders[strings.ToLower(k)] = v[0]
		}
	}

	data, err := decompressBody(rawData, respHeaders["content-encoding"])
	if err != nil {
		return &Response{StatusCode: resp.StatusCode}, fmt.Errorf("decompress body: %w", err)
	}

	return &Response{Body: data, Headers: respHeaders, StatusCode: resp.StatusCode}, nil
}

func (t *tlsClientDoer) SetProxy(proxyURL string) error {
	return t.client.SetProxy(proxyURL)
}

func (t *tlsClientDoer) GetCookieValue(rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, c := range t.client.GetCookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// profileMap maps go-stealth TLSProfile values to bogdanfinn profiles.
var profileMap = map[TLSProfile]profiles.ClientProfile{
	ProfileChrome131:    profiles.Chrome_131,
	ProfileChrome133:    profiles.Chrome_133,
	ProfileFirefox133:   profiles.Firefox_133,
	ProfileSafari16:     profiles.Safari_16_0,
	ProfileSafariIOS18:  profiles.Safari_IOS_18_0,
	ProfileSafariIOS17:  profiles.Safari_IOS_17_0,
}

// mapTLSProfile converts a TLSProfile to a bogdanfinn ClientProfile.
// Falls back to Chrome_131 for unmapped values.
func mapTLSProfile(p TLSProfile) profiles.ClientProfile {
	if mapped, ok := profileMap[p]; ok {
		return mapped
	}
	return profiles.Chrome_131
}
