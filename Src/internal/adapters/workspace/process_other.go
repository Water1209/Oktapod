//go:build !windows

package workspace

import (
	"os/exec"
	"syscall"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		return command.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
