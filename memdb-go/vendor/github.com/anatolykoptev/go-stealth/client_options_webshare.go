package stealth

import "github.com/anatolykoptev/go-stealth/proxypool"

// WithWebshareCountry creates a Webshare backbone proxy pool targeting the given
// ISO-2 country codes (e.g. "US", "GB"). Defaults to ["US"] if no countries are given.
// The API call is made eagerly; if it fails, NewClient returns the error.
//
// The client owns the pool this option builds: call BrowserClient.Close to
// stop the pool's periodic credential refresher when the client is done.
func WithWebshareCountry(apiKey string, countries ...string) ClientOption {
	return withWebshareCountryURL(apiKey, "", countries...)
}

// withWebshareCountryURL is the testable variant that accepts an override base URL.
// Pass "" for baseURL to use the real Webshare endpoint.
func withWebshareCountryURL(apiKey, baseURL string, countries ...string) ClientOption {
	return func(c *clientConfig) {
		cc := countries
		if len(cc) == 0 {
			cc = []string{"US"}
		}
		pool, err := proxypool.NewWebshareWithConfig(apiKey, proxypool.WebshareConfig{
			Countries: cc,
			BaseURL:   baseURL,
		})
		if err != nil {
			c.buildErrors = append(c.buildErrors, err)
			return
		}
		c.proxyPool = pool
		c.poolCloser = pool
	}
}

// WithWebshareRotating creates a Webshare rotating-endpoint proxy pool without
// calling the Webshare API. Uses username-CC-rotate syntax.
// Defaults to ["US"] when no countries are given.
//
// The client owns the pool this option builds (BrowserClient.Close releases
// it); a rotating pool runs no refresher, so closing it is a no-op today.
func WithWebshareRotating(username, password string, countries ...string) ClientOption {
	return func(c *clientConfig) {
		pool, err := proxypool.NewWebshareRotating(username, password, countries...)
		if err != nil {
			c.buildErrors = append(c.buildErrors, err)
			return
		}
		c.proxyPool = pool
		c.poolCloser = pool
	}
}
