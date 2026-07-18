package stealth

import (
	"fmt"
	"io"
	"net"
	"net/http"
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
	}
	if cfg.InsecureSkipVerify {
		opts = append(opts, tls_client.WithInsecureSkipVerify())
	}
	if cfg.DialControl != nil {
		// Connect-time SSRF guard on the resolved address. tls-client dials
		// the DIRECT target through this net.Dialer (connect.go directDialer),
		// so Control fires per redirect hop on the direct path — rebind-proof.
		// On a PROXIED client the dialer targets the proxy, so this sees only
		// the proxy address; RedirectGuard + the pre-request guard cover that.
		opts = append(opts, tls_client.WithDialer(net.Dialer{Control: adaptControl(cfg.DialControl)}))
	}
	switch {
	case !cfg.FollowRedirects:
		opts = append(opts, tls_client.WithNotFollowRedirects())
	case cfg.RedirectGuard != nil:
		// Per-hop SSRF guard. tls-client's hook is fhttp-typed; the adapter
		// keeps bogdanfinn/fhttp out of go-stealth's public option signatures.
		opts = append(opts, tls_client.WithCustomRedirectFunc(adaptRedirect(cfg.RedirectGuard)))
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

// adaptRedirect bridges the stdlib-typed RedirectGuard into the fhttp-typed
// hook tls-client's WithCustomRedirectFunc expects, so bogdanfinn/fhttp never
// leaks into go-stealth's public API (boundaries H1).
//
// fhttp's redirect loop (fhttp/client.go) builds its own via chain from the
// actual prior *fhttp.Request values (each with URL/Method/Header set,
// appended via `reqs = append(reqs, req)` before the NEXT hop's
// checkRedirect call) — the same shape and ordering as stdlib
// net/http.Client's via. adaptHTTPRequest converts each hop faithfully
// (fhttp.Request.URL is already a stdlib *net/url.URL; fhttp.Header and
// http.Header share the identical underlying map[string][]string, so the
// conversion is a zero-copy type conversion) rather than handing the guard a
// length-only slice of empty requests — WithRedirectGuard is a PUBLIC option,
// and a caller-supplied guard that reads via[i].URL or via[i].Header (not
// just len(via)) must not nil/zero-panic.
func adaptRedirect(guard func(req *http.Request, via []*http.Request) error) func(req *fhttp.Request, via []*fhttp.Request) error {
	return func(fr *fhttp.Request, fvia []*fhttp.Request) error {
		via := make([]*http.Request, len(fvia))
		for i, v := range fvia {
			via[i] = adaptHTTPRequest(v)
		}
		return guard(adaptHTTPRequest(fr), via)
	}
}

// adaptHTTPRequest converts a fhttp.Request's guard-relevant fields (Method,
// URL, Header) into a stdlib *http.Request. A nil input yields a non-nil,
// zero-value *http.Request so a guard can never dereference a nil hop entry.
func adaptHTTPRequest(fr *fhttp.Request) *http.Request {
	if fr == nil {
		return &http.Request{}
	}
	return &http.Request{
		Method: fr.Method,
		URL:    fr.URL,
		Header: http.Header(fr.Header),
	}
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

	rawData, readErr := io.ReadAll(resp.Body)
	if readErr != nil && len(rawData) == 0 {
		return &Response{StatusCode: resp.StatusCode}, fmt.Errorf("read body: %w", readErr)
	}
	// Partial read (e.g. brotli decode error mid-stream) — use what we got.

	respHeaders := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		if strings.ToLower(k) == "set-cookie" {
			respHeaders["set-cookie"] = strings.Join(v, "\n")
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
	ProfileChrome131:   profiles.Chrome_131,
	ProfileChrome133:   profiles.Chrome_133,
	ProfileFirefox133:  profiles.Firefox_133,
	ProfileSafari16:    profiles.Safari_16_0,
	ProfileSafariIOS18: profiles.Safari_IOS_18_0,
	ProfileSafariIOS17: profiles.Safari_IOS_17_0,
}

// mapTLSProfile converts a TLSProfile to a bogdanfinn ClientProfile.
// Falls back to Chrome_131 for unmapped values.
func mapTLSProfile(p TLSProfile) profiles.ClientProfile {
	if mapped, ok := profileMap[p]; ok {
		return mapped
	}
	return profiles.Chrome_131
}
