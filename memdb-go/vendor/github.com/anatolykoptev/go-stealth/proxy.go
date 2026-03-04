package stealth

import "net/url"

// MaskProxy masks credentials in a proxy URL for safe logging.
// "socks5://user:pass@host:1080" -> "socks5://***@host:1080"
func MaskProxy(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return proxyURL
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

// ValidateProxy tests proxy connectivity by fetching the given URL.
// Returns nil if the proxy is reachable, error otherwise.
func ValidateProxy(client *BrowserClient, testURL string) error {
	headers := map[string]string{
		"user-agent": RandomUserAgent(),
		"accept":     "*/*",
	}
	_, _, status, err := client.Do("GET", testURL, headers, nil)
	if err != nil {
		return err
	}
	if status >= 500 {
		return &HttpStatusError{StatusCode: status}
	}
	return nil
}
