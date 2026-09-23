// Package provider defines the search Provider interface and implementations
// for Exa, Parallel, Sonar (Perplexity), DuckDuckGo, and SearXNG.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// SearchResult is a single, provider-agnostic web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	// Content is the provider's full excerpt text when it returns more than
	// a snippet (the keyless Exa and Parallel MCP backends do). It is not
	// sent to Jev for qualification; it stands in for a page that cannot
	// be fetched.
	Content string `json:"content,omitempty"`
}

// Provider is a web search backend.
type Provider interface {
	// Name returns the short provider identifier (exa, parallel, sonar, ddg, searxng).
	Name() string
	// Search runs query and returns up to numResults results.
	Search(ctx context.Context, query string, numResults int) ([]SearchResult, error)
	// Validate performs a lightweight call to confirm the backend is reachable
	// and, for keyed providers, that the API key works.
	Validate(ctx context.Context) error
}

// Options customize provider construction. The zero value is fine.
type Options struct {
	// BaseURL overrides the provider's API root (useful for tests and proxies).
	BaseURL string
	// HTTPClient overrides the default client. A per-provider timeout is
	// applied only when this is nil.
	HTTPClient *http.Client
}

// DefaultTimeout bounds a single provider request.
const DefaultTimeout = 30 * time.Second

// SonarTimeout is longer because Sonar runs an LLM before answering.
const SonarTimeout = 90 * time.Second

// Names returns the supported provider names in auto-chain order: keyed
// providers first, then the keyless fallbacks.
func Names() []string {
	return []string{"exa", "parallel", "sonar", "youcom", "brave", "tavily", "firecrawl", "keenable", "serpbase", "serply", "keiro", "ketch", "ddg", "searxng", "degoog"}
}

// Keyed lists the providers that require an API key.
// Exa, Parallel, You.com, Firecrawl, and Keenable also run keyless when
// named explicitly.
func Keyed() []string {
	return []string{"brave", "exa", "parallel", "sonar", "youcom", "tavily", "firecrawl", "keenable", "serpbase", "serply", "keiro"}
}

// Normalize lowercases and trims a provider name, resolving aliases.
func Normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "perplexity":
		return "sonar"
	case "duckduckgo":
		return "ddg"
	case "you", "you.com":
		return "youcom"
	}
	return name
}

// KeylessChain is the order tried when no search key is configured: the
// hosted endpoints that answer without a key, best first, then DuckDuckGo.
// Each throttles by IP after a few dozen queries a day; the chain's
// cooldowns rotate past a throttled one. ketch is deliberately absent: it
// is a separate binary that covers the same endpoints, and is reachable
// with -p ketch.
func KeylessChain() []string {
	return []string{"parallel", "exa", "keenable", "youcom", "firecrawl", "ddg"}
}

// Keyless reports whether the named provider works without an API key.
// Exa and Parallel fall back to their hosted MCP endpoints without one.
func Keyless(name string) bool {
	switch Normalize(name) {
	case "ddg", "searxng", "degoog", "exa", "parallel", "youcom", "ketch", "firecrawl", "keenable":
		return true
	}
	return false
}

// New constructs the named provider. cred is the API key for keyed providers
// and the instance URL for searxng; ddg ignores it. Exa and Parallel accept
// an empty cred and use their keyless MCP endpoints.
func New(name, cred string, opts Options) (Provider, error) {
	switch Normalize(name) {
	case "ddg":
		return NewDDG(opts), nil
	case "searxng":
		if cred == "" && opts.BaseURL == "" {
			return nil, errors.New("searxng: instance URL is empty")
		}
		return NewSearXNG(cred, opts), nil
	case "degoog":
		if cred == "" && opts.BaseURL == "" {
			return nil, errors.New("degoog: instance URL is empty")
		}
		return NewDegoog(cred, opts), nil
	case "exa":
		return NewExa(cred, opts), nil
	case "parallel":
		return NewParallel(cred, opts), nil
	case "youcom":
		return NewYoucom(cred, opts), nil
	case "firecrawl":
		return NewFirecrawl(cred, opts), nil
	case "keenable":
		return NewKeenable(cred, opts), nil
	case "ketch":
		return NewKetch(cred, opts), nil
	}
	if cred == "" {
		return nil, fmt.Errorf("%s: API key is empty", name)
	}
	switch Normalize(name) {
	case "sonar":
		return NewSonar(cred, opts), nil
	case "brave":
		return NewBrave(cred, opts), nil
	case "tavily":
		return NewTavily(cred, opts), nil
	case "serpbase":
		return NewSerpBase(cred, opts), nil
	case "serply":
		return NewSerply(cred, opts), nil
	case "keiro":
		return NewKeiro(cred, opts), nil
	}
	names := Names()
	sort.Strings(names)
	return nil, fmt.Errorf("unknown provider %q (expected one of %s)", name, strings.Join(names, ", "))
}

// clampNum normalizes a requested result count.
func clampNum(n, max int) int {
	if n <= 0 {
		return 10
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

// truncate shortens s to at most n runes, appending "…" when cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// collapseWhitespace flattens newlines and runs of spaces into single spaces.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
