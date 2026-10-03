package provider

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// SonarBaseURL is the default Perplexity API root.
const SonarBaseURL = "https://api.perplexity.ai"

// SonarModel is the Perplexity model used for search.
const SonarModel = "sonar"

// Sonar searches via Perplexity's Sonar model. Sonar is an answer engine, so
// the "results" are the sources it cited while answering the query.
type Sonar struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewSonar constructs a Sonar provider.
func NewSonar(apiKey string, opts Options) *Sonar {
	base := opts.BaseURL
	if base == "" {
		base = SonarBaseURL
	}
	return &Sonar{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(base, "/"),
		client:  newHTTPClient(opts, SonarTimeout),
	}
}

// Name implements Provider.
func (s *Sonar) Name() string { return "sonar" }

type sonarMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type sonarRequest struct {
	Model            string              `json:"model"`
	Messages         []sonarMessage      `json:"messages"`
	MaxTokens        int                 `json:"max_tokens,omitempty"`
	WebSearchOptions *sonarSearchOptions `json:"web_search_options,omitempty"`
}

type sonarSearchOptions struct {
	SearchContextSize string `json:"search_context_size,omitempty"`
}

type sonarResponse struct {
	Choices []struct {
		Message sonarMessage `json:"message"`
	} `json:"choices"`
	Citations     []string `json:"citations"`
	SearchResults []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Date    string `json:"date"`
		Snippet string `json:"snippet"`
	} `json:"search_results"`
}

// Search implements Provider. Perplexity doesn't accept a result count, so we
// request a larger search context for bigger asks and truncate client-side.
func (s *Sonar) Search(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	n := clampNum(numResults, 50)
	contextSize := "low"
	switch {
	case n > 10:
		contextSize = "high"
	case n > 5:
		contextSize = "medium"
	}
	req := sonarRequest{
		Model:            SonarModel,
		Messages:         []sonarMessage{{Role: "user", Content: query}},
		WebSearchOptions: &sonarSearchOptions{SearchContextSize: contextSize},
	}
	var resp sonarResponse
	if err := postJSON(ctx, s.client, "Sonar", s.baseURL+"/chat/completions", s.headers(), req, &resp); err != nil {
		return nil, err
	}

	answer := ""
	if len(resp.Choices) > 0 {
		answer = collapseWhitespace(resp.Choices[0].Message.Content)
	}

	seen := map[string]bool{}
	out := make([]SearchResult, 0, n)
	add := func(title, u, snippet string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] || len(out) >= n {
			return
		}
		seen[u] = true
		if title == "" {
			title = hostOf(u)
		}
		if snippet == "" {
			// Sonar's answer is grounded in these sources; use it as context when
			// no per-source snippet is available.
			snippet = truncate(answer, 300)
		}
		out = append(out, SearchResult{Title: strings.TrimSpace(title), URL: u, Snippet: truncate(collapseWhitespace(snippet), 600)})
	}
	for _, r := range resp.SearchResults {
		add(r.Title, r.URL, r.Snippet)
	}
	for _, c := range resp.Citations {
		add("", c, "")
	}
	return out, nil
}

// Validate implements Provider with a minimal completion. Perplexity
// rejects max_tokens below 16, so use the API minimum.
func (s *Sonar) Validate(ctx context.Context) error {
	req := sonarRequest{
		Model:     SonarModel,
		Messages:  []sonarMessage{{Role: "user", Content: "hello"}},
		MaxTokens: 16,
	}
	return postJSON(ctx, s.client, "Sonar", s.baseURL+"/chat/completions", s.headers(), req, nil)
}

func (s *Sonar) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + s.apiKey}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(u.Host, "www.")
}
