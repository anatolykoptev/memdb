package stealth

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-stealth/internal/uri"
	"golang.org/x/sync/singleflight"
)

// knownWAFs is the list of WAF/CDN technology names to detect.
var knownWAFs = []string{
	"Cloudflare", "Akamai", "Sucuri", "Imperva", "AWS WAF",
	"Fastly", "StackPath", "Barracuda", "F5 BIG-IP",
}

// SiteInfo contains cached intelligence about a domain.
type SiteInfo struct {
	WAF          string        // Detected WAF/CDN name, or ""
	Technologies []AnalyzeTech // Full tech list
	FetchedAt    time.Time
}

// HasTech returns true if the site has the named technology.
func (si *SiteInfo) HasTech(name string) bool {
	for _, t := range si.Technologies {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}

// SiteIntel provides per-domain tech/WAF intelligence with caching.
type SiteIntel struct {
	client *OxBrowserClient
	ttl    time.Duration

	mu    sync.RWMutex
	cache map[string]*SiteInfo
	sf    singleflight.Group
}

// NewSiteIntel creates a SiteIntel cache backed by ox-browser /analyze.
func NewSiteIntel(client *OxBrowserClient) *SiteIntel {
	return &SiteIntel{
		client: client,
		ttl:    1 * time.Hour,
		cache:  make(map[string]*SiteInfo),
	}
}

// Get returns cached SiteInfo for the domain, fetching via /analyze if needed.
// Concurrent calls for the same domain are deduplicated via singleflight.
func (si *SiteIntel) Get(rawURL string) (*SiteInfo, error) {
	domain := uri.ExtractHost(rawURL)

	si.mu.RLock()
	if info, ok := si.cache[domain]; ok && time.Since(info.FetchedAt) < si.ttl {
		si.mu.RUnlock()
		return info, nil
	}
	si.mu.RUnlock()

	v, err, _ := si.sf.Do(domain, func() (interface{}, error) {
		// Double-check cache after winning the singleflight race.
		si.mu.RLock()
		if info, ok := si.cache[domain]; ok && time.Since(info.FetchedAt) < si.ttl {
			si.mu.RUnlock()
			return info, nil
		}
		si.mu.RUnlock()

		resp, err := si.client.Analyze(context.Background(), rawURL)
		if err != nil {
			slog.Debug("site_intel: analyze failed", slog.String("url", rawURL), slog.Any("error", err))
			return &SiteInfo{}, err
		}

		info := &SiteInfo{
			Technologies: resp.Technologies,
			FetchedAt:    time.Now(),
		}
		for _, t := range resp.Technologies {
			for _, waf := range knownWAFs {
				if strings.EqualFold(t.Name, waf) {
					info.WAF = waf
					break
				}
			}
			if info.WAF != "" {
				break
			}
		}

		si.mu.Lock()
		si.cache[domain] = info
		si.mu.Unlock()

		return info, nil
	})
	if err != nil {
		if info, ok := v.(*SiteInfo); ok {
			return info, err
		}
		return &SiteInfo{}, err
	}
	return v.(*SiteInfo), nil
}

// SuggestProfile returns a BrowserProfile optimized for the target site's WAF.
func (si *SiteIntel) SuggestProfile(rawURL string) BrowserProfile {
	info, err := si.Get(rawURL)
	if err != nil {
		return RandomProfile()
	}

	switch info.WAF {
	case "Cloudflare":
		// Chrome is most common on CF sites — least suspicious.
		return RandomProfile(WithBrowser("chrome"), WithMobile(false))
	case "Akamai":
		// Akamai checks TLS fingerprint aggressively — randomize browser.
		return RandomProfile(WithMobile(false))
	default:
		return RandomProfile()
	}
}
