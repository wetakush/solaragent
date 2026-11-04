//go:build windows

package tools

import (
	"context"
	"os/exec"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if _, err := exec.LookPath("bash"); err == nil {
		return exec.CommandContext(ctx, "bash", "-c", command)
	}
	return exec.CommandContext(ctx, "cmd", "/C", command)
}

func setProcGroup(cmd *exec.Cmd) {}
