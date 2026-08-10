//go:build windows

package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// newMCPCommand resolves npm's Windows shim to node.exe directly. Go cannot
// reliably launch the npx.cmd/npx.ps1 wrappers as a child process, and using a
// shell here would turn otherwise structured arguments back into command text.
func newMCPCommand(ctx context.Context, command string, args []string) (*exec.Cmd, error) {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	if base != "npx" && base != "npx.cmd" && base != "npx.ps1" {
		return exec.CommandContext(ctx, command, args...), nil
	}
	nodePath, err := exec.LookPath("node.exe")
	if err != nil {
		return nil, fmt.Errorf("resolve npx launcher: node.exe is unavailable: %w", err)
	}
	npxCLI := filepath.Join(filepath.Dir(nodePath), "node_modules", "npm", "bin", "npx-cli.js")
	if info, statErr := os.Stat(npxCLI); statErr != nil || info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("path is a directory")
		}
		return nil, fmt.Errorf("resolve npx launcher %q: %w", npxCLI, statErr)
	}
	return exec.CommandContext(ctx, nodePath, append([]string{npxCLI}, args...)...), nil
}

func configureMCPCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func terminateMCPProcessTree(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F").Run()
}
