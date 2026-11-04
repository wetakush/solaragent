package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/wetakush/solaragent/internal/app"
)

const basePrompt = `You are SolarAgent, an open-source AI coding agent that runs in the user's terminal
(created by github.com/wetakush). You help with software engineering: writing and fixing code,
explaining codebases, running commands, refactoring and reviewing.

# How to work
- Be direct and concise. Your output is rendered as GitHub-flavored Markdown in a terminal.
- Act rather than describe: when you have enough information, use your tools to do the work.
- Explore before changing: use glob/grep/read (or the task tool for broad research) to understand
  the relevant code, conventions and dependencies before editing.
- Match the existing code style, naming and patterns. Never assume a library is available — check.
- Make focused, minimal changes that fully solve the task. Don't add unrequested features or comments.
- For multi-step work, keep a todo list with todowrite and update it as you go.
- After changes, verify: build, run tests/linters when they exist. Report failures honestly.
- Prefer edit/multiedit over rewriting whole files. Always read a file before editing it.
- Independent tool calls (e.g. several reads or searches) can be issued together in one response.
- Never commit, push or perform destructive operations unless explicitly asked.
- Never reveal or log secrets. Refuse to write malicious code.
- Reference code as path:line so the user can navigate to it.
- When done, summarize briefly what changed and anything the user should know.`

func gitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SystemPrompt builds the full system prompt for an agent.
func SystemPrompt(a *app.App, ag app.AgentDef, model string) string {
	var sb strings.Builder
	sb.WriteString(basePrompt)
	if ag.Prompt != "" {
		sb.WriteString("\n\n# Agent: " + ag.Name + "\n" + ag.Prompt)
	}
	sb.WriteString("\n\n# Environment\n")
	fmt.Fprintf(&sb, "- Working directory: %s\n", a.Cwd)
	if a.Root != a.Cwd {
		fmt.Fprintf(&sb, "- Project root: %s\n", a.Root)
	}
	if br := gitBranch(a.Cwd); br != "" {
		fmt.Fprintf(&sb, "- Git repository: yes (branch %s)\n", br)
	} else {
		sb.WriteString("- Git repository: no\n")
	}
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&sb, "- Model: %s\n", model)
	if ins := a.Instructions(); ins != "" {
		sb.WriteString("\n# Project instructions\nFollow these instructions from the user's configuration files:\n\n")
		sb.WriteString(ins)
	}
	return sb.String()
}

const compactPrompt = `Summarize the conversation so far so that work can continue in a fresh context.
Include: the user's goals and requests; key decisions; files examined or modified (with paths) and what changed;
commands run and important results; the current state; any errors; and the concrete next steps.
Be thorough but compact. Write the summary only.`

const titlePrompt = `Generate a short title (3-6 words, no quotes, no trailing punctuation) describing the user's request below.
Reply with the title only, in the same language as the request.

Request:
`

const InitPrompt = `Analyze this codebase and create (or improve) an AGENTS.md file in the project root that will be given to
future AI coding agents working here. Include:
1. Build, lint and test commands — especially how to run a single test.
2. Code style guidelines: imports, formatting, types, naming, error handling.
3. A brief architecture overview of the important directories.
Look for existing rules (.cursorrules, .cursor/rules/, .github/copilot-instructions.md, CLAUDE.md, CONTRIBUTING.md)
and incorporate them. Keep it around 30-60 lines. If AGENTS.md already exists, improve it.`
