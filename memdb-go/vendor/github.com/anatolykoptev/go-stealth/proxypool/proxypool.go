package proxypool

import (
	"net/http"
	"net/url"
)

// ProxyPool is an abstract proxy provider.
type ProxyPool interface {
	// Next returns the next proxy URL in rotation.
	Next() string
	// Len returns the number of proxies in the pool.
	Len() int
	// TransportProxy returns a function suitable for http.Transport.Proxy.
	TransportProxy() func(*http.Request) (*url.URL, error)
}
