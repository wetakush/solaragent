package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/tools"
)

// AgentDef is a mode the assistant can operate in (Tab cycles primary agents).
type AgentDef struct {
	Name        string
	Description string
	Prompt      string
	Model       string
	Color       string
	Tools       map[string]bool // explicit enable/disable; nil = defaults
	ReadOnly    bool
	Subagent    bool
	Builtin     bool
}

func (a AgentDef) Allows(tool string) bool {
	if a.Tools != nil {
		if v, ok := a.Tools[tool]; ok {
			return v
		}
		if v, ok := a.Tools["*"]; ok {
			return v
		}
	}
	if a.Subagent && tool == "task" {
		return false
	}
	if a.ReadOnly {
		return tools.ReadOnly(tool) || tool == "bash"
	}
	return true
}

const planPrompt = `You are in PLAN mode. You must NOT modify any files or system state.
Research the codebase with read-only tools, then present a clear, concrete, step-by-step plan
(files to change, approach, risks, how to verify). Only run bash commands that are read-only.
When the user is happy with the plan they will switch to BUILD mode (Tab) to implement it.`

func builtinAgents() []AgentDef {
	return []AgentDef{
		{Name: "build", Description: "Default agent with full tool access for writing code", Color: "primary", Builtin: true},
		{Name: "plan", Description: "Read-only agent for analysis and planning", Color: "secondary", Prompt: planPrompt, ReadOnly: true, Builtin: true},
		{Name: "general", Description: "Read-only research sub-agent used by the task tool", ReadOnly: true, Subagent: true, Builtin: true,
			Prompt: "You are a research sub-agent. Investigate the task thoroughly using read-only tools and finish with a concise, complete report of your findings, including relevant file paths and line numbers."},
	}
}

func (a *App) loadAgents() []AgentDef {
	agents := builtinAgents()
	idx := map[string]int{}
	for i, ag := range agents {
		idx[ag.Name] = i
	}
	put := func(ag AgentDef) {
		if i, ok := idx[ag.Name]; ok {
			base := agents[i]
			if ag.Prompt != "" {
				base.Prompt = ag.Prompt
			}
			if ag.Model != "" {
				base.Model = ag.Model
			}
			if ag.Description != "" {
				base.Description = ag.Description
			}
			if ag.Tools != nil {
				base.Tools = ag.Tools
			}
			if ag.Color != "" {
				base.Color = ag.Color
			}
			agents[i] = base
			return
		}
		idx[ag.Name] = len(agents)
		agents = append(agents, ag)
	}
	for _, dir := range []string{filepath.Join(config.ConfigDir(), "agent"), filepath.Join(a.Root, ".solar", "agent")} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		sort.Strings(files)
		for _, f := range files {
			fm, body := readFrontmatter(f)
			ag := AgentDef{Name: strings.TrimSuffix(filepath.Base(f), ".md"), Description: fm["description"],
				Model: fm["model"], Color: fm["color"], Prompt: body, Subagent: fm["mode"] == "subagent",
				ReadOnly: fm["readonly"] == "true"}
			if t := fm["tools"]; t != "" {
				ag.Tools = map[string]bool{"*": false}
				for _, n := range strings.Split(t, ",") {
					if n = strings.TrimSpace(n); n != "" {
						ag.Tools[n] = true
					}
				}
			}
			put(ag)
		}
	}
	names := make([]string, 0, len(a.Cfg.Agents))
	for n := range a.Cfg.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := a.Cfg.Agents[n]
		if c.Disable {
			if i, ok := idx[n]; ok && n != "build" {
				agents = append(agents[:i], agents[i+1:]...)
				delete(idx, n)
				for k, v := range idx {
					if v > i {
						idx[k] = v - 1
					}
				}
			}
			continue
		}
		put(AgentDef{Name: n, Description: c.Description, Prompt: c.Prompt, Model: c.Model, Tools: c.Tools, Color: c.Color})
	}
	return agents
}

// PrimaryAgents are the ones cycled with Tab.
func (a *App) PrimaryAgents() []AgentDef {
	var out []AgentDef
	for _, ag := range a.Agents {
		if !ag.Subagent {
			out = append(out, ag)
		}
	}
	return out
}

func (a *App) Agent(name string) (AgentDef, bool) {
	for _, ag := range a.Agents {
		if ag.Name == name {
			return ag, true
		}
	}
	return AgentDef{}, false
}

// readFrontmatter parses a simple "---\nkey: value\n---\nbody" markdown file.
func readFrontmatter(path string) (map[string]string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	fm := map[string]string{}
	if strings.HasPrefix(s, "---\n") {
		if end := strings.Index(s[4:], "\n---"); end >= 0 {
			for _, line := range strings.Split(s[4:4+end], "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok {
					fm[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
			s = strings.TrimPrefix(s[4+end+4:], "\n")
		}
	}
	return fm, strings.TrimSpace(s)
}

// ---- custom commands ----

type Command struct {
	Name        string
	Description string
	Template    string
	Agent       string
	Model       string
	Source      string
}

func (a *App) loadCommands() []Command {
	var out []Command
	seen := map[string]int{}
	for _, dir := range []string{filepath.Join(config.ConfigDir(), "command"), filepath.Join(a.Root, ".solar", "command")} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		sort.Strings(files)
		for _, f := range files {
			fm, body := readFrontmatter(f)
			c := Command{Name: strings.TrimSuffix(filepath.Base(f), ".md"), Description: fm["description"],
				Template: body, Agent: fm["agent"], Model: fm["model"], Source: f}
			if c.Description == "" {
				c.Description = "custom command"
			}
			if i, ok := seen[c.Name]; ok {
				out[i] = c
			} else {
				seen[c.Name] = len(out)
				out = append(out, c)
			}
		}
	}
	return out
}

var shellSubst = regexp.MustCompile("!`([^`]+)`")
var posArg = regexp.MustCompile(`\$([1-9])`)

// Render expands $ARGUMENTS, $1..$9 and !`shell` substitutions.
func (c Command) Render(cwd, args string) string {
	fields := strings.Fields(args)
	t := strings.ReplaceAll(c.Template, "$ARGUMENTS", args)
	t = posArg.ReplaceAllStringFunc(t, func(m string) string {
		i := int(m[1] - '1')
		if i < len(fields) {
			return fields[i]
		}
		return ""
	})
	t = shellSubst.ReplaceAllStringFunc(t, func(m string) string {
		cmdline := shellSubst.FindStringSubmatch(m)[1]
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", cmdline)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("%s\n(exit: %v)", strings.TrimSpace(string(out)), err)
		}
		return strings.TrimSpace(string(out))
	})
	if !strings.Contains(c.Template, "$ARGUMENTS") && strings.TrimSpace(args) != "" && !posArg.MatchString(c.Template) {
		t += "\n\n" + args
	}
	return t
}
