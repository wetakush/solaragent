//go:build !windows

package tools

import (
	"context"
	"os/exec"
	"syscall"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "sh"
	}
	return exec.CommandContext(ctx, shell, "-c", command)
}

// setProcGroup makes cancellation kill the whole process tree.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
