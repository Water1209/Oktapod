package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestExternalPlaywrightStdioNavigatesLocalProduct(t *testing.T) {
	if os.Getenv("OKTAPOD_TEST_LOCAL_MCP") == "" {
		t.Skip("set OKTAPOD_TEST_LOCAL_MCP=1 for local npm MCP smoke tests")
	}
	browserExecutable := strings.TrimSpace(os.Getenv("OKTAPOD_TEST_PLAYWRIGHT_EXECUTABLE"))
	if browserExecutable == "" && runtime.GOOS == "windows" {
		for _, candidate := range []string{
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				browserExecutable = candidate
				break
			}
		}
	}
	if browserExecutable == "" {
		t.Skip("set OKTAPOD_TEST_PLAYWRIGHT_EXECUTABLE to an installed Chromium-family browser")
	}
	manifestDocument := map[string]any{
		"schema_version": "oktapod.mcp/v1",
		"source":         map[string]any{"type": "npm", "reference": "@playwright/mcp", "version": "0.0.78", "publisher": "Microsoft"},
		"transport": map[string]any{"type": "stdio", "command": "npx", "args": []string{
			"-y", "@playwright/mcp@0.0.78", "--headless", "--browser", "chromium", "--executable-path", browserExecutable, "--isolated", "--image-responses", "omit",
		}},
		"auth": map[string]any{"type": "none"}, "permissions": []string{"browser", "network:loopback"},
	}
	encoded, _ := json.Marshal(manifestDocument)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	health, err := Connect(ctx, string(encoded), "")
	if err != nil {
		t.Fatalf("connect official Playwright MCP: %v", err)
	}
	if !hasTool(health.Tools, "browser_navigate") || len(health.Tools) < 10 {
		t.Fatalf("Playwright MCP exposed an unexpected tool catalog (%d tools)", len(health.Tools))
	}
	result, err := Call(ctx, string(encoded), "", "browser_navigate", map[string]any{"url": "http://127.0.0.1:8787/"})
	if err != nil || result.IsError {
		t.Fatalf("navigate Oktapod with Playwright MCP: err=%v isError=%v detail=%s", err, result.IsError, externalResultText(result))
	}
	text := externalResultText(result)
	if !strings.Contains(text, "Oktapod") {
		t.Fatalf("Playwright MCP navigation returned no Oktapod accessibility content")
	}
}

func TestExternalGitStdioReadsTemporaryRepository(t *testing.T) {
	if os.Getenv("OKTAPOD_TEST_LOCAL_MCP") == "" {
		t.Skip("set OKTAPOD_TEST_LOCAL_MCP=1 for local package MCP smoke tests")
	}
	repository := t.TempDir()
	runExternalCommand(t, "git", "init", repository)
	runExternalCommand(t, "git", "-C", repository, "config", "user.name", "Oktapod MCP Test")
	runExternalCommand(t, "git", "-C", repository, "config", "user.email", "mcp-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# MCP Git smoke\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runExternalCommand(t, "git", "-C", repository, "add", "README.md")
	runExternalCommand(t, "git", "-C", repository, "commit", "-m", "Initial MCP smoke commit")
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("visible to git_status\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestDocument := map[string]any{
		"schema_version": "oktapod.mcp/v1",
		"source":         map[string]any{"type": "pypi", "reference": "mcp-server-git", "version": "2026.7.10", "publisher": "Model Context Protocol"},
		// The released package currently declares mcp>=1 without a major-version
		// ceiling even though its low-level Server API is a v1 contract. Keep the
		// complete executable dependency set fixed in this third-party smoke test.
		"transport": map[string]any{"type": "stdio", "command": "uvx", "args": []string{
			"--with", "mcp<2", "mcp-server-git==2026.7.10", "--repository", repository,
		}},
		"auth": map[string]any{"type": "none"}, "permissions": []string{"filesystem:" + repository, "git:read"},
	}
	encoded, _ := json.Marshal(manifestDocument)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	health, err := Connect(ctx, string(encoded), "")
	if err != nil {
		t.Fatalf("connect official Git MCP: %v", err)
	}
	if !hasTool(health.Tools, "git_status") || !hasTool(health.Tools, "git_log") {
		t.Fatalf("Git MCP did not expose required read-only tools among %d tools", len(health.Tools))
	}
	status, err := Call(ctx, string(encoded), "", "git_status", map[string]any{"repo_path": repository})
	if err != nil || status.IsError || !strings.Contains(externalResultText(status), "dirty.txt") {
		t.Fatalf("Git MCP status did not report the temporary untracked file: err=%v isError=%v", err, status.IsError)
	}
	logResult, err := Call(ctx, string(encoded), "", "git_log", map[string]any{"repo_path": repository, "max_count": 1})
	if err != nil || logResult.IsError || !strings.Contains(externalResultText(logResult), "Initial MCP smoke commit") {
		t.Fatalf("Git MCP log did not return the temporary commit: err=%v isError=%v", err, logResult.IsError)
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

func hasTool(tools []Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func externalResultText(result CallResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		text.Write(content)
		text.WriteByte('\n')
	}
	text.Write(result.StructuredResult)
	return text.String()
}

func runExternalCommand(t *testing.T, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v (%s)", name, err, strings.TrimSpace(string(output)))
	}
}
