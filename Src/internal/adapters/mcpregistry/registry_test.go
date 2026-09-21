package mcpregistry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewUsesConservativeRegistryTimeout(t *testing.T) {
	client := New(nil)
	if client.http.Timeout != 30*time.Second {
		t.Fatalf("timeout=%s, want 30s", client.http.Timeout)
	}
}

func TestSearchUsesOfficialContractWrapper(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "filesystem" || r.URL.Query().Get("limit") != "4" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"servers":[{"server":{"name":"io.github.example/filesystem","title":"Filesystem","description":"Safe files","version":"1.2.3","repository":{"url":"https://github.com/example/filesystem","source":"github"},"packages":[{"registryType":"npm","identifier":"example-filesystem","version":"1.2.3","transport":{"type":"stdio"}}]}}]}`)
	}))
	defer server.Close()
	client := NewWithEndpoint(server.URL, server.Client())
	servers, err := client.Search(t.Context(), "filesystem", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "io.github.example/filesystem" || len(servers[0].Packages) != 1 {
		t.Fatalf("servers=%#v", servers)
	}
}
