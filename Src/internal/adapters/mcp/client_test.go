package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectStreamableHTTPDiscoversTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]string{{"name": "search", "description": "Search documents"}}}})
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
	}))
	defer server.Close()
	health, err := Connect(context.Background(), `{"transport":"streamable_http","url":"`+server.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if health.Protocol != "2025-11-25" || len(health.Tools) != 1 || health.Tools[0].Name != "search" {
		t.Fatalf("health=%#v", health)
	}
}

func TestConnectRejectsPrivateNonLoopbackHTTP(t *testing.T) {
	if _, err := Connect(context.Background(), `{"transport":"streamable_http","url":"http://10.0.0.1/mcp"}`); err == nil {
		t.Fatal("expected SSRF policy rejection")
	}
}

func TestCallStreamableHTTPReturnsStructuredContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25"}})
		case "tools/call":
			params := request.Params.(map[string]any)
			if params["name"] != "search" {
				t.Fatalf("params=%#v", params)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}, "structuredContent": map[string]any{"count": 1}}})
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
	}))
	defer server.Close()
	result, err := Call(context.Background(), `{"transport":"streamable_http","url":"`+server.URL+`"}`, "search", map[string]any{"query": "ok"})
	if err != nil || result.IsError || len(result.Content) != 1 || len(result.StructuredResult) == 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCallStreamableHTTPRejectsRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("redirect target must not receive MCP traffic")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	_, err := Call(context.Background(), `{"transport":"streamable_http","url":"`+redirector.URL+`"}`, "search", map[string]any{"query": "ok"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}

func TestMCPCommandEnvironmentDoesNotInheritUnlistedSecrets(t *testing.T) {
	t.Setenv("OKTAPOD_MODEL_API_KEY", "must-not-leak")
	t.Setenv("MCP_TEST_TOKEN", "allowed-token")
	env := mcpCommandEnvironment(manifest{AuthEnv: "MCP_TEST_TOKEN"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "must-not-leak") {
		t.Fatalf("model key leaked into MCP environment: %q", joined)
	}
	if !strings.Contains(joined, "MCP_TEST_TOKEN=allowed-token") {
		t.Fatalf("declared auth env missing: %q", joined)
	}
}
