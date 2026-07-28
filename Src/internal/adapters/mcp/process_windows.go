//go:build windows

package mcp

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

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
