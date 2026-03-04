package stealth

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
)

// BrowserProfile pairs a User-Agent string with a matching TLS fingerprint
// and metadata for filtering.
type BrowserProfile struct {
	UserAgent  string
	TLSProfile TLSProfile
	Browser    string // "chrome", "firefox", "safari", "edge"
	OS         string // "windows", "macos", "linux", "android", "ios"
	Mobile     bool
}

// BuiltinProfiles provides browser fingerprint diversity across Chrome, Safari,
// Firefox, and Edge with per-OS variants.
var BuiltinProfiles = []BrowserProfile{
	// Chrome — Windows
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome131, Browser: "chrome", OS: "windows",
	},
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome133, Browser: "chrome", OS: "windows",
	},
	// Chrome — macOS
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome131, Browser: "chrome", OS: "macos",
	},
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome133, Browser: "chrome", OS: "macos",
	},
	// Chrome — Linux
	{
		UserAgent:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome131, Browser: "chrome", OS: "linux",
	},
	{
		UserAgent:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome133, Browser: "chrome", OS: "linux",
	},
	// Chrome — Android
	{
		UserAgent:  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
		TLSProfile: ProfileChrome131, Browser: "chrome", OS: "android", Mobile: true,
	},

	// Safari — macOS
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15",
		TLSProfile: ProfileSafari16, Browser: "safari", OS: "macos",
	},
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
		TLSProfile: ProfileSafari16, Browser: "safari", OS: "macos",
	},
	// Safari — iOS
	{
		UserAgent:  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
		TLSProfile: ProfileSafariIOS18, Browser: "safari", OS: "ios", Mobile: true,
	},
	{
		UserAgent:  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
		TLSProfile: ProfileSafariIOS17, Browser: "safari", OS: "ios", Mobile: true,
	},

	// Firefox — Windows
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
		TLSProfile: ProfileFirefox133, Browser: "firefox", OS: "windows",
	},
	// Firefox — macOS
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:133.0) Gecko/20100101 Firefox/133.0",
		TLSProfile: ProfileFirefox133, Browser: "firefox", OS: "macos",
	},
	// Firefox — Linux
	{
		UserAgent:  "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
		TLSProfile: ProfileFirefox133, Browser: "firefox", OS: "linux",
	},

	// Edge — Windows (uses Chrome TLS fingerprint — same Chromium engine)
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
		TLSProfile: ProfileChrome131, Browser: "edge", OS: "windows",
	},
	// Edge — macOS
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
		TLSProfile: ProfileChrome131, Browser: "edge", OS: "macos",
	},
}

// ProfileOption configures profile filtering for RandomProfile.
type ProfileOption func(*profileFilter)

type profileFilter struct {
	os      string
	browser string
	mobile  *bool
}

// WithOS filters profiles by operating system.
// Valid values: "windows", "macos", "linux", "android", "ios".
func WithOS(os string) ProfileOption {
	return func(f *profileFilter) {
		f.os = strings.ToLower(os)
	}
}

// WithBrowser filters profiles by browser.
// Valid values: "chrome", "firefox", "safari", "edge".
func WithBrowser(b string) ProfileOption {
	return func(f *profileFilter) {
		f.browser = strings.ToLower(b)
	}
}

// WithMobile filters for mobile or desktop profiles.
func WithMobile(mobile bool) ProfileOption {
	return func(f *profileFilter) {
		f.mobile = &mobile
	}
}

// RandomProfile returns a random BrowserProfile matching the given filters.
// With no options, returns any profile. Returns a fallback if no profiles match.
func RandomProfile(opts ...ProfileOption) BrowserProfile {
	var f profileFilter
	for _, o := range opts {
		o(&f)
	}

	var candidates []BrowserProfile
	for _, p := range BuiltinProfiles {
		if f.os != "" && p.OS != f.os {
			continue
		}
		if f.browser != "" && p.Browser != f.browser {
			continue
		}
		if f.mobile != nil && p.Mobile != *f.mobile {
			continue
		}
		candidates = append(candidates, p)
	}

	if len(candidates) == 0 {
		return BuiltinProfiles[rand.IntN(len(BuiltinProfiles))]
	}
	return candidates[rand.IntN(len(candidates))]
}

// PlatformMatchedProfile returns a BrowserProfile whose OS matches
// the actual runtime platform (runtime.GOOS).
func PlatformMatchedProfile() BrowserProfile {
	var goosToOS string
	switch runtime.GOOS {
	case "windows":
		goosToOS = "windows"
	case "darwin":
		goosToOS = "macos"
	case "linux":
		goosToOS = "linux"
	default:
		return BuiltinProfiles[rand.IntN(len(BuiltinProfiles))]
	}
	return RandomProfile(WithOS(goosToOS), WithMobile(false))
}

// ClientHintsHeaders returns sec-ch-ua-* headers for Chromium-based UAs.
// Returns nil for Safari/Firefox (they don't send Client Hints).
func ClientHintsHeaders(ua string) map[string]string {
	if !strings.Contains(ua, "Chrome/") {
		return nil
	}
	version := ExtractChromeVersion(ua)
	platform := extractPlatform(ua)
	mobile := "?0"
	if strings.Contains(ua, "Mobile") {
		mobile = "?1"
	}

	hints := map[string]string{
		"sec-ch-ua":          fmt.Sprintf(`"Chromium";v="%s", "Not_A Brand";v="24"`, version),
		"sec-ch-ua-mobile":   mobile,
		"sec-ch-ua-platform": fmt.Sprintf(`"%s"`, platform),
	}

	// Edge adds its own brand
	if strings.Contains(ua, "Edg/") {
		edgeVersion := extractEdgeVersion(ua)
		hints["sec-ch-ua"] = fmt.Sprintf(`"Chromium";v="%s", "Microsoft Edge";v="%s", "Not_A Brand";v="24"`, version, edgeVersion)
	}

	return hints
}

// ExtractChromeVersion extracts the major Chrome version from a User-Agent string.
func ExtractChromeVersion(ua string) string {
	idx := strings.Index(ua, "Chrome/")
	if idx == -1 {
		return "131"
	}
	rest := ua[idx+7:]
	dot := strings.IndexByte(rest, '.')
	if dot == -1 {
		return rest
	}
	return rest[:dot]
}

func extractEdgeVersion(ua string) string {
	idx := strings.Index(ua, "Edg/")
	if idx == -1 {
		return "131"
	}
	rest := ua[idx+4:]
	dot := strings.IndexByte(rest, '.')
	if dot == -1 {
		return rest
	}
	return rest[:dot]
}

func extractPlatform(ua string) string {
	switch {
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		return "macOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		return "iOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return "Windows"
	}
}
