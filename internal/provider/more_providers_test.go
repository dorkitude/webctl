package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// serve returns a server answering every request with body and recording it.
func serve(t *testing.T, status int, body string) (*httptest.Server, *capture) {
	t.Helper()
	return mockServer(t, status, body)
}

func TestBraveTavilyFirecrawlKeenableSerpBaseSerplyDegoog(t *testing.T) {
	ctx := context.Background()

	srv, c := serve(t, 200, `{"web":{"results":[{"title":"A","url":"https://a","description":"Brave description sentence that is long enough to be prose for the snippet."},{"title":"","url":"https://skip"}]}}`)
	got, err := NewBrave("bk", Options{BaseURL: srv.URL}).Search(ctx, "q", 30)
	if err != nil || len(got) != 1 || got[0].URL != "https://a" || c.Headers.Get("X-Subscription-Token") != "bk" || !strings.Contains(c.Path+"?"+c.Query, "count=20") {
		t.Errorf("brave: %+v %v path %s?%s", got, err, c.Path, c.Query)
	}

	srv, c = serve(t, 200, `{"results":[{"title":"T","url":"https://t","content":"Extracted page text long enough to read as a sentence of prose here."}]}`)
	got, err = NewTavily("tk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5)
	if err != nil || len(got) != 1 || got[0].Content == "" || c.Headers.Get("Authorization") != "Bearer tk" || c.Body["search_depth"] != "basic" || c.Body["max_results"] != float64(5) {
		t.Errorf("tavily: %+v %v %v", got, err, c.Body)
	}

	srv, c = serve(t, 200, `{"results":[{"name":"L","url":"https://l","content":"Extracted page text long enough to read as a sentence of prose here.","type":"text"},{"name":"pic","url":"https://img","type":"image"},{"name":"","url":"https://skip"}]}`)
	got, err = NewLinkup("lk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5)
	if err != nil || len(got) != 1 || got[0].URL != "https://l" || got[0].Content == "" || c.Headers.Get("Authorization") != "Bearer lk" || c.Path != "/v1/search" || c.Body["q"] != "q" || c.Body["depth"] != "fast" || c.Body["outputType"] != "searchResults" || c.Body["maxResults"] != float64(5) {
		t.Errorf("linkup: %+v %v %v %s", got, err, c.Body, c.Path)
	}
	if _, err := New("linkup", "", Options{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("linkup without a key = %v", err)
	}

	srv, c = serve(t, 200, `{"success":true,"data":{"web":[{"title":"F","url":"https://f","description":"desc"}]}}`)
	got, err = NewFirecrawl("", Options{BaseURL: srv.URL}).Search(ctx, "q", 5)
	if err != nil || len(got) != 1 || c.Headers.Get("Authorization") != "" || c.Path != "/v2/search" {
		t.Errorf("firecrawl keyless: %+v %v %q %s", got, err, c.Headers.Get("Authorization"), c.Path)
	}
	if _, err := NewFirecrawl("fk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5); err != nil || c.Headers.Get("Authorization") != "Bearer fk" {
		t.Errorf("firecrawl keyed header = %q", c.Headers.Get("Authorization"))
	}

	srv, c = serve(t, 200, `{"results":[{"title":"K","url":"https://k","snippet":"Page text for keenable that is long enough to count as prose in the snippet."},{"title":"K2","url":"https://k2","description":"d"}]}`)
	got, err = NewKeenable("", Options{BaseURL: srv.URL}).Search(ctx, "q", 1)
	if err != nil || len(got) != 1 || c.Path != "/v1/search/public" || c.Headers.Get("X-Keenable-Title") == "" || c.Body["mode"] != "pro" {
		t.Errorf("keenable keyless: %+v %v %s", got, err, c.Path)
	}
	if _, err := NewKeenable("kk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5); err != nil || c.Path != "/v1/search" || c.Headers.Get("X-API-Key") != "kk" {
		t.Errorf("keenable keyed: %v %s", err, c.Path)
	}

	srv, c = serve(t, 200, `{"status":0,"organic":[{"title":"S","link":"https://s","snippet":"snip"}]}`)
	got, err = NewSerpBase("sk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5)
	if err != nil || len(got) != 1 || got[0].URL != "https://s" || c.Headers.Get("X-API-Key") != "sk" || c.Body["q"] != "q" {
		t.Errorf("serpbase: %+v %v", got, err)
	}
	for status, want := range map[int]int{1001: 401, 1020: 402, 1029: 429} {
		srv, _ = serve(t, 200, `{"status":`+strconv.Itoa(status)+`,"error":"x"}`)
		_, err = NewSerpBase("sk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != want {
			t.Errorf("serpbase status %d → %v, want HTTP %d", status, err, want)
		}
	}

	srv, c = serve(t, 200, `{"results":[{"title":"P","link":"https://p","description":"d"}]}`)
	got, err = NewSerply("pk", Options{BaseURL: srv.URL}).Search(ctx, "q", 25)
	if err != nil || len(got) != 1 || c.Headers.Get("X-Api-Key") != "pk" || !strings.Contains(c.Query, "num=10") {
		t.Errorf("serply: %+v %v %s", got, err, c.Query)
	}

	srv, c = serve(t, 200, `{"results":[{"title":"D","url":"https://d","snippet":"s"},{"title":"D2","url":"https://d2","snippet":"s"}]}`)
	got, err = NewDegoog(srv.URL, Options{}).Search(ctx, "q", 1)
	if err != nil || len(got) != 1 || c.Path != "/api/search" || !strings.Contains(c.Query, "q=q") {
		t.Errorf("degoog: %+v %v %s?%s", got, err, c.Path, c.Query)
	}
	if _, err := New("degoog", "", Options{}); err == nil {
		t.Error("degoog without a URL must fail to construct")
	}

	// A 429 from any of them is an APIError the chain's cooldown understands.
	srv, _ = serve(t, http.StatusTooManyRequests, `{"message":"slow down"}`)
	_, err = NewBrave("bk", Options{BaseURL: srv.URL}).Search(ctx, "q", 5)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || !strings.Contains(err.Error(), "keys set brave") {
		t.Errorf("brave 429 = %v", err)
	}
}
