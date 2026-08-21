package webaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestFetchPrivateHTMLUsesBoundedMarkdownConversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Referer") != "" {
			t.Errorf("unsafe request: method=%s headers=%v", request.Method, request.Header)
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<html><body><h1>Wiki</h1><script>steal()</script><table><tr><th>A</th></tr><tr><td>B</td></tr></table><a href="/next">Next</a></body></html>`))
	}))
	defer server.Close()

	client, snapshot := privateTestClient(t, server.URL, "wiki")
	result, err := client.Fetch(context.Background(), core.WebFetchRequest{URL: server.URL + "/docs", PolicySnapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || result.NetworkClass != "private" || result.MatchedRuleID != "wiki" || result.ContentFormat != "markdown" {
		t.Fatalf("result=%#v", result)
	}
	if strings.Contains(result.Content, "steal") || !strings.Contains(result.Content, "Wiki") || !strings.Contains(result.Content, server.URL+"/next") || !strings.Contains(result.Content, "| A |") {
		t.Fatalf("unexpected markdown: %q", result.Content)
	}
	transport, ok := client.httpClient(snapshot, client.EvaluateFetch(snapshot, server.URL)).Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("web fetch transport must not use system or environment proxies")
	}
}

func TestFetchRejectsPrivateDNSAndCrossOriginRedirects(t *testing.T) {
	client, err := New(nil, core.WebAccessPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	client.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	_, err = client.Fetch(context.Background(), core.WebFetchRequest{URL: "http://public.example.test/", PolicySnapshot: `{}`})
	var webErr *Error
	if !errors.As(err, &webErr) || webErr.Code != "WEB_FETCH_BLOCKED_URL" {
		t.Fatalf("mixed public/private DNS was not blocked: %T %v", err, err)
	}

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cross-origin target must not be contacted")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, target.URL+"/secret", http.StatusFound)
	}))
	defer source.Close()
	privateClient, snapshot := privateTestClient(t, source.URL, "source")
	_, err = privateClient.Fetch(context.Background(), core.WebFetchRequest{URL: source.URL + "/redirect", PolicySnapshot: snapshot})
	if !errors.As(err, &webErr) || webErr.Code != "WEB_FETCH_REDIRECT_BLOCKED" || webErr.RedirectTarget != target.URL+"/secret" {
		t.Fatalf("cross-origin redirect=%T %#v", err, webErr)
	}
}

func TestFetchHandlesSameOriginRedirectStatusAndContentBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/missing", http.StatusFound)
		case "/missing":
			writer.Header().Set("Content-Type", "text/plain")
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte("not found body"))
		case "/binary":
			writer.Header().Set("Content-Type", "application/octet-stream")
			_, _ = writer.Write([]byte{0, 1, 2, 3})
		case "/large":
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = writer.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
		}
	}))
	defer server.Close()
	client, snapshot := privateTestClient(t, server.URL, "service")

	result, err := client.Fetch(context.Background(), core.WebFetchRequest{URL: server.URL + "/redirect", PolicySnapshot: snapshot})
	if err != nil || result.StatusCode != http.StatusNotFound || result.Content != "not found body" || !strings.HasSuffix(result.FinalURL, "/missing") {
		t.Fatalf("same-origin redirect/non-2xx result=%#v err=%v", result, err)
	}
	for path, wantCode := range map[string]string{"/binary": "WEB_FETCH_UNSUPPORTED_CONTENT_TYPE", "/large": "WEB_FETCH_TOO_LARGE"} {
		_, err := client.Fetch(context.Background(), core.WebFetchRequest{URL: server.URL + path, PolicySnapshot: snapshot})
		var webErr *Error
		if !errors.As(err, &webErr) || webErr.Code != wantCode {
			t.Fatalf("path=%s err=%T %#v", path, err, webErr)
		}
	}
}

func TestFetchEnforcesRedirectDepthAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/redirect/") {
			var index int
			_, _ = fmt.Sscanf(request.URL.Path, "/redirect/%d", &index)
			http.Redirect(writer, request, fmt.Sprintf("/redirect/%d", index+1), http.StatusFound)
			return
		}
		if request.URL.Path == "/deep" {
			writer.Header().Set("Content-Type", "text/html")
			_, _ = writer.Write([]byte(strings.Repeat("<div>", maxHTMLDepth+1) + "safe text" + strings.Repeat("</div>", maxHTMLDepth+1)))
			return
		}
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
		}
	}))
	defer server.Close()
	client, snapshot := privateTestClient(t, server.URL, "bounded")

	_, err := client.Fetch(context.Background(), core.WebFetchRequest{URL: server.URL + "/redirect/0", PolicySnapshot: snapshot})
	var webErr *Error
	if !errors.As(err, &webErr) || webErr.Code != "WEB_FETCH_REDIRECT_LIMIT" {
		t.Fatalf("redirect limit err=%T %#v", err, webErr)
	}
	deep, err := client.Fetch(context.Background(), core.WebFetchRequest{URL: server.URL + "/deep", PolicySnapshot: snapshot})
	if err != nil || !deep.ConversionDegraded || deep.ContentFormat != "text" || !strings.Contains(deep.Content, "safe text") {
		t.Fatalf("deep html result=%#v err=%v", deep, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Fetch(ctx, core.WebFetchRequest{URL: server.URL + "/slow", PolicySnapshot: snapshot})
	if !errors.As(err, &webErr) || (webErr.Code != "WEB_FETCH_TIMEOUT" && webErr.Code != "TOOL_CANCELED") {
		t.Fatalf("cancellation err=%T %#v", err, webErr)
	}
}

func privateTestClient(t *testing.T, rawURL, id string) (*Client, string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (core.WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []core.WebPrivateOrigin{{ID: id, Origin: parsed.Scheme + "://" + parsed.Host}}}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{}
	policy.Snapshot().AddTo(snapshot)
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return client, string(body)
}
