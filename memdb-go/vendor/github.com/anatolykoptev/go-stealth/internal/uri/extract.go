// Package uri provides shared URL utility functions.
package uri

import (
	"net/url"
	"strings"
)

// ExtractHost returns the lowercased hostname from a URL string.
// If the URL cannot be parsed or has no host, the raw input is returned.
func ExtractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if host == "" {
		return rawURL
	}
	return strings.ToLower(host)
}
