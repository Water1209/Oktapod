//go:build !windows

package mcp

import (
	"context"
	"os/exec"
	"syscall"
)

func newMCPCommand(ctx context.Context, command string, args []string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, command, args...), nil
}

func configureMCPCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateMCPProcessTree(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		return command.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
