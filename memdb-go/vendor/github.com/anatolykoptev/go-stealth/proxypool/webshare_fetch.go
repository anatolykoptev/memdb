package proxypool

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// fetchAllProxies retrieves all pages from the Webshare API.
// It stops after webshareMaxPages pages to guard against infinite loops.
func fetchAllProxies(apiKey string, cfg WebshareConfig) ([]webshareProxy, error) {
	firstURL := buildAPIURL(cfg)
	client := cfg.HTTPClient

	var all []webshareProxy
	nextURL := firstURL
	for range webshareMaxPages {
		page, next, err := fetchPageWithNext(client, nextURL, apiKey)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		nextURL = next
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("proxy: webshare returned 0 proxies")
	}
	return all, nil
}

// fetchPage fetches a single API page and returns raw proxy entries (no next link).
func fetchPage(client *http.Client, apiURL, apiKey string) ([]webshareProxy, error) {
	proxies, _, err := fetchPageWithNext(client, apiURL, apiKey)
	return proxies, err
}

// fetchPageWithNext fetches one page and returns proxies + the next-page URL (empty if none).
func fetchPageWithNext(client *http.Client, apiURL, apiKey string) ([]webshareProxy, string, error) {
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("proxy: build request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("proxy: fetch list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("proxy: webshare API returned %d: %s", resp.StatusCode, string(body))
	}

	var data webshareResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", fmt.Errorf("proxy: decode response: %w", err)
	}

	next := ""
	if data.Next != nil {
		next = *data.Next
	}
	return data.Results, next, nil
}
