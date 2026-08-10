package application

import (
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestResolveDirectMCPSourceRequiresPinnedPackages(t *testing.T) {
	t.Parallel()
	definition, name, version, matched, err := resolveDirectMCPSource("npm:@modelcontextprotocol/server-filesystem@1.2.3")
	if err != nil || !matched || name != "@modelcontextprotocol/server-filesystem" || version != "1.2.3" {
		t.Fatalf("definition=%#v name=%q version=%q matched=%v err=%v", definition, name, version, matched, err)
	}
	if definition.Transport.Command != "npx" || strings.Join(definition.Transport.Args, " ") != "-y @modelcontextprotocol/server-filesystem@1.2.3" {
		t.Fatalf("transport=%#v", definition.Transport)
	}
	_, _, _, matched, err = resolveDirectMCPSource("npm:@modelcontextprotocol/server-filesystem")
	if !matched || err == nil || !strings.Contains(err.Error(), "固定版本") {
		t.Fatalf("unpinned matched=%v err=%v", matched, err)
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
	if got := trimMCPInstallLanguage("请帮我安装 Tavily MCP"); got != "Tavily" {
		t.Fatalf("got=%q", got)
	}
}
