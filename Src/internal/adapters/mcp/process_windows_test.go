//go:build windows

package mcp

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMCPCommandResolvesNPXWithoutShell(t *testing.T) {
	if _, err := exec.LookPath("node.exe"); err != nil {
		t.Skip("node.exe is unavailable")
	}
	command, err := newMCPCommand(context.Background(), "npx", []string{"-y", "fixture@1.0.0"})
	if err != nil {
		t.Fatalf("resolve npx: %v", err)
	}
	if strings.EqualFold(filepath.Ext(command.Path), ".cmd") || strings.EqualFold(filepath.Ext(command.Path), ".ps1") {
		t.Fatalf("npx must not use a shell shim: %s", command.Path)
	}
	if len(command.Args) < 2 || !strings.EqualFold(filepath.Base(command.Args[1]), "npx-cli.js") {
		t.Fatalf("expected node plus npx-cli.js, got %#v", command.Args)
	}
}
