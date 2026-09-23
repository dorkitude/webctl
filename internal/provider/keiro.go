package provider

import (
	"context"
	"net/http"
	"strings"
)

// KeiroBaseURL is the Keiro API root.
const KeiroBaseURL = "https://kierolabs.space"

// Keiro searches via the KeiroLabs agent-oriented v2 API (keyed; free tier
// of 10 queries a minute, then pay-per-query at fractions of a cent). The
// fast endpoint is always-fresh index search, about a second per query.
type Keiro struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewKeiro constructs a Keiro provider.
func NewKeiro(apiKey string, opts Options) *Keiro {
	base := opts.BaseURL
	if base == "" {
		base = KeiroBaseURL
	}
	return &Keiro{apiKey: apiKey, baseURL: strings.TrimRight(base, "/"), client: newHTTPClient(opts, DefaultTimeout)}
}

// Name implements Provider.
func (k *Keiro) Name() string { return "keiro" }

type keiroResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

// Search implements Provider.
func (k *Keiro) Search(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	// maxResults accepts up to 50 per request.
	body := map[string]any{"query": query, "maxResults": clampNum(numResults, 50)}
	var resp keiroResponse
	if err := postJSON(ctx, k.client, "Keiro", k.baseURL+"/api/v2/search/fast", map[string]string{"Authorization": "Bearer " + k.apiKey}, body, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, SearchResult{Title: collapseWhitespace(r.Title), URL: strings.TrimSpace(r.URL), Snippet: collapseWhitespace(r.Snippet)})
	}
	return out, nil
}

// Validate implements Provider with a one-result search.
func (k *Keiro) Validate(ctx context.Context) error {
	_, err := k.Search(ctx, "hello world", 1)
	return err
}
