package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ParallelBaseURL is the default Parallel API root.
const ParallelBaseURL = "https://api.parallel.ai"

// ParallelMCPURL is Parallel's hosted Search MCP server, usable without a key.
const ParallelMCPURL = "https://search.parallel.ai/mcp"

// Parallel searches via https://parallel.ai: the REST API with a key, the
// hosted MCP server without one.
type Parallel struct {
	apiKey  string
	baseURL string
	mcpURL  string
	client  *http.Client
}

// NewParallel constructs a Parallel provider. An empty apiKey selects the
// keyless MCP endpoint; opts.BaseURL then overrides it as <BaseURL>/mcp.
func NewParallel(apiKey string, opts Options) *Parallel {
	base := opts.BaseURL
	mcp := ParallelMCPURL
	if base == "" {
		base = ParallelBaseURL
	} else {
		mcp = strings.TrimRight(base, "/") + "/mcp"
	}
	return &Parallel{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(base, "/"),
		mcpURL:  mcp,
		client:  newHTTPClient(opts, DefaultTimeout),
	}
}

// Keyless reports whether this instance uses the MCP endpoint.
func (p *Parallel) Keyless() bool { return p.apiKey == "" }

// Name implements Provider.
func (p *Parallel) Name() string { return "parallel" }

// parallelRequest is the keyed REST body. The API requires search_queries
// and rejects extra fields like query and max_results; it takes no result
// limit, so the limit is applied locally.
type parallelRequest struct {
	SearchQueries []string `json:"search_queries"`
}

// parallelResult is decoded leniently: Parallel has shipped both a single
// "snippet"/"description" string and an "excerpts" array for result text.
type parallelResult struct {
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	Snippet     string      `json:"snippet"`
	Description string      `json:"description"`
	Excerpts    stringSlice `json:"excerpts"`
}

type parallelResponse struct {
	Results []parallelResult `json:"results"`
}

// Search implements Provider.
func (p *Parallel) Search(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	if p.Keyless() {
		return p.searchMCP(ctx, query, numResults)
	}
	req := parallelRequest{SearchQueries: []string{query}}
	var resp parallelResponse
	if err := postJSON(ctx, p.client, "Parallel", p.baseURL+"/v1/search", p.headers(), req, &resp); err != nil {
		return nil, err
	}
	limit := clampNum(numResults, 100)
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if len(out) >= limit {
			break
		}
		if sr, ok := toSearchResult(r); ok {
			out = append(out, sr)
		}
	}
	return out, nil
}

// searchMCP calls the web_search tool. Each text block is a JSON object with
// the same result shape as the REST API. The tool takes no result limit, so
// it is applied locally.
func (p *Parallel) searchMCP(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	texts, err := callMCPTool(ctx, p.client, "Parallel", p.mcpURL, "web_search", map[string]any{
		"objective":      query,
		"search_queries": []string{query},
	})
	if err != nil {
		return nil, err
	}
	limit := clampNum(numResults, 100)
	var out []SearchResult
	for _, t := range texts {
		var payload parallelResponse
		if err := json.Unmarshal([]byte(t), &payload); err != nil {
			return nil, fmt.Errorf("Parallel: decode results: %w", err)
		}
		for _, r := range payload.Results {
			if len(out) >= limit {
				return out, nil
			}
			if sr, ok := toSearchResult(r); ok {
				out = append(out, sr)
			}
		}
	}
	return out, nil
}

// toSearchResult maps one Parallel result, dropping ones without a title or URL.
func toSearchResult(r parallelResult) (SearchResult, bool) {
	title, url := strings.TrimSpace(r.Title), strings.TrimSpace(r.URL)
	if title == "" || url == "" {
		return SearchResult{}, false
	}
	content := strings.TrimSpace(strings.Join(r.Excerpts, "\n"))
	snippet := r.Snippet
	if snippet == "" {
		snippet = content
	}
	if snippet == "" {
		snippet = r.Description
	}
	return SearchResult{
		Title:   collapseWhitespace(title),
		URL:     url,
		Snippet: excerpt(snippet),
		Content: content,
	}, true
}

// Validate implements Provider with a one-result hello-world search.
func (p *Parallel) Validate(ctx context.Context) error {
	if p.Keyless() {
		_, err := p.searchMCP(ctx, "hello world", 1)
		return err
	}
	req := parallelRequest{SearchQueries: []string{"hello world"}}
	return postJSON(ctx, p.client, "Parallel", p.baseURL+"/v1/search", p.headers(), req, nil)
}

func (p *Parallel) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// stringSlice unmarshals from either a JSON array of strings or a single string.
type stringSlice []string

func (s *stringSlice) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = nil
		return nil
	}
	if b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}
