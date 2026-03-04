package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/text"
)

const (
	ddgDJSTruncateLen  = 200 // truncation length for d.js error snippet
	metricDDGRequests  = "ddg_requests"
	directResultScore  = 1.0 // direct scraper results get full score
)

var vqdPatterns = []*regexp.Regexp{
	regexp.MustCompile(`vqd='([^']+)'`),
	regexp.MustCompile(`vqd="([^"]+)"`),
	regexp.MustCompile(`vqd=([a-zA-Z0-9_-]+)`),
}

// ddgResult represents a single DuckDuckGo search result from d.js.
type ddgResult struct {
	T string `json:"t"` // title
	A string `json:"a"` // abstract/content (HTML)
	U string `json:"u"` // URL
	C string `json:"c"` // content URL (alternative)
}

// SearchDDGDirect queries DuckDuckGo directly using browser TLS fingerprint.
// Uses the HTML lite endpoint as primary, falls back to d.js JSON API.
func SearchDDGDirect(ctx context.Context, bc BrowserDoer, query, region string, m *metrics.Registry) ([]sources.Result, error) {
	if region == "" {
		region = "wt-wt"
	}

	if m != nil {
		m.Incr(metricDDGRequests)
	}

	// Primary: HTML lite endpoint (more reliable, no VQD needed)
	results, err := ddgSearchHTML(ctx, bc, query, region)
	if err == nil && len(results) > 0 {
		slog.Debug("ddg direct results (html)", slog.Int("count", len(results)))
		return results, nil
	}
	if err != nil {
		slog.Debug("ddg html failed, trying d.js", slog.Any("error", err))
	}

	// Fallback: d.js JSON API (needs VQD token)
	vqd, err := ddgGetVQD(ctx, bc, query)
	if err != nil {
		return nil, fmt.Errorf("ddg vqd: %w", err)
	}
	results, err = ddgSearchDJS(ctx, bc, query, vqd, region)
	if err != nil {
		return nil, fmt.Errorf("ddg d.js: %w", err)
	}

	slog.Debug("ddg direct results (d.js)", slog.Int("count", len(results)))
	return results, nil
}

func ddgSearchHTML(ctx context.Context, bc BrowserDoer, query, region string) ([]sources.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	formBody := fmt.Sprintf("q=%s&kl=%s&df=", url.QueryEscape(query), url.QueryEscape(region))

	headers := fetch.ChromeHeaders()
	headers["referer"] = "https://html.duckduckgo.com/"
	headers["content-type"] = "application/x-www-form-urlencoded"

	data, _, status, err := bc.Do(http.MethodPost, "https://html.duckduckgo.com/html/", headers, strings.NewReader(formBody))
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests || status == http.StatusForbidden {
		return nil, &ErrRateLimited{Engine: "ddg"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("ddg html status %d", status)
	}
	if isDDGRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "ddg"}
	}

	return ParseDDGHTML(data)
}

// ParseDDGHTML extracts search results from DDG HTML lite response.
func ParseDDGHTML(data []byte) ([]sources.Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []sources.Result

	doc.Find(".result, .web-result").Each(func(_ int, s *goquery.Selection) {
		link := s.Find("a.result__a, .result__title a, a.result-link").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")
		if !exists || title == "" {
			return
		}

		href = DDGUnwrapURL(href)
		if href == "" {
			return
		}

		snippet := s.Find(".result__snippet, .result__body").First()
		content := strings.TrimSpace(snippet.Text())

		results = append(results, sources.Result{
			Title:    title,
			Content:  content,
			URL:      href,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "ddg"},
		})
	})

	return results, nil
}

// DDGUnwrapURL extracts the actual URL from DDG redirect wrappers.
// DDG HTML wraps links as: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=...
func DDGUnwrapURL(href string) string {
	if strings.Contains(href, "duckduckgo.com/l/") || strings.Contains(href, "uddg=") {
		if u, err := url.Parse(href); err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	if strings.HasPrefix(href, "http") {
		return href
	}
	return ""
}

func ddgGetVQD(ctx context.Context, bc BrowserDoer, query string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	u := "https://duckduckgo.com/?q=" + url.QueryEscape(query)

	headers := fetch.ChromeHeaders()
	headers["referer"] = "https://duckduckgo.com/"

	data, _, status, err := bc.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("ddg homepage status %d", status)
	}

	body := string(data)
	for _, pat := range vqdPatterns {
		if m := pat.FindStringSubmatch(body); len(m) > 1 {
			return m[1], nil
		}
	}

	return "", fmt.Errorf("vqd token not found in response (%d bytes)", len(data))
}

func ddgSearchDJS(ctx context.Context, bc BrowserDoer, query, vqd, region string) ([]sources.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	params := url.Values{
		"q":   {query},
		"vqd": {vqd},
		"kl":  {region},
		"df":  {""},
		"l":   {"us-en"},
		"o":   {"json"},
	}
	u := "https://links.duckduckgo.com/d.js?" + params.Encode()

	headers := fetch.ChromeHeaders()
	headers["referer"] = "https://duckduckgo.com/"
	headers["accept"] = "application/json, text/javascript, */*; q=0.01"

	data, _, status, err := bc.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests || status == http.StatusForbidden {
		return nil, &ErrRateLimited{Engine: "ddg"}
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return nil, fmt.Errorf("ddg d.js status %d", status)
	}

	return ParseDDGResponse(data)
}

// ParseDDGResponse extracts search results from DDG d.js response.
// The response may be JSONP or raw JSON array.
func ParseDDGResponse(data []byte) ([]sources.Result, error) {
	body := strings.TrimSpace(string(data))

	// Strip JSONP wrapper if present: DDGjsonp_xxx({results:[...]})
	if idx := strings.Index(body, "["); idx >= 0 {
		end := strings.LastIndex(body, "]")
		if end > idx {
			body = body[idx : end+1]
		}
	}

	var raw []ddgResult
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("ddg json parse: %w (first %d bytes: %s)", err, ddgDJSTruncateLen, text.Truncate(body, ddgDJSTruncateLen))
	}

	var results []sources.Result
	for _, r := range raw {
		resultURL := r.U
		if resultURL == "" {
			resultURL = r.C
		}
		if resultURL == "" || r.T == "" {
			continue
		}
		// Skip DDG internal/ad entries.
		if strings.HasPrefix(resultURL, "https://duckduckgo.com/") {
			continue
		}
		results = append(results, sources.Result{
			Title:    text.CleanHTML(r.T),
			Content:  text.CleanHTML(r.A),
			URL:      resultURL,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "ddg"},
		})
	}

	return results, nil
}

// ExtractVQD extracts the VQD token from DDG response HTML.
func ExtractVQD(body string) string {
	for _, pat := range vqdPatterns {
		if m := pat.FindStringSubmatch(body); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// isDDGRateLimited checks whether the DDG response body indicates a CAPTCHA
// or rate-limit page. It looks for known captcha markers on a lowercased copy.
func isDDGRateLimited(body []byte) bool {
	low := bytes.ToLower(body)
	for _, marker := range [][]byte{
		[]byte("please try again"),
		[]byte("not a robot"),
		[]byte("unusual traffic"),
		[]byte("blocked"),
	} {
		if bytes.Contains(low, marker) {
			return true
		}
	}
	// DDG captcha form: action="/d.js" combined with type="hidden".
	return bytes.Contains(low, []byte(`action="/d.js"`)) &&
		bytes.Contains(low, []byte(`type="hidden"`))
}
