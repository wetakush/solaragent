package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/wetakush/solaragent/internal/util"
)

// ---------- glob ----------

type Glob struct{}

func (*Glob) Name() string       { return "glob" }
func (*Glob) Permission() string { return PermNone }
func (*Glob) Description() string {
	return `Find files by glob pattern (supports **, *, ?, {a,b}). Returns paths sorted by modification time (newest first).
Examples: "**/*.go", "src/**/*.{ts,tsx}", "*.md".`
}
func (*Glob) Schema() map[string]any {
	return obj(map[string]any{
		"pattern": str("Glob pattern to match"),
		"path":    str("Directory to search in (default: project directory)"),
	}, "pattern")
}

func (*Glob) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if in.Pattern == "" {
		return errResult("pattern is required")
	}
	root := env.Abs(in.Path)
	re, err := util.GlobRegexp(in.Pattern)
	if err != nil {
		return errResult("bad pattern: %v", err)
	}
	type hit struct {
		path string
		mod  int64
	}
	var hits []hit
	_ = util.Walk(root, func(rel string, d os.DirEntry) bool {
		if ctx.Err() != nil {
			return false
		}
		if !d.IsDir() && (re.MatchString(rel) || (!strings.Contains(in.Pattern, "/") && re.MatchString(d.Name()))) {
			var mt int64
			if info, err := d.Info(); err == nil {
				mt = info.ModTime().UnixNano()
			}
			hits = append(hits, hit{rel, mt})
		}
		return true
	})
	sort.Slice(hits, func(i, j int) bool { return hits[i].mod > hits[j].mod })
	const limit = 200
	var sb strings.Builder
	for i, h := range hits {
		if i >= limit {
			fmt.Fprintf(&sb, "\n(%d more results not shown — refine the pattern)", len(hits)-limit)
			break
		}
		sb.WriteString(filepath.Join(env.Rel(root), h.path) + "\n")
	}
	if len(hits) == 0 {
		sb.WriteString("No files found")
	}
	return Result{Output: sb.String(), Title: in.Pattern, Meta: map[string]any{"count": len(hits)}}
}

// ---------- grep ----------

type Grep struct{}

func (*Grep) Name() string       { return "grep" }
func (*Grep) Permission() string { return PermNone }
func (*Grep) Description() string {
	return `Search file contents with a regular expression (ripgrep syntax). Returns file:line: matches.
- include filters files by glob, e.g. "*.go" or "*.{ts,tsx}".
- Use this instead of running grep/rg through bash.`
}
func (*Grep) Schema() map[string]any {
	return obj(map[string]any{
		"pattern":     str("Regular expression to search for"),
		"path":        str("File or directory to search (default: project directory)"),
		"include":     str("Glob of files to include, e.g. \"*.go\""),
		"ignore_case": boolean("Case-insensitive search"),
	}, "pattern")
}

func (*Grep) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Include    string `json:"include"`
		IgnoreCase bool   `json:"ignore_case"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if in.Pattern == "" {
		return errResult("pattern is required")
	}
	root := env.Abs(in.Path)
	const limit = 150
	var lines []string
	if rg, err := exec.LookPath("rg"); err == nil {
		args := []string{"-n", "--no-heading", "--color=never", "-M", "400", "--max-columns-preview", "--no-require-git"}
		for _, d := range util.SkipDirs() {
			args = append(args, "-g", "!"+d)
		}
		if in.IgnoreCase {
			args = append(args, "-i")
		}
		if in.Include != "" {
			args = append(args, "-g", in.Include)
		}
		args = append(args, "-e", in.Pattern, root)
		cmd := exec.CommandContext(ctx, rg, args...)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				return Result{Output: "No matches found", Title: in.Pattern, Meta: map[string]any{"count": 0}}
			} else if ok && ee.ExitCode() == 2 && len(out) == 0 {
				return errResult("rg: %s", strings.TrimSpace(string(ee.Stderr)))
			}
		}
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines = append(lines, strings.TrimPrefix(sc.Text(), env.Cwd+"/"))
		}
	} else {
		pat := in.Pattern
		if in.IgnoreCase {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return errResult("bad regex: %v", err)
		}
		var inc *regexp.Regexp
		if in.Include != "" {
			inc, _ = util.GlobRegexp(in.Include)
		}
		search := func(abs string) {
			f, err := os.Open(abs)
			if err != nil {
				return
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			n := 0
			for sc.Scan() {
				n++
				t := sc.Text()
				if n == 1 && strings.IndexByte(t, 0) >= 0 {
					return
				}
				if re.MatchString(t) {
					if len(t) > 400 {
						t = t[:400] + "…"
					}
					lines = append(lines, fmt.Sprintf("%s:%d:%s", env.Rel(abs), n, t))
				}
			}
		}
		if st, err := os.Stat(root); err == nil && !st.IsDir() {
			search(root)
		} else {
			_ = util.Walk(root, func(rel string, d os.DirEntry) bool {
				if ctx.Err() != nil || len(lines) > limit*4 {
					return false
				}
				if !d.IsDir() && (inc == nil || inc.MatchString(d.Name()) || inc.MatchString(rel)) {
					search(filepath.Join(root, rel))
				}
				return true
			})
		}
	}
	total := len(lines)
	if total == 0 {
		return Result{Output: "No matches found", Title: in.Pattern, Meta: map[string]any{"count": 0}}
	}
	if total > limit {
		lines = append(lines[:limit], fmt.Sprintf("\n(%d more matches not shown — narrow the search)", total-limit))
	}
	return Result{Output: truncate(strings.Join(lines, "\n"), 30000), Title: in.Pattern, Meta: map[string]any{"count": total}}
}
