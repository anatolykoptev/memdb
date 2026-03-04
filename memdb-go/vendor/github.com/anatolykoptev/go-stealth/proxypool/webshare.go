package proxypool

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// Webshare implements ProxyPool using the Webshare API.
type Webshare struct {
	proxies []string
	counter atomic.Uint64
}

type webshareResponse struct {
	Results []webshareProxy `json:"results"`
}

type webshareProxy struct {
	ProxyAddress string `json:"proxy_address"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

const (
	webshareDefaultURL  = "https://proxy.webshare.io/api/v2/proxy/list/?mode=backbone&page_size=100"
	webshareDefaultHost = "p.webshare.io" // shared gateway for backbone proxies
)

// NewWebshare fetches proxies from the Webshare API and returns a ready-to-use pool.
func NewWebshare(apiKey string) (*Webshare, error) {
	return newWebshareFromURL(webshareDefaultURL, apiKey)
}

func newWebshareFromURL(apiURL, apiKey string) (*Webshare, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("proxy: empty API key")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("proxy: build request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy: fetch list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("proxy: webshare API returned %d: %s", resp.StatusCode, string(body))
	}

	var data webshareResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("proxy: decode response: %w", err)
	}

	if len(data.Results) == 0 {
		return nil, fmt.Errorf("proxy: webshare returned 0 proxies")
	}

	proxies := make([]string, 0, len(data.Results))
	for _, p := range data.Results {
		host := p.ProxyAddress
		if host == "" {
			host = webshareDefaultHost
		}
		u := fmt.Sprintf("http://%s:%s@%s:%d", p.Username, p.Password, host, p.Port)
		proxies = append(proxies, u)
	}

	slog.Info("proxy pool initialized", slog.Int("count", len(proxies)))

	return &Webshare{proxies: proxies}, nil
}

// Next returns the next proxy URL in round-robin order.
func (w *Webshare) Next() string {
	idx := w.counter.Add(1) % uint64(len(w.proxies))
	return w.proxies[idx]
}

// Len returns the number of proxies in the pool.
func (w *Webshare) Len() int {
	return len(w.proxies)
}

// TransportProxy returns a function suitable for http.Transport.Proxy.
func (w *Webshare) TransportProxy() func(*http.Request) (*url.URL, error) {
	return func(_ *http.Request) (*url.URL, error) {
		return url.Parse(w.Next())
	}
}
