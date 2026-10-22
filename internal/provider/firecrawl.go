package provider

import (
	"context"
	"net/http"
	"strings"
)

// FirecrawlBaseURL is the hosted Firecrawl API root; a self-hosted
// instance can be given as opts.BaseURL.
const FirecrawlBaseURL = "https://api.firecrawl.dev"

// Firecrawl searches via Firecrawl's v2 search API. The hosted endpoint
// answers without a key (IP-gated, occasionally 403); a key lifts limits.
type Firecrawl struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewFirecrawl constructs a Firecrawl provider.
func NewFirecrawl(apiKey string, opts Options) *Firecrawl {
	base := opts.BaseURL
	if base == "" {
		base = FirecrawlBaseURL
	}
	return &Firecrawl{apiKey: apiKey, baseURL: strings.TrimRight(base, "/"), client: newHTTPClient(opts, DefaultTimeout)}
}

// Name implements Provider.
func (f *Firecrawl) Name() string { return "firecrawl" }

type firecrawlResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Web []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"web"`
	} `json:"data"`
}

// Search implements Provider.
func (f *Firecrawl) Search(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	body := map[string]any{"query": query, "limit": clampNum(numResults, 20), "integration": "_webctl"}
	var headers map[string]string
	if f.apiKey != "" {
		headers = map[string]string{"Authorization": "Bearer " + f.apiKey}
	}
	var resp firecrawlResponse
	if err := postJSON(ctx, f.client, "Firecrawl", f.baseURL+"/v2/search", headers, body, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Data.Web))
	for _, r := range resp.Data.Web {
		if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, SearchResult{Title: collapseWhitespace(r.Title), URL: strings.TrimSpace(r.URL), Snippet: excerpt(r.Description)})
	}
	return out, nil
}

// Validate implements Provider with a one-result search.
func (f *Firecrawl) Validate(ctx context.Context) error {
	_, err := f.Search(ctx, "hello world", 1)
	return err
}
