package mcppackage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolverReadsOfficialMetadataShapes(t *testing.T) {
	t.Parallel()
	requested := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested = append(requested, request.URL.String())
		writer.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/json") {
			_, _ = writer.Write([]byte(`{"info":{"version":"2.4.1"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"dist-tags":{"latest":"0.0.81"}}`))
	}))
	defer server.Close()

	resolver := New(server.Client())
	resolver.npmEndpoint = server.URL
	resolver.pypiEndpoint = server.URL

	npmVersion, err := resolver.Resolve(context.Background(), "npm", "@playwright/mcp")
	if err != nil || npmVersion != "0.0.81" {
		t.Fatalf("npm version=%q err=%v", npmVersion, err)
	}
	pypiVersion, err := resolver.Resolve(context.Background(), "pypi", "mcp-server-git")
	if err != nil || pypiVersion != "2.4.1" {
		t.Fatalf("pypi version=%q err=%v", pypiVersion, err)
	}
}

func TestResolverRejectsUnknownEcosystem(t *testing.T) {
	t.Parallel()
	if _, err := New(nil).Resolve(context.Background(), "cargo", "example"); err == nil {
		t.Fatal("expected unsupported ecosystem error")
	}
}
