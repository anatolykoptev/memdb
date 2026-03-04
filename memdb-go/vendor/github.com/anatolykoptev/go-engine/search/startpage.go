package search

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
)

const metricStartpageRequests = "startpage_requests"

// SearchStartpageDirect queries Startpage directly using browser TLS fingerprint.
// Returns results compatible with the SearXNG pipeline.
func SearchStartpageDirect(ctx context.Context, bc BrowserDoer, query, language string, m *metrics.Registry) ([]sources.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if language == "" || language == languageAll {
		language = "english"
	}

	if m != nil {
		m.Incr(metricStartpageRequests)
	}

	formBody := fmt.Sprintf("query=%s&cat=web&language=%s", url.QueryEscape(query), url.QueryEscape(language))

	headers := fetch.ChromeHeaders()
	headers["referer"] = "https://www.startpage.com/"
	headers["content-type"] = "application/x-www-form-urlencoded"
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	data, _, status, err := bc.Do(http.MethodPost, "https://www.startpage.com/sp/search", headers, strings.NewReader(formBody))
	if err != nil {
		return nil, fmt.Errorf("startpage request: %w", err)
	}
	if status == http.StatusTooManyRequests || status == http.StatusForbidden {
		return nil, &ErrRateLimited{Engine: "startpage"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("startpage status %d", status)
	}
	if isStartpageRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "startpage"}
	}

	results, err := ParseStartpageHTML(data)
	if err != nil {
		return nil, fmt.Errorf("startpage parse: %w", err)
	}

	slog.Debug("startpage direct results", slog.Int("count", len(results)), slog.String("query", query))
	return results, nil
}

// ParseStartpageHTML extracts search results from Startpage HTML response.
func ParseStartpageHTML(data []byte) ([]sources.Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []sources.Result

	// Startpage result blocks: <div class="w-gl__result"> or <div class="result">
	doc.Find(".w-gl__result, .result").Each(func(_ int, s *goquery.Selection) {
		// Title + URL from <a> inside heading.
		link := s.Find("a.w-gl__result-title, h3 a, a.result-link").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")
		if !exists || title == "" {
			return
		}

		// Description.
		desc := s.Find("p.w-gl__description, .w-gl__description, p.result-description").First()
		content := strings.TrimSpace(desc.Text())

		// Skip empty/ad results.
		if href == "" || strings.Contains(href, "startpage.com/do/") {
			return
		}

		results = append(results, sources.Result{
			Title:    title,
			Content:  content,
			URL:      href,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "startpage"},
		})
	})

	return results, nil
}

// isStartpageRateLimited returns true when the response body contains
// markers indicating Startpage has blocked or rate-limited the request.
func isStartpageRateLimited(body []byte) bool {
	lower := bytes.ToLower(body)
	markers := [][]byte{
		[]byte("rate limited"),
		[]byte("too many requests"),
		[]byte("g-recaptcha"),
		[]byte("captcha"),
	}
	for _, m := range markers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}
