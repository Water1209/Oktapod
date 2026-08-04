package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExternalFilesystemStdioBoundedRoot(t *testing.T) {
	if os.Getenv("OKTAPOD_TEST_LOCAL_MCP") == "" {
		t.Skip("set OKTAPOD_TEST_LOCAL_MCP=1 for local npm MCP smoke tests")
	}
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	manifestDocument := map[string]any{
		"schema_version": "oktapod.mcp/v1",
		"source":         map[string]any{"type": "npm", "reference": "@modelcontextprotocol/server-filesystem", "version": "2026.7.10"},
		"transport":      map[string]any{"type": "stdio", "command": "npx", "args": []string{"-y", "@modelcontextprotocol/server-filesystem@2026.7.10", root}},
		"auth":           map[string]any{"type": "none"}, "permissions": []string{"filesystem:" + root},
	}
	encoded, _ := json.Marshal(manifestDocument)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	health, err := Connect(ctx, string(encoded), "")
	if err != nil {
		t.Fatalf("connect official filesystem MCP: %v", err)
	}
	if len(health.Tools) < 5 {
		t.Fatalf("filesystem MCP exposed only %d tools", len(health.Tools))
	}
	created := filepath.Join(root, "oktapod-mcp-smoke.txt")
	if result, err := Call(ctx, string(encoded), "", "write_file", map[string]any{"path": created, "content": "bounded MCP write"}); err != nil || result.IsError {
		t.Fatalf("write inside allowed root: err=%v isError=%v", err, result.IsError)
	}
	if result, err := Call(ctx, string(encoded), "", "read_text_file", map[string]any{"path": created}); err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("read inside allowed root: err=%v isError=%v", err, result.IsError)
	}
	if result, err := Call(ctx, string(encoded), "", "write_file", map[string]any{"path": outside, "content": "must be denied"}); err == nil && !result.IsError {
		t.Fatalf("filesystem MCP allowed a write outside the configured root")
	}
}

// This opt-in smoke test validates the official remote Tavily MCP without
// ever placing a credential in source, fixtures, output, or the database.
func TestExternalTavilyRemoteSearch(t *testing.T) {
	credential := strings.TrimSpace(os.Getenv("OKTAPOD_TEST_TAVILY_KEY"))
	if credential == "" {
		t.Skip("set OKTAPOD_TEST_TAVILY_KEY for the external Tavily smoke test")
	}
	manifest := `{"schema_version":"oktapod.mcp/v1","source":{"type":"official","reference":"https://github.com/tavily-ai/tavily-mcp","publisher":"Tavily"},"transport":{"type":"streamable_http","url":"https://mcp.tavily.com/mcp/"},"auth":{"type":"bearer"},"secret_bindings":[{"name":"credential","target":"header:Authorization","required":true}],"permissions":["network"]}`
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	health, err := Connect(ctx, manifest, credential)
	if err != nil {
		t.Fatalf("connect official Tavily MCP: %v", err)
	}
	searchTool := ""
	for _, tool := range health.Tools {
		if strings.Contains(strings.ToLower(tool.Name), "search") {
			searchTool = tool.Name
			break
		}
	}
	if searchTool == "" {
		t.Fatalf("official Tavily MCP exposed no search tool among %d tools", len(health.Tools))
	}
	result, err := Call(ctx, manifest, credential, searchTool, map[string]any{"query": "Model Context Protocol official specification", "max_results": 2})
	if err != nil {
		t.Fatalf("call Tavily search tool %q: %v", searchTool, err)
	}
	if result.IsError || (len(result.Content) == 0 && len(result.StructuredResult) == 0) {
		t.Fatalf("Tavily search returned no usable result")
	}
}
