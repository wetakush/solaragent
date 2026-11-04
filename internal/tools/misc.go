package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ---------- webfetch ----------

type WebFetch struct{}

func (*WebFetch) Name() string       { return "webfetch" }
func (*WebFetch) Permission() string { return PermWebFetch }
func (*WebFetch) Description() string {
	return `Fetch a URL and return its content as readable text (HTML is converted), raw "html", or "markdown"-ish text.
Use for documentation, API references and issues. Content over 50000 characters is truncated.`
}
func (*WebFetch) Schema() map[string]any {
	return obj(map[string]any{
		"url":    str("The http(s) URL to fetch"),
		"format": map[string]any{"type": "string", "enum": []string{"text", "markdown", "html"}, "description": "Output format (default markdown)"},
	}, "url")
}

func (*WebFetch) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		URL    string `json:"url"`
		Format string `json:"format"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return errResult("url must start with http:// or https://")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
	if err != nil {
		return errResult("%v", err)
	}
	req.Header.Set("User-Agent", "SolarAgent/1.0 (+https://github.com/wetakush/solaragent)")
	req.Header.Set("Accept", "text/html,text/markdown,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errResult("%v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if resp.StatusCode >= 400 {
		return errResult("HTTP %d fetching %s", resp.StatusCode, in.URL)
	}
	ct := resp.Header.Get("Content-Type")
	body := string(b)
	if strings.Contains(ct, "html") && in.Format != "html" {
		body = htmlToText(body)
	}
	return Result{Output: truncate(body, 50000), Title: in.URL,
		Meta: map[string]any{"status": resp.StatusCode, "bytes": len(b), "type": ct}}
}

var blankLines = regexp.MustCompile(`\n{3,}`)

func htmlToText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var sb strings.Builder
	skip := 0
	var href string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return strings.TrimSpace(blankLines.ReplaceAllString(sb.String(), "\n\n"))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			switch tag {
			case "script", "style", "noscript", "svg", "head", "nav", "footer":
				if tt == html.StartTagToken {
					skip++
				}
			case "br":
				sb.WriteString("\n")
			case "p", "div", "section", "article", "tr", "ul", "ol", "table", "blockquote":
				sb.WriteString("\n\n")
			case "li":
				sb.WriteString("\n- ")
			case "h1", "h2", "h3", "h4", "h5", "h6":
				sb.WriteString("\n\n" + strings.Repeat("#", int(tag[1]-'0')) + " ")
			case "pre":
				sb.WriteString("\n```\n")
			case "code":
				sb.WriteString("`")
			case "a":
				href = ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" {
						href = string(v)
					}
				}
				sb.WriteString("[")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "svg", "head", "nav", "footer":
				if skip > 0 {
					skip--
				}
			case "pre":
				sb.WriteString("\n```\n")
			case "code":
				sb.WriteString("`")
			case "a":
				if href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") {
					sb.WriteString("](" + href + ")")
				} else {
					sb.WriteString("]")
				}
			}
		case html.TextToken:
			if skip == 0 {
				t := string(z.Text())
				t = strings.Join(strings.Fields(t), " ")
				if t != "" {
					sb.WriteString(html.UnescapeString(t) + " ")
				}
			}
		}
	}
}

// ---------- todo ----------

type TodoWrite struct{}

func (*TodoWrite) Name() string       { return "todowrite" }
func (*TodoWrite) Permission() string { return PermNone }
func (*TodoWrite) Description() string {
	return `Create or replace the session's todo list to plan and track multi-step work.
Use it for tasks with 3+ steps. Keep exactly one item in_progress while working; mark items completed immediately when done.
Always send the full list.`
}
func (*TodoWrite) Schema() map[string]any {
	return obj(map[string]any{
		"todos": map[string]any{"type": "array", "description": "The complete todo list",
			"items": obj(map[string]any{
				"id":       str("Unique id"),
				"content":  str("Short imperative description"),
				"status":   map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}},
				"priority": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
			}, "id", "content", "status")},
	}, "todos")
}

func (*TodoWrite) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		Todos []Todo `json:"todos"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	env.mu.Lock()
	env.Todos = in.Todos
	env.mu.Unlock()
	done := 0
	for _, t := range in.Todos {
		if t.Status == "completed" {
			done++
		}
	}
	b, _ := json.Marshal(in.Todos)
	return Result{Output: fmt.Sprintf("Todo list updated (%d/%d done)\n%s", done, len(in.Todos), b),
		Title: fmt.Sprintf("%d/%d done", done, len(in.Todos)), Meta: map[string]any{"todos": in.Todos}}
}

type TodoRead struct{}

func (*TodoRead) Name() string       { return "todoread" }
func (*TodoRead) Permission() string { return PermNone }
func (*TodoRead) Description() string {
	return "Read the session's current todo list."
}
func (*TodoRead) Schema() map[string]any { return obj(map[string]any{}) }
func (*TodoRead) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	env.mu.Lock()
	b, _ := json.MarshalIndent(env.Todos, "", "  ")
	env.mu.Unlock()
	return Result{Output: string(b), Title: "todos"}
}

// ---------- task (sub-agent) ----------

type Task struct{}

func (*Task) Name() string       { return "task" }
func (*Task) Permission() string { return PermNone }
func (*Task) Description() string {
	return `Launch a read-only sub-agent to research a question autonomously (searching and reading many files),
returning only its final report. Use it for broad codebase exploration to keep your own context small.
Give it a complete, self-contained prompt and say exactly what information to return.`
}
func (*Task) Schema() map[string]any {
	return obj(map[string]any{
		"description": str("Short (3-5 word) description of the task"),
		"prompt":      str("Full task for the sub-agent"),
	}, "description", "prompt")
}

func (*Task) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := parse(input, &in); err != nil {
		return errResult("%v", err)
	}
	if env.SubAgent == nil {
		return errResult("sub-agents are not available here")
	}
	out, err := env.SubAgent(ctx, in.Description, in.Prompt)
	if err != nil {
		return errResult("sub-agent failed: %v", err)
	}
	return Result{Output: out, Title: in.Description}
}
