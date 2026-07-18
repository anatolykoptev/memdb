package proxypool

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var isoCountryRE = regexp.MustCompile(`^[A-Z]{2}$`)

// applyConfigDefaults fills zero-value fields with sensible defaults and validates Countries.
func applyConfigDefaults(cfg *WebshareConfig) error {
	if len(cfg.Countries) == 0 {
		cfg.Countries = []string{"US"}
	} else {
		deduped, err := validateAndDedup(cfg.Countries)
		if err != nil {
			return err
		}
		cfg.Countries = deduped
	}

	if cfg.PageSize <= 0 {
		cfg.PageSize = 100
	}

	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return nil
}

// validateAndDedup checks ISO-2 format and removes duplicates preserving order.
func validateAndDedup(countries []string) ([]string, error) {
	seen := make(map[string]bool, len(countries))
	result := make([]string, 0, len(countries))
	for _, cc := range countries {
		if !isoCountryRE.MatchString(cc) {
			return nil, fmt.Errorf("proxy: invalid country code %q, expected ISO-2", cc)
		}
		if !seen[cc] {
			seen[cc] = true
			result = append(result, cc)
		}
	}
	return result, nil
}

// buildAPIURL constructs the Webshare API URL from config, including mode, page_size,
// and country filter query parameters.
func buildAPIURL(cfg WebshareConfig) string {
	base := buildBaseURL(cfg.BaseURL)

	mode := "backbone"
	if cfg.Mode == ModeDirect {
		mode = "direct"
	}

	params := fmt.Sprintf("?mode=%s&page_size=%d", mode, cfg.PageSize)
	if len(cfg.Countries) > 0 {
		params += "&country_code__in=" + strings.Join(cfg.Countries, ",")
	}
	return base + params
}

// buildBaseURL returns the base URL without query params.
func buildBaseURL(override string) string {
	if override != "" {
		return override
	}
	return webshareDefaultBase
}

// injectCountryModifiers rewrites proxy usernames with country suffixes.
// For a single country: username → username-CC.
// For multiple countries: each proxy is duplicated once per country.
func injectCountryModifiers(proxies []webshareProxy, countries []string, mode WebshareMode) []string {
	suffix := func(cc string) string {
		if mode == ModeRotating {
			return "-" + cc + "-rotate"
		}
		return "-" + cc
	}

	result := make([]string, 0, len(proxies)*len(countries))
	for _, p := range proxies {
		host := p.ProxyAddress
		if host == "" {
			host = webshareDefaultHost
		}
		for _, cc := range countries {
			username := p.Username + suffix(cc)
			u := fmt.Sprintf("http://%s:%s@%s:%d", username, p.Password, host, p.Port)
			result = append(result, u)
		}
	}
	return result
}
