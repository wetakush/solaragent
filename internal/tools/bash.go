package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Bash struct{}

func (*Bash) Name() string       { return "bash" }
func (*Bash) Permission() string { return PermBash }
func (*Bash) Description() string {
	return `Execute a shell command (bash -c) in the project directory and return combined stdout/stderr.
- Use for builds, tests, git, package managers and other terminal operations.
- Prefer the dedicated read/glob/grep/ls tools over cat/find/grep for exploring files.
- Commands time out after 120s by default (max 600s). Output over 30000 characters is truncated.
- Never run interactive commands (editors, pagers, prompts); pass non-interactive flags.
- Quote paths containing spaces. Chain dependent commands with &&.`
}
func (*Bash) Schema() map[string]any {
	return obj(map[string]any{
		"command":     str("The command to execute"),
		"timeout":     num("Optional timeout in milliseconds (max 600000)"),
		"description": str("Clear 5-10 word description of what the command does"),
	}, "command")
}

func (*Bash) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		Command     string `json:"command"`
		Timeout     int    `json:"timeout"`
		Description string `json:"description"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return errResult("command is required")
	}
	return RunShell(ctx, env.Cwd, in.Command, time.Duration(in.Timeout)*time.Millisecond, in.Description)
}

// RunShell runs a command with a timeout; also used by the "!" shell mode.
func RunShell(ctx context.Context, cwd, command string, timeout time.Duration, desc string) Result {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := shellCommand(ctx, command)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "GIT_PAGER=cat", "PAGER=cat", "SOLAR_AGENT=1")
	cmd.Stdin = nil
	setProcGroup(cmd)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	err := cmd.Run()
	out := strings.TrimRight(buf.String(), "\n")
	title := desc
	if title == "" {
		title = firstLine(command)
	}
	res := Result{Title: title, Meta: map[string]any{"command": command, "elapsed": time.Since(start).Round(time.Millisecond).String()}}
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
		if ctx.Err() == context.DeadlineExceeded {
			out += fmt.Sprintf("\n\n(command timed out after %s)", timeout)
		} else if ctx.Err() != nil {
			out += "\n\n(command was interrupted)"
		} else if code == -1 {
			out += "\n" + err.Error()
		}
	}
	res.Meta["exit"] = code
	if code != 0 {
		out += fmt.Sprintf("\n\nExit code: %d", code)
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	res.Output = truncate(out, 30000)
	return res
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return s
}
