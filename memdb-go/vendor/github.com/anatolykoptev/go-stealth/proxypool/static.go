package proxypool

import (
	"net/http"
	"net/url"
	"sync/atomic"
)

// Static implements ProxyPool with a fixed list of proxy URLs.
// Useful for wrapping known proxies (e.g. Tor SOCKS5) into a pool.
type Static struct {
	proxies []string
	counter atomic.Uint64
}

// NewStatic creates a ProxyPool from static proxy URLs.
func NewStatic(urls ...string) *Static {
	return &Static{proxies: urls}
}

// Next returns the next proxy URL in round-robin order.
// Returns empty string if the pool is empty.
func (s *Static) Next() string {
	if len(s.proxies) == 0 {
		return ""
	}
	idx := s.counter.Add(1) % uint64(len(s.proxies))
	return s.proxies[idx]
}

// Len returns the number of proxies in the pool.
func (s *Static) Len() int {
	return len(s.proxies)
}

// TransportProxy returns a function suitable for http.Transport.Proxy.
func (s *Static) TransportProxy() func(*http.Request) (*url.URL, error) {
	return func(_ *http.Request) (*url.URL, error) {
		next := s.Next()
		if next == "" {
			return nil, nil
		}
		return url.Parse(next)
	}
}
