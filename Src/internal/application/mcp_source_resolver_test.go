package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestResolveDirectMCPSourceRequiresPinnedPackages(t *testing.T) {
	t.Parallel()
	definition, name, version, matched, err := resolveDirectMCPSource(context.Background(), "npm:@modelcontextprotocol/server-filesystem@1.2.3", nil)
	if err != nil || !matched || name != "@modelcontextprotocol/server-filesystem" || version != "1.2.3" {
		t.Fatalf("definition=%#v name=%q version=%q matched=%v err=%v", definition, name, version, matched, err)
	}
	if definition.Transport.Command != "npx" || strings.Join(definition.Transport.Args, " ") != "-y @modelcontextprotocol/server-filesystem@1.2.3" {
		t.Fatalf("transport=%#v", definition.Transport)
	}
	_, _, _, matched, err = resolveDirectMCPSource(context.Background(), "npm:@modelcontextprotocol/server-filesystem", nil)
	if !matched || err == nil || !strings.Contains(err.Error(), "版本解析器") {
		t.Fatalf("unpinned matched=%v err=%v", matched, err)
	}
}

func TestResolveDirectMCPSourceAcceptsPinnedBareNPMPackages(t *testing.T) {
	t.Parallel()
	definition, name, version, matched, err := resolveDirectMCPSource(context.Background(), "@playwright/mcp@0.0.81", nil)
	if err != nil || !matched || name != "@playwright/mcp" || version != "0.0.81" {
		t.Fatalf("definition=%#v name=%q version=%q matched=%v err=%v", definition, name, version, matched, err)
	}
	if definition.Source.Type != "npm" || definition.Transport.Command != "npx" || strings.Join(definition.Transport.Args, " ") != "-y @playwright/mcp@0.0.81" {
		t.Fatalf("definition=%#v", definition)
	}
	_, _, _, matched, err = resolveDirectMCPSource(context.Background(), "@playwright/mcp", nil)
	if !matched || err == nil || !strings.Contains(err.Error(), "版本解析器") {
		t.Fatalf("unpinned bare package matched=%v err=%v", matched, err)
	}
}

type stubMCPPackageResolver struct {
	version     string
	err         error
	ecosystem   string
	packageName string
}

func (resolver *stubMCPPackageResolver) Resolve(_ context.Context, ecosystem, packageName string) (string, error) {
	resolver.ecosystem = ecosystem
	resolver.packageName = packageName
	return resolver.version, resolver.err
}

func TestResolveDirectMCPSourceResolvesLatestPackageVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		ecosystem string
		pkg       string
		command   string
		arg       string
	}{
		{name: "explicit npm", input: "npm:@playwright/mcp", ecosystem: "npm", pkg: "@playwright/mcp", command: "npx", arg: "-y @playwright/mcp@0.0.81"},
		{name: "bare scoped npm", input: "@playwright/mcp", ecosystem: "npm", pkg: "@playwright/mcp", command: "npx", arg: "-y @playwright/mcp@0.0.81"},
		{name: "explicit pypi", input: "pypi:mcp-server-git", ecosystem: "pypi", pkg: "mcp-server-git", command: "uvx", arg: "mcp-server-git==0.0.81"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &stubMCPPackageResolver{version: "0.0.81"}
			definition, name, version, matched, err := resolveDirectMCPSource(context.Background(), test.input, resolver)
			if err != nil || !matched || name != test.pkg || version != "0.0.81" {
				t.Fatalf("definition=%#v name=%q version=%q matched=%v err=%v", definition, name, version, matched, err)
			}
			if resolver.ecosystem != test.ecosystem || resolver.packageName != test.pkg {
				t.Fatalf("resolver ecosystem=%q package=%q", resolver.ecosystem, resolver.packageName)
			}
			if definition.Transport.Command != test.command || strings.Join(definition.Transport.Args, " ") != test.arg {
				t.Fatalf("transport=%#v", definition.Transport)
			}
		})
	}
}

func TestResolveDirectMCPSourceRejectsFloatingOrFailedMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resolver MCPPackageResolver
		want     string
	}{
		{name: "metadata failure", resolver: &stubMCPPackageResolver{err: errors.New("HTTP 503")}, want: "解析 npm 包"},
		{name: "floating tag", resolver: &stubMCPPackageResolver{version: "latest"}, want: "无效版本"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, matched, err := resolveDirectMCPSource(context.Background(), "npm:@playwright/mcp", test.resolver)
			if !matched || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("matched=%v err=%v want contains %q", matched, err, test.want)
			}
		})
	}
}

func TestSplitMCPCommandLinePreservesWindowsPaths(t *testing.T) {
	t.Parallel()
	words, err := splitMCPCommandLine(`npx -y @modelcontextprotocol/server-filesystem@1.0.0 "C:\Safe Folder"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"npx", "-y", "@modelcontextprotocol/server-filesystem@1.0.0", `C:\Safe Folder`}
	if len(words) != len(want) {
		t.Fatalf("words=%#v", words)
	}
	for index := range want {
		if words[index] != want[index] {
			t.Fatalf("words[%d]=%q want=%q", index, words[index], want[index])
		}
	}
}

func TestRegistryInstallCandidatePinsVersionAndSecrets(t *testing.T) {
	t.Parallel()
	candidate, err := registryInstallCandidate(MCPRegistryServer{
		Name: "io.github.example/search", Version: "2.4.1", Description: "Search server",
		Repository: MCPRegistryRepository{URL: "https://github.com/example/search"},
		Packages: []MCPRegistryPackage{{
			RegistryType: "npm", Identifier: "example-search", Version: "2.4.1",
			EnvironmentVariables: []MCPRegistryEnvironmentVariable{{Name: "SEARCH_API_KEY", Required: true, Secret: true}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := candidate.Definition
	if definition.Source.Type != "registry" || definition.Transport.Command != "npx" || strings.Join(definition.Transport.Args, " ") != "-y example-search@2.4.1" {
		t.Fatalf("definition=%#v", definition)
	}
	if definition.Auth.Type != "environment" || len(definition.SecretBindings) != 1 || definition.SecretBindings[0].Target != "env:SEARCH_API_KEY" {
		t.Fatalf("auth=%#v bindings=%#v", definition.Auth, definition.SecretBindings)
	}
	encoded, err := definition.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ParseMCPDefinition(encoded); err != nil {
		t.Fatalf("canonical definition rejected: %v", err)
	}
}

func TestRegistryRemoteUsesOAuthDiscoveryOrDeclaredSecretHeaders(t *testing.T) {
	t.Parallel()
	oauthCandidate, err := registryInstallCandidate(MCPRegistryServer{
		Name: "com.example/oauth", Version: "1.0.0",
		Remotes: []MCPRegistryRemote{{Type: "streamable-http", URL: "https://mcp.example.com/mcp"}},
	})
	if err != nil || oauthCandidate.Definition.Auth.Type != "oauth" || len(oauthCandidate.Definition.SecretBindings) != 0 {
		t.Fatalf("OAuth remote=%#v err=%v", oauthCandidate.Definition, err)
	}
	headerCandidate, err := registryInstallCandidate(MCPRegistryServer{
		Name: "com.example/header", Version: "1.0.0",
		Remotes: []MCPRegistryRemote{{Type: "streamable-http", URL: "https://mcp.example.com/mcp", Headers: []MCPRegistryRemoteHeader{{Name: "X-API-Key", Required: true, Secret: true}}}},
	})
	if err != nil || headerCandidate.Definition.Auth.Type != "custom_header" || len(headerCandidate.Definition.SecretBindings) != 1 || headerCandidate.Definition.SecretBindings[0].Target != "header:X-API-Key" {
		t.Fatalf("header remote=%#v err=%v", headerCandidate.Definition, err)
	}
}

func TestTrimMCPInstallLanguageKeepsSourceOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{input: "请帮我安装 Tavily MCP", want: "Tavily"},
		{input: "@playwright/mcp", want: "@playwright/mcp"},
		{input: "npm:@playwright/mcp", want: "npm:@playwright/mcp"},
		{input: "npx -y @playwright/mcp@0.0.81", want: "npx -y @playwright/mcp@0.0.81"},
		{input: "帮我安装 @playwright/mcp", want: "@playwright/mcp"},
		{input: "安装 Tavily MCP", want: "Tavily"},
	}
	for _, test := range tests {
		if got := trimMCPInstallLanguage(test.input); got != test.want {
			t.Fatalf("trimMCPInstallLanguage(%q)=%q, want %q", test.input, got, test.want)
		}
	}
}
