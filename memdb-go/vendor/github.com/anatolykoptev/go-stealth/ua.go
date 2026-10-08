package stealth

import "math/rand/v2"

// RandomUserAgents is a pool of Chrome-like User-Agents for rotation.
// Chrome entries are kept in line with the newest default TLS profile
// (ProfileChrome146). Firefox entries are kept in line with ProfileFirefox148.
// The Safari 17.2 entry is stale relative to BuiltinProfiles (17.6/18.2) but
// out of scope here — left as-is.
var RandomUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
	"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:148.0) Gecko/20100101 Firefox/148.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:148.0) Gecko/20100101 Firefox/148.0",
}

// RandomUserAgent returns a random Chrome-like User-Agent.
func RandomUserAgent() string {
	return RandomUserAgents[rand.IntN(len(RandomUserAgents))]
}

// chromeUserAgents is the Chrome-only subset of RandomUserAgents. ChromeHeaders
// advertises Chrome-specific accept/accept-encoding and is paired with a Chrome
// TLS profile, so its User-Agent MUST be a Chrome UA — sourcing it from the
// mixed RandomUserAgents pool (which also holds Safari 17.2 and Firefox 115
// entries) can return a Firefox UA over Chrome headers/JA3, the same
// UA<->JA3 mismatch class this package exists to eliminate. RandomUserAgents
// itself is left untouched: its other caller (proxy.go ValidateProxy) is a
// connectivity probe where browser identity is irrelevant, and the stale
// Safari/Firefox entries are a separate out-of-scope cleanup.
var chromeUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
}

// randomChromeUserAgent returns a random Chrome User-Agent from the Chrome-only
// pool, for use by ChromeHeaders.
func randomChromeUserAgent() string {
	return chromeUserAgents[rand.IntN(len(chromeUserAgents))]
}

// ChromeHeaders returns common Chrome browser headers with a random Chrome User-Agent.
//
// The set and the accept value match the real-Chrome references captured per
// major in internal/fingerprint/testdata/reference_chrome_*.json (header_order
// + accept fields). Real Chrome sends 13 request headers on a top-level GET
// navigation; the sec-ch-ua-* trio is added separately by ClientHintsHeaders
// (it is profile-version-dependent), so ChromeHeaders emits the remaining 10.
// The sec-fetch-*, upgrade-insecure-requests, and priority values are the
// canonical Chrome top-level-navigation values; the references record only
// these headers' names and order (in header_order), not their values, and the
// oracle compares only names+order for them.
func ChromeHeaders() map[string]string {
	return map[string]string{
		"accept-language":           "en-US,en;q=0.9",
		"upgrade-insecure-requests": "1",
		"user-agent":                randomChromeUserAgent(),
		"accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"sec-fetch-site":            "none",
		"sec-fetch-mode":            "navigate",
		"sec-fetch-user":            "?1",
		"sec-fetch-dest":            "document",
		"accept-encoding":           "gzip, deflate, br, zstd",
		"priority":                  "u=0, i",
	}
}
