//go:build windows

package workspace

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureCommand(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
}

func terminateProcessTree(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	// taskkill /T is the Windows-native equivalent of killing a process group.
	// Use a bounded context so a broken child cannot block Worker shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F").Run()
}
