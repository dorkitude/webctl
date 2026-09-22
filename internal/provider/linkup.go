package provider

import (
	"context"
	"net/http"
	"strings"
)

// LinkupBaseURL is the Linkup API root.
const LinkupBaseURL = "https://api.linkup.so"

// Linkup searches via Linkup's search API (keyed). The call asks for raw
// results at depth "fast": one-shot retrieval that fits the chain's attempt
// budget. Jev scores the hits, so this does not request a written answer
// or a multi-step search.
type Linkup struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewLinkup constructs a Linkup provider.
func NewLinkup(apiKey string, opts Options) *Linkup {
	base := opts.BaseURL
	if base == "" {
		base = LinkupBaseURL
	}
	return &Linkup{apiKey: apiKey, baseURL: strings.TrimRight(base, "/"), client: newHTTPClient(opts, DefaultTimeout)}
}

// Name implements Provider.
func (l *Linkup) Name() string { return "linkup" }

type linkupResponse struct {
	Results []struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Type    string `json:"type"`
	} `json:"results"`
}

// Search implements Provider.
func (l *Linkup) Search(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	body := map[string]any{
		"q":          query,
		"depth":      "fast",
		"outputType": "searchResults",
		"maxResults": clampNum(numResults, 20),
	}
	var resp linkupResponse
	if err := postJSON(ctx, l.client, "Linkup", l.baseURL+"/v1/search", map[string]string{"Authorization": "Bearer " + l.apiKey}, body, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Type != "" && r.Type != "text" {
			continue
		}
		if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.URL) == "" {
			continue
		}
		content := strings.TrimSpace(r.Content)
		out = append(out, SearchResult{Title: collapseWhitespace(r.Name), URL: strings.TrimSpace(r.URL), Snippet: excerpt(content), Content: content})
	}
	return out, nil
}

// Validate implements Provider with a one-result search.
func (l *Linkup) Validate(ctx context.Context) error {
	_, err := l.Search(ctx, "hello world", 1)
	return err
}
