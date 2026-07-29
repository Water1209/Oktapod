package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestSearchFallsBackToRSSProvider(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "rss" {
			t.Fatalf("format=%q", r.URL.Query().Get("format"))
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel><item><title>Go documentation</title><link>https://go.dev/doc/</link><description>Official Go docs.</description></item></channel></rss>`))
	}))
	defer fallback.Close()

	client, err := New(primary.URL)
	if err != nil {
		t.Fatal(err)
	}
	fallbackURL, err := url.Parse(fallback.URL)
	if err != nil {
		t.Fatal(err)
	}
	fallbackURL.Host = "www.bing.com"
	client.endpoints = append(client.endpoints, fallbackURL)
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "www.bing.com" {
			clone := request.Clone(request.Context())
			target, _ := url.Parse(fallback.URL)
			clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(clone)
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	response, err := client.Search(t.Context(), Input{Query: "latest Go docs", MaxResults: 3, AllowedDomains: []string{"go.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Provider != "bing_rss" || len(response.Results) != 1 || response.Results[0].URL != "https://go.dev/doc/" {
		t.Fatalf("response=%#v", response)
	}
}

func TestConcurrentSearchesAreSerializedForPublicProvider(t *testing.T) {
	var active atomic.Int64
	var peak atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		if current > 1 {
			_, _ = w.Write([]byte(`<html><body>rate limited</body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<div class="result"><a class="result__a" href="https://go.dev/doc/">Go documentation</a></div>`))
	}))
	defer provider.Close()
	client, err := New(provider.URL)
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errors := make(chan error, 4)
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, searchErr := client.Search(t.Context(), Input{Query: "Go documentation", MaxResults: 1})
			errors <- searchErr
		}()
	}
	wait.Wait()
	close(errors)
	for searchErr := range errors {
		if searchErr != nil {
			t.Fatal(searchErr)
		}
	}
	if peak.Load() != 1 {
		t.Fatalf("provider peak concurrency=%d, want 1", peak.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestLiveDuckDuckGoSearch(t *testing.T) {
	if os.Getenv("OKTAPOD_LIVE_WEB_SEARCH") != "1" {
		t.Skip("set OKTAPOD_LIVE_WEB_SEARCH=1 to exercise the real search provider")
	}
	client, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	cases := []Input{
		{Query: "Go programming language official documentation", MaxResults: 3, AllowedDomains: []string{"go.dev"}},
		{Query: "model API platform token pricing official", MaxResults: 5},
	}
	for _, input := range cases {
		response, err := client.Search(t.Context(), input)
		if err != nil {
			t.Fatalf("query %q: %v", input.Query, err)
		}
		if len(response.Results) == 0 || response.Results[0].URL == "" {
			t.Fatalf("query %q live response=%#v", input.Query, response)
		}
	}
}
