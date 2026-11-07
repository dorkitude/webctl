package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// capture records the last request a mock server received.
type capture struct {
	Method  string
	Path    string
	Query   string
	Headers http.Header
	Body    map[string]any
}

// mockServer returns an httptest server that records requests and replies
// with status/body. The capture is filled in on each request.
func mockServer(t *testing.T, status int, body string) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Method = r.Method
		c.Path = r.URL.Path
		c.Query = r.URL.RawQuery
		c.Headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		c.Body = map[string]any{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.Body); err != nil {
				t.Errorf("request body is not JSON: %v\n%s", err, raw)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// mockServerFunc is mockServer with a handler that inspects the query string.
func mockServerFunc(t *testing.T, fn func(query map[string][]string) (int, string)) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Method, c.Path, c.Headers = r.Method, r.URL.Path, r.Header.Clone()
		status, body := fn(r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestNew(t *testing.T) {
	for _, name := range []string{"exa", "parallel", "sonar", "perplexity", "EXA", " Exa "} {
		p, err := New(name, "k", Options{})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		want := strings.ToLower(strings.TrimSpace(name))
		if want == "perplexity" {
			want = "sonar"
		}
		if p.Name() != want {
			t.Errorf("New(%q).Name() = %q, want %q", name, p.Name(), want)
		}
	}
	if _, err := New("bing", "k", Options{}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("unknown provider error = %v", err)
	}
	if _, err := New("sonar", "", Options{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty key error = %v", err)
	}
	if p, err := New("exa", "", Options{}); err != nil || !p.(*Exa).Keyless() {
		t.Errorf("keyless exa = %v, %v", p, err)
	}
}

func TestExaSearch(t *testing.T) {
	srv, c := mockServer(t, 200, `{"results":[
		{"title":" Attention Is All You Need ","url":"https://arxiv.org/abs/1706.03762","highlights":["We propose","the Transformer.\n\nIt   works."]},
		{"title":"No highlights","url":"https://example.com/a","summary":"A summary"},
		{"title":"Text only","url":"https://example.com/b","text":"Body text"}
	]}`)
	p := NewExa("exa-key", Options{BaseURL: srv.URL + "/"})

	got, err := p.Search(context.Background(), "attention", 3)
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != http.MethodPost || c.Path != "/search" {
		t.Errorf("request = %s %s, want POST /search", c.Method, c.Path)
	}
	if c.Headers.Get("x-api-key") != "exa-key" {
		t.Errorf("x-api-key header = %q", c.Headers.Get("x-api-key"))
	}
	if c.Body["query"] != "attention" || c.Body["numResults"] != float64(3) {
		t.Errorf("body = %v", c.Body)
	}
	if _, ok := c.Body["contents"]; !ok {
		t.Errorf("expected contents.highlights in request body: %v", c.Body)
	}
	want := []SearchResult{
		{Title: "Attention Is All You Need", URL: "https://arxiv.org/abs/1706.03762", Snippet: "We propose the Transformer. It works."},
		{Title: "No highlights", URL: "https://example.com/a", Snippet: "A summary"},
		{Title: "Text only", URL: "https://example.com/b", Snippet: "Body text", Content: "Body text"},
	}
	assertResults(t, got, want)
}

func TestExaValidate(t *testing.T) {
	srv, c := mockServer(t, 200, `{"results":[]}`)
	p := NewExa("k", Options{BaseURL: srv.URL})
	if err := p.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Body["numResults"] != float64(1) {
		t.Errorf("validate should request 1 result, got %v", c.Body["numResults"])
	}

	bad, _ := mockServer(t, 401, `{"error":"invalid api key"}`)
	p = NewExa("k", Options{BaseURL: bad.URL})
	err := p.Validate(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T %v", err, err)
	}
	if !apiErr.Unauthorized() || !IsUnauthorized(err) {
		t.Errorf("expected unauthorized, got %+v", apiErr)
	}
	if apiErr.Provider != "Exa" || apiErr.Body != "invalid api key" {
		t.Errorf("apiErr = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "keys validate") {
		t.Errorf("error text = %q", err.Error())
	}
}

func TestParallelSearch(t *testing.T) {
	srv, c := mockServer(t, 200, `{"results":[
		{"title":"One","url":"https://one.example","snippet":"snip"},
		{"title":"Two","url":"https://two.example","excerpts":["ex a","ex b"]},
		{"title":"Three","url":"https://three.example","excerpts":"single excerpt"},
		{"title":"Four","url":"https://four.example","description":"desc"}
	]}`)
	p := NewParallel("par-key", Options{BaseURL: srv.URL})

	got, err := p.Search(context.Background(), "q", 4)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "/v1/search" {
		t.Errorf("path = %q", c.Path)
	}
	if c.Headers.Get("Authorization") != "Bearer par-key" {
		t.Errorf("Authorization = %q", c.Headers.Get("Authorization"))
	}
	sq, _ := c.Body["search_queries"].([]any)
	if len(sq) != 1 || sq[0] != "q" {
		t.Errorf("body = %v", c.Body)
	}
	if _, ok := c.Body["max_results"]; ok {
		t.Errorf("API rejects max_results; the limit must be local: %v", c.Body)
	}
	want := []SearchResult{
		{Title: "One", URL: "https://one.example", Snippet: "snip"},
		{Title: "Two", URL: "https://two.example", Snippet: "ex a ex b", Content: "ex a\nex b"},
		{Title: "Three", URL: "https://three.example", Snippet: "single excerpt", Content: "single excerpt"},
		{Title: "Four", URL: "https://four.example", Snippet: "desc"},
	}
	assertResults(t, got, want)

	// The API takes no result limit, so Search applies it locally.
	got, err = p.Search(context.Background(), "q", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d results, want 2", len(got))
	}
}

func TestParallelErrors(t *testing.T) {
	srv, _ := mockServer(t, 429, `{"error":{"message":"slow down"}}`)
	p := NewParallel("k", Options{BaseURL: srv.URL})
	_, err := p.Search(context.Background(), "q", 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || apiErr.Body != "slow down" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error text = %q", err)
	}

	bad, _ := mockServer(t, 200, `not json`)
	p = NewParallel("k", Options{BaseURL: bad.URL})
	if _, err := p.Search(context.Background(), "q", 1); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestSonarSearch(t *testing.T) {
	srv, c := mockServer(t, 200, `{
		"choices":[{"message":{"role":"assistant","content":"The   answer\nis 42."}}],
		"citations":["https://a.example/x","https://b.example/y","https://c.example/z"],
		"search_results":[
			{"title":"A","url":"https://a.example/x","snippet":"about a"},
			{"title":"","url":"https://b.example/y","snippet":""}
		]
	}`)
	p := NewSonar("sonar-key", Options{BaseURL: srv.URL})

	got, err := p.Search(context.Background(), "meaning of life", 12)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "/chat/completions" {
		t.Errorf("path = %q", c.Path)
	}
	if c.Headers.Get("Authorization") != "Bearer sonar-key" {
		t.Errorf("Authorization = %q", c.Headers.Get("Authorization"))
	}
	if c.Body["model"] != SonarModel {
		t.Errorf("model = %v", c.Body["model"])
	}
	wso, _ := c.Body["web_search_options"].(map[string]any)
	if wso["search_context_size"] != "high" {
		t.Errorf("num=12 should request high context, got %v", wso)
	}
	msgs, _ := c.Body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "meaning of life" {
		t.Errorf("messages = %v", msgs)
	}
	want := []SearchResult{
		{Title: "A", URL: "https://a.example/x", Snippet: "about a"},
		// Missing title falls back to host; missing snippet falls back to the answer.
		{Title: "b.example", URL: "https://b.example/y", Snippet: "The answer is 42."},
		// Citation not in search_results is still included (deduped against the above).
		{Title: "c.example", URL: "https://c.example/z", Snippet: "The answer is 42."},
	}
	assertResults(t, got, want)
}

func TestSonarSearchTruncatesToNum(t *testing.T) {
	srv, c := mockServer(t, 200, `{"citations":["https://a.example","https://b.example","https://c.example"]}`)
	p := NewSonar("k", Options{BaseURL: srv.URL})
	got, err := p.Search(context.Background(), "q", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d results, want 2", len(got))
	}
	wso, _ := c.Body["web_search_options"].(map[string]any)
	if wso["search_context_size"] != "low" {
		t.Errorf("num=2 should request low context, got %v", wso)
	}
}

func TestSonarValidate(t *testing.T) {
	srv, c := mockServer(t, 200, `{"choices":[]}`)
	p := NewSonar("k", Options{BaseURL: srv.URL})
	if err := p.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Body["max_tokens"] != float64(16) {
		t.Errorf("validate should cap max_tokens at Perplexity's minimum, 16, got %v", c.Body["max_tokens"])
	}
}

func TestPostJSONTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	p := NewExa("k", Options{BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Search(ctx, "q", 1)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestPostJSONUsesCustomClient(t *testing.T) {
	srv, c := mockServer(t, 200, `{"results":[]}`)
	called := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	p := NewExa("k", Options{BaseURL: srv.URL, HTTPClient: client})
	if _, err := p.Search(context.Background(), "q", 1); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("custom HTTPClient was not used")
	}
	if ua := c.Headers.Get("User-Agent"); !strings.HasPrefix(ua, "webctl/") {
		t.Errorf("User-Agent = %q", ua)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSummarizeBody(t *testing.T) {
	cases := map[string]string{
		``:                                     "",
		`{"error":"bad key"}`:                  "bad key",
		`{"error":{"message":"nested\n msg"}}`: "nested msg",
		`{"message":"top level"}`:              "top level",
		`{"detail":"details"}`:                 "details",
		"plain   text\nerror":                  "plain text error",
		`{"unrelated":1}`:                      `{"unrelated":1}`,
		`{"error":"` + strings.Repeat("x", 300) + `"}`: strings.Repeat("x", 200) + "…",
	}
	for in, want := range cases {
		if got := summarizeBody([]byte(in)); got != want {
			t.Errorf("summarizeBody(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHelpers(t *testing.T) {
	if clampNum(0, 100) != 10 || clampNum(-5, 100) != 10 {
		t.Error("clampNum should default non-positive to 10")
	}
	if clampNum(500, 100) != 100 {
		t.Error("clampNum should cap at max")
	}
	if clampNum(500, 0) != 500 {
		t.Error("clampNum with max=0 should not cap")
	}
	if got := truncate("héllo wörld", 5); got != "héllo…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("  short  ", 100); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	if got := collapseWhitespace(" a \n\n b\t c "); got != "a b c" {
		t.Errorf("collapseWhitespace = %q", got)
	}
	if got := hostOf("https://www.example.com/path"); got != "example.com" {
		t.Errorf("hostOf = %q", got)
	}
	if got := hostOf("not a url"); got != "not a url" {
		t.Errorf("hostOf fallback = %q", got)
	}
}

func TestStringSlice(t *testing.T) {
	var s stringSlice
	for in, want := range map[string][]string{
		`null`:      nil,
		`"one"`:     {"one"},
		`["a","b"]`: {"a", "b"},
	} {
		s = nil
		if err := json.Unmarshal([]byte(in), &s); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if strings.Join(s, "|") != strings.Join(want, "|") {
			t.Errorf("unmarshal %s = %v, want %v", in, s, want)
		}
	}
	if err := json.Unmarshal([]byte(`123`), &s); err == nil {
		t.Error("expected error for numeric excerpts")
	}
}

func assertResults(t *testing.T, got, want []SearchResult) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestExaKeylessMCP(t *testing.T) {
	text := "Title: First Result\nURL: https://one.example\nPublished: 2024-01-01\nHighlights:\n- First line of body.\n- Second line.\n...\n\nTitle: Second\nURL: https://two.example\nAuthor: someone\nBody two.\n---\nTitle: no url\nBody"
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}}
	raw, _ := json.Marshal(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		params := body["params"].(map[string]any)
		if params["name"] != "web_search_exa" || params["arguments"].(map[string]any)["numResults"] != float64(5) {
			t.Errorf("params = %v", params)
		}
		// Exa answers as a single-frame event stream.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
	}))
	t.Cleanup(srv.Close)
	p := NewExa("", Options{BaseURL: srv.URL})
	got, err := p.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []SearchResult{
		{Title: "First Result", URL: "https://one.example", Snippet: "First line of body. Second line.", Content: "First line of body.\nSecond line."},
		{Title: "Second", URL: "https://two.example", Snippet: "Body two.", Content: "Body two."},
	}
	assertResults(t, got, want)
}

func TestParallelKeylessMCP(t *testing.T) {
	inner := `{"results":[{"url":"https://a.example","title":"A  title","excerpts":["one","two"]},{"url":"https://b.example","title":"B","excerpts":[]},{"url":"","title":"skip"}]}`
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": inner}}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["params"].(map[string]any)["name"] != "web_search" {
			t.Errorf("params = %v", body["params"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	p := NewParallel("", Options{BaseURL: srv.URL})
	got, err := p.Search(context.Background(), "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []SearchResult{{Title: "A title", URL: "https://a.example", Snippet: "one two", Content: "one\ntwo"}}
	assertResults(t, got, want)
}

func TestMCPErrors(t *testing.T) {
	cases := map[string]string{
		"rpc error":  `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"quota exceeded"}}`,
		"tool error": `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"rate limited"}]}}`,
		"empty":      `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`,
	}
	for name, body := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		p := NewExa("", Options{BaseURL: srv.URL})
		_, err := p.Search(context.Background(), "q", 1)
		srv.Close()
		if err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	t.Cleanup(srv.Close)
	_, err := NewParallel("", Options{BaseURL: srv.URL}).Search(context.Background(), "q", 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Errorf("429 err = %v", err)
	}
}

func TestExaKeylessRateLimitNotice(t *testing.T) {
	body := `{"result":{"_meta":{"ai.exa/rateLimited":true},"content":[{"type":"text","text":"You've hit Exa's free MCP rate limit. Create an API key."}]},"jsonrpc":"2.0","id":1}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	_, err := NewExa("", Options{BaseURL: srv.URL}).Search(context.Background(), "q", 3)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || !strings.Contains(err.Error(), "keys set exa") {
		t.Errorf("err = %v", err)
	}
}

func TestAPIErrorMessages(t *testing.T) {
	quota := (&APIError{Provider: "You.com", Status: 402, Body: "long marketing copy"}).Error()
	if strings.Contains(quota, "marketing") || !strings.Contains(quota, "keys set youcom") || !strings.Contains(quota, "402") {
		t.Errorf("402 message = %q", quota)
	}
	if got := (&APIError{Provider: "Exa", Status: 401}).Error(); !strings.Contains(got, "key rejected") {
		t.Errorf("401 message = %q", got)
	}
	if got := (&APIError{Provider: "SearXNG", Status: 500, Body: "boom"}).Error(); got != "SearXNG API returned HTTP 500: boom" {
		t.Errorf("500 message = %q", got)
	}
	sse := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"error\",\"data\":\"Free tier limit exceeded.\"}}\n\n"
	if got := summarizeBody([]byte(sse)); got != "Free tier limit exceeded." {
		t.Errorf("SSE summary = %q", got)
	}
}
