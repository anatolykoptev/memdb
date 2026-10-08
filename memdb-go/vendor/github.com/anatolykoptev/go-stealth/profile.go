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

// BrowserIdentity is the single owner of everything a target observes about
// this client's browser half: the TLS fingerprint (JA3/JA4), the User-Agent,
// and the Client Hints (sec-ch-ua-*) derived from that User-Agent. It embeds
// BrowserProfile as its core so every existing caller of BuiltinProfiles,
// RandomProfile, PlatformMatchedProfile and session.WithProfile keeps
// compiling unchanged — promoted fields give id.UserAgent, id.TLSProfile,
// id.Browser, id.OS, id.Mobile directly, and id.BrowserProfile is reachable
// when the whole struct is needed.
//
// A BrowserClient built with only a TLSProfile (the common case) resolves its
// Identity() from BuiltinProfiles so the UA and Client Hints agree with the
// JA3 by contract, not coincidence. WithIdentity sets profile and UA together
// so they can never drift apart.
type BrowserIdentity struct {
	BrowserProfile
	// ClientHints holds the sec-ch-ua-* headers derived from UserAgent via
	// ClientHintsHeaders. nil for Safari/Firefox (they send no Client Hints).
	ClientHints map[string]string
}

// UserAgentForProfile returns a User-Agent string from BuiltinProfiles whose
// TLSProfile matches p. It returns the FIRST matching entry (BuiltinProfiles
// holds per-OS variants sharing one TLS profile; the first is Windows where
// available). Returns "" when no BuiltinProfiles entry matches p — this is
// the documented no-entry behaviour, NOT a fallback: callers pairing a UA
// with an unknown profile must handle the empty string rather than silently
// presenting a mismatched UA. This is the single lookup the consumer repos
// use to delete their hardcoded UA literals.
func UserAgentForProfile(p TLSProfile) string {
	bp, ok := profileForTLS(p)
	if !ok {
		return ""
	}
	return bp.UserAgent
}

// profileForTLS returns the first BuiltinProfiles entry whose TLSProfile
// matches p, with ok=false when none match. Used by UserAgentForProfile and
// by Identity() to resolve a full BrowserProfile (Browser/OS/Mobile included)
// from a bare TLSProfile.
func profileForTLS(p TLSProfile) (BrowserProfile, bool) {
	for _, bp := range BuiltinProfiles {
		if bp.TLSProfile == p {
			return bp, true
		}
	}
	return BrowserProfile{}, false
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
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome144, Browser: "chrome", OS: "windows",
	},
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome146, Browser: "chrome", OS: "windows",
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
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome144, Browser: "chrome", OS: "macos",
	},
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome146, Browser: "chrome", OS: "macos",
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
	{
		UserAgent:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome144, Browser: "chrome", OS: "linux",
	},
	{
		UserAgent:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		TLSProfile: ProfileChrome146, Browser: "chrome", OS: "linux",
	},
	// Chrome — Android
	{
		UserAgent:  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
		TLSProfile: ProfileChrome131, Browser: "chrome", OS: "android", Mobile: true,
	},
	{
		UserAgent:  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Mobile Safari/537.36",
		TLSProfile: ProfileChrome146, Browser: "chrome", OS: "android", Mobile: true,
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
	// Firefox 148 — Windows
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0",
		TLSProfile: ProfileFirefox148, Browser: "firefox", OS: "windows",
	},
	// Firefox 148 — macOS
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:148.0) Gecko/20100101 Firefox/148.0",
		TLSProfile: ProfileFirefox148, Browser: "firefox", OS: "macos",
	},
	// Firefox 148 — Linux
	{
		UserAgent:  "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:148.0) Gecko/20100101 Firefox/148.0",
		TLSProfile: ProfileFirefox148, Browser: "firefox", OS: "linux",
	},

	// Brave 146 — Windows.
	// Brave deliberately does NOT identify itself in the User-Agent: it sends
	// a stock Chromium/Chrome UA on every platform. So a correct entry pairs
	// brave_146 with a Chrome/146 UA, which looks wrong but is what real Brave
	// emits. Do NOT "fix" this to a Brave-branded UA — none exists. The
	// distinguishing signal is the TLS fingerprint (Brave_146 sets
	// RandomExtensionOrder: true), not the UA.
	{
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		TLSProfile: ProfileBrave146, Browser: "brave", OS: "windows",
	},
	// Brave 146 — macOS (Chrome/146 UA, see Windows entry rationale).
	{
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		TLSProfile: ProfileBrave146, Browser: "brave", OS: "macos",
	},
	// Brave 146 — Linux (Chrome/146 UA, see Windows entry rationale).
	{
		UserAgent:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		TLSProfile: ProfileBrave146, Browser: "brave", OS: "linux",
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
//
// The sec-ch-ua brand list is derived from the UA's Chrome major so the
// client-hint brands agree with the User-Agent (and the TLS profile): a real
// Chrome sends THREE brands — a GREASE brand, "Chromium";v="<major>", and
// "Google Chrome";v="<major>". Edge replaces "Google Chrome" with
// "Microsoft Edge";v="<edge major>".
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

	const greaseBrand = `"Not_A Brand";v="24"`

	var secChUa string
	if strings.Contains(ua, "Edg/") {
		edgeVersion := extractEdgeVersion(ua)
		secChUa = fmt.Sprintf(`%s, "Chromium";v="%s", "Microsoft Edge";v="%s"`, greaseBrand, version, edgeVersion)
	} else {
		secChUa = fmt.Sprintf(`%s, "Chromium";v="%s", "Google Chrome";v="%s"`, greaseBrand, version, version)
	}

	hints := map[string]string{
		"sec-ch-ua":          secChUa,
		"sec-ch-ua-mobile":   mobile,
		"sec-ch-ua-platform": fmt.Sprintf(`"%s"`, platform),
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
