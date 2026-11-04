// Package tools implements the built-in tools the agent can call.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wetakush/solaragent/internal/llm"
)

// Permission categories. Tools in a category share an allow/ask/deny rule.
const (
	PermNone     = ""
	PermEdit     = "edit"
	PermBash     = "bash"
	PermWebFetch = "webfetch"
	PermMCP      = "mcp"
)

type Result struct {
	Output  string
	Title   string // short summary shown in the UI
	Meta    map[string]any
	IsError bool
}

type Todo struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`   // pending | in_progress | completed | cancelled
	Priority string `json:"priority"` // high | medium | low
}

// Env is the execution context shared by tool calls in a session.
type Env struct {
	Cwd string

	mu       sync.Mutex
	Todos    []Todo
	readSet  map[string]bool
	Snapshot func(path string)                                       // called before a file is modified
	SubAgent func(ctx context.Context, desc, prompt string) (string, error) // used by the task tool
}

func NewEnv(cwd string) *Env { return &Env{Cwd: cwd, readSet: map[string]bool{}} }

func (e *Env) Abs(p string) string {
	if p == "" {
		return e.Cwd
	}
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.Cwd, p)
	}
	return filepath.Clean(p)
}

func (e *Env) Rel(p string) string {
	if r, err := filepath.Rel(e.Cwd, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func (e *Env) markRead(p string) {
	e.mu.Lock()
	e.readSet[p] = true
	e.mu.Unlock()
}

func (e *Env) wasRead(p string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.readSet[p]
}

func (e *Env) snap(p string) {
	if e.Snapshot != nil {
		e.Snapshot(p)
	}
}

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Permission() string
	Run(ctx context.Context, env *Env, input json.RawMessage) Result
}

func Def(t Tool) llm.ToolDef {
	return llm.ToolDef{Name: t.Name(), Description: t.Description(), Schema: t.Schema()}
}

// Builtins returns all built-in tools.
func Builtins() []Tool {
	return []Tool{
		&Bash{}, &Read{}, &Write{}, &Edit{}, &MultiEdit{}, &LS{}, &Glob{}, &Grep{},
		&WebFetch{}, &TodoWrite{}, &TodoRead{}, &Task{},
	}
}

// ReadOnly reports whether a tool never modifies anything.
func ReadOnly(name string) bool {
	switch name {
	case "read", "ls", "glob", "grep", "webfetch", "todoread", "todowrite", "task":
		return true
	}
	return false
}

func obj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func errResult(format string, a ...any) Result {
	return Result{Output: fmt.Sprintf(format, a...), IsError: true, Title: "error"}
}

func parse(input json.RawMessage, v any) error {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("invalid tool input JSON (it may have been truncated): %v", err)
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + fmt.Sprintf("\n\n… [%d characters truncated] …\n\n", len(s)-max) + s[len(s)-half:]
}
