//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

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
