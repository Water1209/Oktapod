package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestPlaywrightDependencyProviderDetectsMissingChromium(t *testing.T) {
	tests := []struct {
		name      string
		manifest  string
		lastError string
		wantLen   int
		wantVer   string
		install   bool
		seed      string
	}{
		{
			name:      "known version with missing browser error",
			manifest:  `{"source":{"type":"npm","reference":"@playwright/mcp","version":"0.0.81"},"transport":{"type":"stdio","command":"npx","args":["-y","@playwright/mcp@0.0.81","--test-missing-error"]}}`,
			lastError: "Executable doesn't exist at chrome-for-testing/chrome.exe",
			wantLen:   1,
			wantVer:   "1.64.0-alpha-2026-09-14",
			install:   true,
		},
		{
			name:      "provider neutral persisted marker",
			manifest:  `{"source":{"type":"npm","reference":"@playwright/mcp","version":"0.0.81"},"transport":{"type":"stdio","command":"npx","args":["-y","@playwright/mcp@0.0.81","--test-marker-error"]}}`,
			lastError: "RUNTIME_DEPENDENCY_MISSING provider=playwright id=chromium",
			wantLen:   1,
			wantVer:   "1.64.0-alpha-2026-09-14",
			install:   true,
		},
		{
			name:      "unknown mcp version is not auto-installable",
			manifest:  `{"source":{"type":"npm","reference":"@playwright/mcp","version":"0.0.999"},"transport":{"type":"stdio","command":"npx","args":["-y","@playwright/mcp@0.0.999","--test-unknown"]}}`,
			lastError: "chrome-for-testing is missing",
			wantLen:   1,
		},
		{
			name:     "known version with empty isolated cache is missing without last error",
			manifest: `{"source":{"type":"npm","reference":"@playwright/mcp","version":"0.0.81"},"transport":{"type":"stdio","command":"npx","args":["-y","@playwright/mcp@0.0.81","--test-empty"]}}`,
			wantLen:  1,
			wantVer:  "1.64.0-alpha-2026-09-14",
			install:  true,
		},
		{
			name:     "valid installation marker is healthy",
			manifest: `{"source":{"type":"npm","reference":"@playwright/mcp","version":"0.0.81"},"transport":{"type":"stdio","command":"npx","args":["-y","@playwright/mcp@0.0.81","--test-marker"]}}`,
			wantLen:  0,
			seed:     "marker",
		},
		{
			name:     "valid executable is healthy",
			manifest: `{"source":{"type":"npm","reference":"@playwright/mcp","version":"0.0.81"},"transport":{"type":"stdio","command":"npx","args":["-y","@playwright/mcp@0.0.81","--test-executable"]}}`,
			wantLen:  0,
			seed:     "executable",
		},
		{
			name:      "non-playwright MCP is ignored",
			manifest:  `{"source":{"type":"npm","reference":"tavily-mcp","version":"1.0.0"},"transport":{"type":"stdio","command":"npx","args":["-y","tavily-mcp@1.0.0"]}}`,
			lastError: "chrome-for-testing is missing",
			wantLen:   0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, err := core.ParseMCPDefinition(test.manifest)
			if err != nil {
				t.Fatal(err)
			}
			if test.seed != "" {
				seedPlaywrightInstall(t, definition, test.seed)
			}
			items, err := (&PlaywrightDependencyProvider{}).Detect(context.Background(), application.RuntimeDependencyRequest{Definition: definition, LastError: test.lastError})
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != test.wantLen {
				t.Fatalf("dependencies=%v want length %d", items, test.wantLen)
			}
			if test.wantLen == 0 {
				return
			}
			if items[0].Version != test.wantVer || items[0].Installable != test.install {
				t.Fatalf("dependency=%+v want version %q installable %v", items[0], test.wantVer, test.install)
			}
		})
	}
}

func seedPlaywrightInstall(t *testing.T, definition core.MCPDefinitionV1, mode string) {
	t.Helper()
	spec := manifest{Transport: definition.Transport.Type, Command: definition.Transport.Command, Args: definition.Transport.Args}
	root := mcpCommandIsolationRoot(spec)
	packageRoot := filepath.Join(root, "npm-cache", "_npx", "test", "node_modules", "playwright-core")
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	browsers, err := json.Marshal(map[string]any{"browsers": []map[string]string{{"name": "chromium", "revision": "test-revision"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "browsers.json"), browsers, 0o600); err != nil {
		t.Fatal(err)
	}
	browserRoot := filepath.Join(root, "appdata", "local", "ms-playwright", "chromium-test-revision")
	if err := os.MkdirAll(browserRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if mode == "marker" {
		if err := os.WriteFile(filepath.Join(browserRoot, "INSTALLATION_COMPLETE"), []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	executable := filepath.Join(browserRoot, "chrome-win64", "chrome.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
}
