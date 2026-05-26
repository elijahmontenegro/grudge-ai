package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Web tools — WebSearch hits a configured search backend (SearXNG
// JSON API). WebFetch issues a plain GET. Both are network-side and
// not subject to the plan-mode write guard.

func registerWebTools(c *buildCtx) error {
	webSearch, err := functiontool.New(
		functiontool.Config{Name: "WebSearch", Description: "Search the web for current information. Returns titles, URLs, and snippets."},
		func(ctx tool.Context, args WebSearchArgs) (WebSearchResult, error) {
			if c.deps.SearchURL == "" {
				return WebSearchResult{}, fmt.Errorf("web search not configured — set a search provider (e.g. SearXNG) in Settings")
			}
			searchURL := c.deps.SearchURL + "/search?format=json&q=" + args.Query
			req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
			if err != nil {
				return WebSearchResult{}, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return WebSearchResult{}, fmt.Errorf("search request: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return WebSearchResult{}, fmt.Errorf("search returned %d", resp.StatusCode)
			}
			var searchResp struct {
				Results []struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					Content string `json:"content"`
				} `json:"results"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
				return WebSearchResult{}, fmt.Errorf("parse search results: %w", err)
			}
			var results []string
			for _, r := range searchResp.Results {
				if len(results) >= 8 {
					break
				}
				entry := r.Title + " — " + r.URL
				if r.Content != "" {
					entry += "\n" + r.Content
				}
				results = append(results, entry)
			}
			if len(results) == 0 {
				results = []string{"No results found for: " + args.Query}
			}
			return WebSearchResult{Results: results}, nil
		},
	)
	if err := c.addTool("WebSearch", webSearch, err); err != nil {
		return err
	}

	webFetch, err := functiontool.New(
		functiontool.Config{Name: "WebFetch", Description: "Fetch content from a URL."},
		func(ctx tool.Context, args WebFetchArgs) (WebFetchResult, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
			if err != nil {
				return WebFetchResult{}, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return WebFetchResult{}, err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return WebFetchResult{Output: string(body), StatusCode: resp.StatusCode}, nil
		},
	)
	return c.addTool("WebFetch", webFetch, err)
}
