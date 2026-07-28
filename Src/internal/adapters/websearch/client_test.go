package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSearchParsesBoundsAndFiltersResults(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "site:go.dev") || r.URL.Query().Get("df") != "w" {
			t.Fatalf("query=%q freshness=%q", r.URL.Query().Get("q"), r.URL.Query().Get("df"))
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
			<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F">Go documentation</a><a class="result__snippet">Official <b>Go</b> docs.</a></div>
			<div class="result"><a class="result__a" href="https://example.com/ignored">Ignored</a><a class="result__snippet">Wrong domain.</a></div>
		</body></html>`))
	}))
	defer provider.Close()

	client, err := New(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(context.Background(), Input{Query: "latest Go docs", MaxResults: 5, Freshness: "week", AllowedDomains: []string{"go.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].URL != "https://go.dev/doc/" || response.Results[0].Snippet != "Official Go docs." {
		t.Fatalf("response=%#v", response)
	}
}

func TestSearchRejectsExternalPlainHTTPProvider(t *testing.T) {
	if _, err := New("http://example.com/search"); err == nil {
		t.Fatal("plain HTTP external provider was accepted")
	}
}

func TestLiveDuckDuckGoSearch(t *testing.T) {
	if os.Getenv("OKTAPOD_LIVE_WEB_SEARCH") != "1" {
		t.Skip("set OKTAPOD_LIVE_WEB_SEARCH=1 to exercise the real search provider")
	}
	client, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), Input{Query: "Go programming language official documentation", MaxResults: 3, AllowedDomains: []string{"go.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].URL == "" {
		t.Fatalf("live response=%#v", response)
	}
}
