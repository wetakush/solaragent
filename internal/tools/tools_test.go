package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, tool Tool, env *Env, in any) Result {
	b, _ := json.Marshal(in)
	return tool.Run(context.Background(), env, b)
}

func TestFileTools(t *testing.T) {
	dir := t.TempDir()
	env := NewEnv(dir)
	res := run(t, &Write{}, env, map[string]any{"file_path": "a/b.txt", "content": "one\ntwo\nthree\n"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	res = run(t, &Read{}, env, map[string]any{"file_path": "a/b.txt", "offset": 2, "limit": 1})
	if !strings.Contains(res.Output, "     2\ttwo") || strings.Contains(res.Output, "three") {
		t.Fatalf("read: %q", res.Output)
	}
	res = run(t, &Edit{}, env, map[string]any{"file_path": "a/b.txt", "old_string": "two", "new_string": "2"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a/b.txt"))
	if string(b) != "one\n2\nthree\n" {
		t.Fatalf("edit result %q", b)
	}
	// ambiguous edit
	_ = os.WriteFile(filepath.Join(dir, "dup.txt"), []byte("x\nx\n"), 0o644)
	res = run(t, &Edit{}, env, map[string]any{"file_path": "dup.txt", "old_string": "x", "new_string": "y"})
	if !res.IsError {
		t.Fatal("expected ambiguity error")
	}
	res = run(t, &MultiEdit{}, env, map[string]any{"file_path": "dup.txt", "edits": []map[string]any{
		{"old_string": "x", "new_string": "y", "replace_all": true}, {"old_string": "y\ny", "new_string": "z"}}})
	if res.IsError {
		t.Fatal(res.Output)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "dup.txt"))
	if string(b) != "z\n" {
		t.Fatalf("multiedit %q", b)
	}
	// whitespace-tolerant match
	_ = os.WriteFile(filepath.Join(dir, "ws.go"), []byte("func a() {\n\treturn 1\n}\n"), 0o644)
	res = run(t, &Edit{}, env, map[string]any{"file_path": "ws.go", "old_string": "  return 1", "new_string": "\treturn 2"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	// overwrite without read is refused
	_ = os.WriteFile(filepath.Join(dir, "new.txt"), []byte("old"), 0o644)
	if res = run(t, &Write{}, env, map[string]any{"file_path": "new.txt", "content": "x"}); !res.IsError {
		t.Fatal("expected read-before-write error")
	}
}

func TestSearchTools(t *testing.T) {
	dir := t.TempDir()
	env := NewEnv(dir)
	_ = os.MkdirAll(filepath.Join(dir, "src/inner"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules/x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src/inner/a.go"), []byte("package inner\nfunc Hello() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "src/b.ts"), []byte("export const hello = 1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "node_modules/x/c.go"), []byte("func Hello(){}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "debug.log"), []byte("Hello"), 0o644)

	res := run(t, &Glob{}, env, map[string]any{"pattern": "**/*.go"})
	if !strings.Contains(res.Output, "src/inner/a.go") || strings.Contains(res.Output, "node_modules") {
		t.Fatalf("glob: %q", res.Output)
	}
	res = run(t, &Glob{}, env, map[string]any{"pattern": "src/*.{ts,go}"})
	if !strings.Contains(res.Output, "src/b.ts") || strings.Contains(res.Output, "a.go") {
		t.Fatalf("glob braces: %q", res.Output)
	}
	res = run(t, &Grep{}, env, map[string]any{"pattern": "hello", "ignore_case": true})
	if !strings.Contains(res.Output, "a.go:2") || !strings.Contains(res.Output, "b.ts:1") || strings.Contains(res.Output, "debug.log") {
		t.Fatalf("grep: %q", res.Output)
	}
	res = run(t, &LS{}, env, map[string]any{})
	if !strings.Contains(res.Output, "inner/") || strings.Contains(res.Output, "node_modules") {
		t.Fatalf("ls: %q", res.Output)
	}
}

func TestBash(t *testing.T) {
	env := NewEnv(t.TempDir())
	res := run(t, &Bash{}, env, map[string]any{"command": "echo hello && exit 3"})
	if !strings.Contains(res.Output, "hello") || !strings.Contains(res.Output, "Exit code: 3") {
		t.Fatalf("bash: %q", res.Output)
	}
	res = run(t, &Bash{}, env, map[string]any{"command": "sleep 5", "timeout": 200})
	if !strings.Contains(res.Output, "timed out") {
		t.Fatalf("timeout: %q", res.Output)
	}
}

func TestHTMLToText(t *testing.T) {
	out := htmlToText(`<html><head><script>x()</script></head><body><h1>Title</h1><p>Hello <a href="https://x.y">link</a></p><ul><li>a</li></ul></body></html>`)
	for _, want := range []string{"# Title", "[link ](https://x.y)", "- a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "x()") {
		t.Error("script not stripped")
	}
}
