package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wetakush/solaragent/internal/util"
)

// ---------- read ----------

type Read struct{}

func (*Read) Name() string       { return "read" }
func (*Read) Permission() string { return PermNone }
func (*Read) Description() string {
	return `Read a file from the filesystem. Returns lines prefixed with line numbers (cat -n style).
- file_path may be absolute or relative to the project directory.
- Reads up to 2000 lines by default; use offset (1-based line) and limit for large files.
- Lines longer than 2000 characters are truncated.
- Always read a file before editing it.`
}
func (*Read) Schema() map[string]any {
	return obj(map[string]any{
		"file_path": str("Path of the file to read"),
		"offset":    num("Line number to start reading from (1-based)"),
		"limit":     num("Number of lines to read"),
	}, "file_path")
}

func (*Read) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	p := env.Abs(in.FilePath)
	st, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return errResult("file not found: %s%s", in.FilePath, suggest(p))
		}
		return errResult("%v", err)
	}
	if st.IsDir() {
		return errResult("%s is a directory — use the ls tool", in.FilePath)
	}
	if st.Size() > 50*1024*1024 {
		return errResult("file is too large (%d bytes)", st.Size())
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return errResult("%v", err)
	}
	if isBinary(b) {
		return errResult("%s looks like a binary file (%d bytes)", in.FilePath, len(b))
	}
	env.markRead(p)
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := in.Offset
	if start < 1 {
		start = 1
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 2000
	}
	var sb strings.Builder
	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	for i := start - 1; i < end; i++ {
		l := lines[i]
		if len(l) > 2000 {
			l = l[:2000] + "…"
		}
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, l)
	}
	if len(lines) == 0 {
		sb.WriteString("(empty file)")
	} else if end < len(lines) {
		fmt.Fprintf(&sb, "\n(showing lines %d-%d of %d; use offset to read more)", start, end, len(lines))
	}
	return Result{Output: sb.String(), Title: env.Rel(p),
		Meta: map[string]any{"path": env.Rel(p), "lines": end - start + 1, "total": len(lines)}}
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

func suggest(p string) string {
	dir, base := filepath.Dir(p), strings.ToLower(filepath.Base(p))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var sims []string
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		if strings.Contains(n, base) || strings.Contains(base, n) {
			sims = append(sims, filepath.Join(dir, e.Name()))
		}
	}
	if len(sims) == 0 {
		return ""
	}
	if len(sims) > 3 {
		sims = sims[:3]
	}
	return "\nDid you mean: " + strings.Join(sims, ", ")
}

// ---------- write ----------

type Write struct{}

func (*Write) Name() string       { return "write" }
func (*Write) Permission() string { return PermEdit }
func (*Write) Description() string {
	return `Write a file, creating parent directories as needed. Overwrites existing files.
- Prefer the edit tool for modifying existing files; read a file before overwriting it.
- Do not create documentation files unless asked.`
}
func (*Write) Schema() map[string]any {
	return obj(map[string]any{
		"file_path": str("Path of the file to write"),
		"content":   str("Full content of the file"),
	}, "file_path", "content")
}

func (*Write) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		FilePath string  `json:"file_path"`
		Content  *string `json:"content"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if in.FilePath == "" || in.Content == nil {
		return errResult("file_path and content are required")
	}
	p := env.Abs(in.FilePath)
	old, readErr := os.ReadFile(p)
	existed := readErr == nil
	if existed && !env.wasRead(p) {
		return errResult("%s already exists — read it first before overwriting", in.FilePath)
	}
	env.snap(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return errResult("%v", err)
	}
	if err := os.WriteFile(p, []byte(*in.Content), 0o644); err != nil {
		return errResult("%v", err)
	}
	env.markRead(p)
	d := util.Diff(string(old), *in.Content)
	add, del := util.DiffStat(d)
	verb := "Created"
	if existed {
		verb = "Wrote"
	}
	return Result{
		Output: fmt.Sprintf("%s %s (%d lines)", verb, env.Rel(p), strings.Count(*in.Content, "\n")+1),
		Title:  env.Rel(p),
		Meta:   map[string]any{"path": env.Rel(p), "diff": d, "added": add, "removed": del},
	}
}

// ---------- edit ----------

type Edit struct{}

func (*Edit) Name() string       { return "edit" }
func (*Edit) Permission() string { return PermEdit }
func (*Edit) Description() string {
	return `Replace text in a file. old_string must match the file exactly (including indentation) and be unique,
unless replace_all is true. Include enough surrounding context to make the match unique.
Read the file first. Use an empty old_string to create a new file with new_string as content.`
}
func (*Edit) Schema() map[string]any {
	return obj(map[string]any{
		"file_path":   str("Path of the file to modify"),
		"old_string":  str("Exact text to replace"),
		"new_string":  str("Replacement text (must differ from old_string)"),
		"replace_all": boolean("Replace every occurrence (default false)"),
	}, "file_path", "old_string", "new_string")
}

type editOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func applyEdit(content string, op editOp) (string, error) {
	if op.OldString == op.NewString {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	n := strings.Count(content, op.OldString)
	if n == 0 {
		// tolerate CRLF files and trailing-whitespace differences
		if strings.Contains(content, "\r\n") {
			o := strings.ReplaceAll(op.OldString, "\n", "\r\n")
			if strings.Count(content, o) > 0 {
				op.OldString, op.NewString = o, strings.ReplaceAll(op.NewString, "\n", "\r\n")
				n = strings.Count(content, o)
			}
		}
		if n == 0 {
			if alt, ok := fuzzyLocate(content, op.OldString); ok {
				op.OldString = alt
				n = strings.Count(content, alt)
			}
		}
		if n == 0 {
			return "", fmt.Errorf("old_string not found in file; re-read the file and copy the text exactly")
		}
	}
	if n > 1 && !op.ReplaceAll {
		return "", fmt.Errorf("old_string matches %d places; add more context or set replace_all", n)
	}
	if op.ReplaceAll {
		return strings.ReplaceAll(content, op.OldString, op.NewString), nil
	}
	return strings.Replace(content, op.OldString, op.NewString, 1), nil
}

// fuzzyLocate finds a block of lines matching old modulo trailing whitespace
// and uniform indentation differences, returning the exact file text.
func fuzzyLocate(content, old string) (string, bool) {
	cl := strings.Split(content, "\n")
	ol := strings.Split(strings.Trim(old, "\n"), "\n")
	if len(ol) == 0 {
		return "", false
	}
	norm := func(s string) string { return strings.TrimSpace(s) }
	var found []string
	for i := 0; i+len(ol) <= len(cl); i++ {
		ok := true
		for j := range ol {
			if norm(cl[i+j]) != norm(ol[j]) {
				ok = false
				break
			}
		}
		if ok {
			found = append(found, strings.Join(cl[i:i+len(ol)], "\n"))
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return "", false
}

func (*Edit) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		FilePath string `json:"file_path"`
		editOp
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	return doEdits(env, in.FilePath, []editOp{in.editOp})
}

func doEdits(env *Env, path string, ops []editOp) Result {
	if path == "" {
		return errResult("file_path is required")
	}
	p := env.Abs(path)
	b, err := os.ReadFile(p)
	if err != nil && !(os.IsNotExist(err) && len(ops) > 0 && ops[0].OldString == "") {
		if os.IsNotExist(err) {
			return errResult("file not found: %s%s", path, suggest(p))
		}
		return errResult("%v", err)
	}
	old := string(b)
	content := old
	for i, op := range ops {
		if op.OldString == "" {
			if content != "" {
				return errResult("edit %d: empty old_string is only allowed for new files", i+1)
			}
			content = op.NewString
			continue
		}
		content, err = applyEdit(content, op)
		if err != nil {
			if len(ops) > 1 {
				return errResult("edit %d: %v (no changes were made)", i+1, err)
			}
			return errResult("%v", err)
		}
	}
	env.snap(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return errResult("%v", err)
	}
	st, _ := os.Stat(p)
	mode := os.FileMode(0o644)
	if st != nil {
		mode = st.Mode()
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		return errResult("%v", err)
	}
	env.markRead(p)
	d := util.Diff(old, content)
	add, del := util.DiffStat(d)
	return Result{
		Output: fmt.Sprintf("Edited %s (+%d -%d)", env.Rel(p), add, del),
		Title:  env.Rel(p),
		Meta:   map[string]any{"path": env.Rel(p), "diff": d, "added": add, "removed": del},
	}
}

type MultiEdit struct{}

func (*MultiEdit) Name() string       { return "multiedit" }
func (*MultiEdit) Permission() string { return PermEdit }
func (*MultiEdit) Description() string {
	return `Apply several edits to one file atomically, in order. Each edit has the same semantics as the edit tool.
If any edit fails, none are applied.`
}
func (*MultiEdit) Schema() map[string]any {
	return obj(map[string]any{
		"file_path": str("Path of the file to modify"),
		"edits": map[string]any{"type": "array", "description": "Edits to apply in sequence",
			"items": obj(map[string]any{
				"old_string":  str("Exact text to replace"),
				"new_string":  str("Replacement text"),
				"replace_all": boolean("Replace every occurrence"),
			}, "old_string", "new_string")},
	}, "file_path", "edits")
}

func (*MultiEdit) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		FilePath string   `json:"file_path"`
		Edits    []editOp `json:"edits"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if len(in.Edits) == 0 {
		return errResult("edits must not be empty")
	}
	return doEdits(env, in.FilePath, in.Edits)
}

// ---------- ls ----------

type LS struct{}

func (*LS) Name() string       { return "ls" }
func (*LS) Permission() string { return PermNone }
func (*LS) Description() string {
	return "List files and directories as a tree (common build/vendor directories are skipped). Prefer glob/grep when you know what you are looking for."
}
func (*LS) Schema() map[string]any {
	return obj(map[string]any{
		"path":   str("Directory to list (default: project directory)"),
		"ignore": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Glob patterns to ignore"},
	})
}

func (*LS) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		Path   string   `json:"path"`
		Ignore []string `json:"ignore"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	root := env.Abs(in.Path)
	const limit = 400
	var files []string
	truncated := false
	_ = util.Walk(root, func(rel string, d os.DirEntry) bool {
		for _, ig := range in.Ignore {
			if ok, _ := filepath.Match(ig, d.Name()); ok {
				return false
			}
		}
		if len(files) >= limit {
			truncated = true
			return false
		}
		if d.IsDir() {
			files = append(files, rel+"/")
		} else {
			files = append(files, rel)
		}
		return true
	})
	sort.Strings(files)
	var sb strings.Builder
	sb.WriteString(root + "/\n")
	for _, f := range files {
		depth := strings.Count(strings.TrimSuffix(f, "/"), "/")
		name := filepath.Base(strings.TrimSuffix(f, "/"))
		if strings.HasSuffix(f, "/") {
			name += "/"
		}
		sb.WriteString(strings.Repeat("  ", depth+1) + name + "\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "\n(truncated at %d entries — use a more specific path or glob)", limit)
	}
	return Result{Output: sb.String(), Title: env.Rel(root), Meta: map[string]any{"count": len(files)}}
}
