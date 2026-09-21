package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

var missingBrowserErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)chrom[e\)]*(?:\s+browser)?\s+(?:was\s+)?not\s+found`),
	regexp.MustCompile(`(?i)executable\s+doesn'?t\s+exist`),
	regexp.MustCompile(`(?i)chrome-for-testing`),
}

const runtimeDependencyMarkerPrefix = "RUNTIME_DEPENDENCY_MISSING"

// PlaywrightDependencyProvider owns the runtime dependency of the Playwright
// MCP package. The generic dependency contract stays package-independent.
type PlaywrightDependencyProvider struct{}

func NewPlaywrightDependencyProvider() *PlaywrightDependencyProvider {
	return &PlaywrightDependencyProvider{}
}

func (p *PlaywrightDependencyProvider) Detect(_ context.Context, request application.RuntimeDependencyRequest) ([]application.RuntimeDependency, error) {
	if !isPlaywrightMCP(request) {
		return []application.RuntimeDependency{}, nil
	}
	version, ok := playwrightPackageVersion(request.Definition)
	if !ok {
		return []application.RuntimeDependency{{
			ID: "chromium", Kind: "browser", Provider: "playwright", Name: "chromium",
			DisplayName: "Chromium 浏览器运行时", Description: "检测到 Playwright MCP 需要 Chromium，但当前版本映射未知；请先重新验证 MCP，或使用已知兼容版本。",
		}}, nil
	}
	// Detection is independent from the last MCP error. A newly installed MCP
	// has no health error yet, but its isolated browser cache can still be empty.
	if playwrightChromiumInstalled(request.Definition) {
		return []application.RuntimeDependency{}, nil
	}
	return []application.RuntimeDependency{{
		ID: "chromium", Kind: "browser", Provider: "playwright", Name: "chromium", Version: version,
		DisplayName: "Chromium 浏览器运行时",
		Description: fmt.Sprintf("Playwright MCP 需要 Chromium（对应 playwright %s）。确认后将下载到 Oktapod 的隔离缓存，不修改系统浏览器。", version),
		Installable: true,
	}}, nil
}

type playwrightBrowsersManifest struct {
	Browsers []struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
	} `json:"browsers"`
}

// playwrightChromiumInstalled only inspects the cache rooted at the same
// isolation directory used to launch the MCP process. The user's global
// ms-playwright cache is intentionally never consulted.
func playwrightChromiumInstalled(definition core.MCPDefinitionV1) bool {
	spec := manifest{Transport: definition.Transport.Type, Command: definition.Transport.Command, Args: append([]string(nil), definition.Transport.Args...)}
	root := mcpCommandIsolationRoot(spec)
	for _, manifestPath := range findPlaywrightBrowsersManifests(filepath.Join(root, "npm-cache")) {
		manifestData, ok := readPlaywrightBrowsersManifest(manifestPath)
		if !ok {
			continue
		}
		revision := chromiumRevision(manifestData)
		if revision == "" {
			continue
		}
		for _, cacheRoot := range playwrightBrowserCacheRoots(root) {
			for _, browserRoot := range []string{
				filepath.Join(cacheRoot, "chromium-"+revision),
				filepath.Join(cacheRoot, "chromium_headless_shell-"+revision),
			} {
				if chromiumInstallPresent(browserRoot) {
					// Keep the manifest path in the same isolation root as an
					// explicit guard against accepting host-global state.
					return filepath.IsAbs(manifestPath) && strings.HasPrefix(filepath.Clean(manifestPath), filepath.Clean(root)+string(os.PathSeparator))
				}
			}
		}
	}
	return false
}

func findPlaywrightBrowsersManifests(cacheRoot string) []string {
	patterns := []string{
		filepath.Join(cacheRoot, "_npx", "*", "node_modules", "playwright-core", "browsers.json"),
		filepath.Join(cacheRoot, "_npx", "*", "node_modules", "@playwright", "mcp", "node_modules", "playwright-core", "browsers.json"),
		filepath.Join(cacheRoot, "node_modules", "playwright-core", "browsers.json"),
	}
	var paths []string
	seen := map[string]struct{}{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range matches {
			clean := filepath.Clean(path)
			if _, ok := seen[clean]; ok {
				continue
			}
			seen[clean] = struct{}{}
			paths = append(paths, clean)
		}
	}
	return paths
}

func readPlaywrightBrowsersManifest(path string) (playwrightBrowsersManifest, bool) {
	var result playwrightBrowsersManifest
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &result) != nil || chromiumRevision(result) == "" {
		return playwrightBrowsersManifest{}, false
	}
	return result, true
}

func chromiumRevision(manifestData playwrightBrowsersManifest) string {
	for _, browser := range manifestData.Browsers {
		if strings.EqualFold(browser.Name, "chromium") {
			return strings.TrimSpace(browser.Revision)
		}
	}
	return ""
}

func playwrightBrowserCacheRoots(root string) []string {
	// Playwright uses LOCALAPPDATA on Windows, XDG_CACHE_HOME on Linux, and
	// ~/Library/Caches on macOS. Include the generic cache root as a portable
	// fallback used by tests and deployments with an explicit cache dir.
	return []string{
		filepath.Join(root, "appdata", "local", "ms-playwright"),
		filepath.Join(root, "cache", "ms-playwright"),
		filepath.Join(root, "home", "Library", "Caches", "ms-playwright"),
	}
}

func chromiumInstallPresent(browserRoot string) bool {
	marker := filepath.Join(browserRoot, "INSTALLATION_COMPLETE")
	if info, err := os.Stat(marker); err == nil && !info.IsDir() {
		return true
	}
	candidates := []string{
		filepath.Join(browserRoot, "chrome-win", "chrome.exe"),
		filepath.Join(browserRoot, "chrome-win64", "chrome.exe"),
		filepath.Join(browserRoot, "chrome-linux", "chrome"),
		filepath.Join(browserRoot, "chrome-linux", "chrome-headless-shell"),
		filepath.Join(browserRoot, "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func (p *PlaywrightDependencyProvider) Supports(dependency application.RuntimeDependency) bool {
	return dependency.Provider == "playwright" && dependency.ID == "chromium"
}

func (p *PlaywrightDependencyProvider) Install(ctx context.Context, request application.RuntimeDependencyInstallRequest) error {
	dependency := request.Dependency
	if !p.Supports(dependency) || dependency.Version == "" {
		return fmt.Errorf("unsupported Playwright dependency %q", dependency.ID)
	}
	installCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	spec := manifest{
		Transport: "stdio",
		Command:   request.Definition.Transport.Command,
		Args:      append([]string(nil), request.Definition.Transport.Args...),
	}
	if strings.TrimSpace(spec.Command) == "" {
		spec.Command = "npx"
		spec.Args = []string{"-y", "@playwright/mcp@" + strings.TrimSpace(request.Definition.Source.Version)}
	}
	installer := manifest{Transport: "stdio", Command: "npx", Args: []string{"-y", "playwright@" + dependency.Version, "install", "chromium"}}
	installCommand, err := newMCPCommand(installCtx, installer.Command, installer.Args)
	if err != nil {
		return err
	}
	installCommand.Env = mcpCommandEnvironment(spec, "")
	installCommand.Stdout = &dependencyInstallOutput{}
	installCommand.Stderr = installCommand.Stdout
	configureMCPCommand(installCommand)
	if err := installCommand.Run(); err != nil {
		return fmt.Errorf("download Chromium (%s): %w", dependency.Version, err)
	}
	return nil
}

func hasRuntimeDependencyFailure(lastError string) bool {
	if hasRuntimeDependencyMarker(lastError, "playwright", "chromium") {
		return true
	}
	for _, pattern := range missingBrowserErrorPatterns {
		if pattern.MatchString(lastError) {
			return true
		}
	}
	return false
}

// @playwright/mcp and playwright use independent release streams. The MCP
// package pins an exact browser-driver version, so unknown MCP versions must
// not fall back to an unrelated playwright release.
var playwrightDriverVersions = map[string]string{
	"0.0.81": "1.64.0-alpha-2026-09-14",
}

func playwrightPackageVersion(definition core.MCPDefinitionV1) (string, bool) {
	mcpVersion := strings.TrimSpace(definition.Source.Version)
	for _, arg := range definition.Transport.Args {
		value := strings.ToLower(strings.TrimSpace(arg))
		if strings.HasPrefix(value, "@playwright/mcp@") {
			if version := strings.TrimSpace(arg[len("@playwright/mcp@"):]); version != "" {
				mcpVersion = version
				break
			}
		}
	}
	if mcpVersion == "" {
		return "", false
	}
	version, ok := playwrightDriverVersions[mcpVersion]
	return version, ok
}

func isPlaywrightMCP(request application.RuntimeDependencyRequest) bool {
	reference := strings.ToLower(strings.TrimSpace(request.Definition.Source.Reference))
	if strings.Contains(reference, "@playwright/mcp") {
		return true
	}
	for _, arg := range request.Definition.Transport.Args {
		if strings.Contains(strings.ToLower(arg), "@playwright/mcp") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(request.Capability.Name), "playwright")
}

func hasRuntimeDependencyMarker(lastError, provider, dependencyID string) bool {
	for _, line := range strings.Split(lastError, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || fields[0] != runtimeDependencyMarkerPrefix {
			continue
		}
		values := map[string]string{}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if ok {
				values[key] = value
			}
		}
		if values["provider"] == provider && values["id"] == dependencyID {
			return true
		}
	}
	return false
}

type dependencyInstallOutput struct {
	buffer bytes.Buffer
}

func (output *dependencyInstallOutput) Write(data []byte) (int, error) {
	// Keep output bounded; install failures are summarized by the command's
	// error and safe MCP messages, while progress logs never enter audits.
	if output.buffer.Len() < 16*1024 {
		output.buffer.Write(data)
	}
	return len(data), nil
}
