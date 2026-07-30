//go:build windows

package workspace

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand supplies cmd.exe's command line verbatim. os/exec's normal
// Windows argv quoting follows CommandLineToArgvW rules, which cmd.exe does not
// use and would turn embedded quotes into literal backslashes.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe")
	cmd.Args = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + command + `"`}
	return cmd
}
