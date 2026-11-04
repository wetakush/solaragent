package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/theme"
	"github.com/wetakush/solaragent/internal/tools"
)

type blockKind int

const (
	bUser blockKind = iota
	bAssistant
	bReasoning
	bTool
	bInfo
	bError
	bShell
	bSummary
)

type toolStatus int

const (
	tsPending toolStatus = iota
	tsRunning
	tsDone
	tsError
)

type block struct {
	kind    blockKind
	text    string
	attach  []string
	footer  string
	agent   string
	tool    *toolState
	live    bool
	started time.Time

	cache    string
	cacheKey string
}

type toolState struct {
	id, name, input string
	status          toolStatus
	output          string
	title           string
	meta            map[string]any
	started         time.Time
	elapsed         time.Duration
}

type renderOpts struct {
	theme        theme.Theme
	width        int
	showThinking bool
	showDetails  bool
	frame        int
	agentColor   func(string) lipgloss.Color
}

func (b *block) key(o renderOpts) string {
	k := fmt.Sprintf("%s|%d|%v|%v|%d", o.theme.Name, o.width, o.showThinking, o.showDetails, len(b.text))
	if b.tool != nil {
		k += fmt.Sprintf("|%d|%d|%d", b.tool.status, len(b.tool.output), len(b.tool.input))
		if b.tool.status == tsRunning || b.tool.status == tsPending {
			k += fmt.Sprintf("|f%d", o.frame/2)
		}
	}
	if b.live {
		k += "|live"
	}
	return k + "|" + b.footer
}

func (b *block) render(o renderOpts) string {
	k := b.key(o)
	if k == b.cacheKey {
		return b.cache
	}
	b.cache = b.doRender(o)
	b.cacheKey = k
	return b.cache
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (b *block) doRender(o renderOpts) string {
	t := o.theme
	w := o.width
	muted := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	switch b.kind {
	case bUser:
		col := o.agentColor(b.agent)
		body, chips := splitAttachments(b.text)
		chips = append(chips, b.attach...)
		st := lipgloss.NewStyle().
			Border(lipgloss.ThickBorder(), false, false, false, true).BorderForeground(col).
			Background(t.C(t.Panel)).Foreground(t.C(t.Text)).
			Padding(1, 2).Width(w)
		content := body
		if len(chips) > 0 {
			var cs []string
			for _, c := range chips {
				cs = append(cs, lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Background(t.C(t.Panel)).Render("📎 "+c))
			}
			content += "\n" + strings.Join(cs, lipgloss.NewStyle().Background(t.C(t.Panel)).Render("  "))
		}
		return st.Render(content)

	case bAssistant:
		out := lipgloss.NewStyle().PaddingLeft(2).Render(markdown(t, b.text, w-4))
		if b.live {
			out += lipgloss.NewStyle().Foreground(t.C(t.Primary)).Render("▍")
		}
		if b.footer != "" {
			out += "\n\n" + lipgloss.NewStyle().PaddingLeft(2).Render(b.footer)
		}
		return out

	case bReasoning:
		if strings.TrimSpace(b.text) == "" {
			if b.live {
				return ""
			}
			return ""
		}
		head := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Italic(true).Bold(true).Render("✦ Thinking")
		if !o.showThinking {
			return lipgloss.NewStyle().PaddingLeft(2).Render(head + muted.Render("  (hidden · /thinking to show)"))
		}
		txt := strings.TrimSpace(b.text)
		body := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Italic(true).Width(w - 6).Render(txt)
		return lipgloss.NewStyle().PaddingLeft(2).Render(
			lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(t.C(t.Subtle)).PaddingLeft(1).Render(head + "\n" + body))

	case bTool:
		return renderTool(b.tool, o)

	case bShell:
		head := lipgloss.NewStyle().Foreground(t.C(t.Warning)).Bold(true).Render("$ ") +
			lipgloss.NewStyle().Foreground(t.C(t.Text)).Bold(true).Render(b.footer)
		out := b.text
		lines := strings.Split(out, "\n")
		if len(lines) > 30 && !o.showDetails {
			lines = append(lines[:30], fmt.Sprintf("… %d more lines", len(lines)-30))
		}
		body := muted.Render(clampLines(strings.Join(lines, "\n"), w-6))
		return lipgloss.NewStyle().PaddingLeft(2).Render(
			lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Subtle)).
				Padding(0, 1).Width(w-4).Render(head + "\n" + body))

	case bSummary:
		bar := lipgloss.NewStyle().Foreground(t.C(t.Subtle)).Render(strings.Repeat("─", max(w-30, 4)))
		lbl := lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Bold(true).Render(" ◎ conversation compacted ")
		return "  " + lbl + bar

	case bInfo:
		return lipgloss.NewStyle().PaddingLeft(2).Foreground(t.C(t.Muted)).Width(w).Render("· " + b.text)

	case bError:
		return lipgloss.NewStyle().PaddingLeft(2).Render(
			lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(t.C(t.Error)).
				PaddingLeft(1).Foreground(t.C(t.Error)).Width(w - 4).Render("✗ " + b.text))
	}
	return b.text
}

func splitAttachments(s string) (string, []string) {
	i := strings.Index(s, "\n\nAttached context:")
	if i < 0 {
		return s, nil
	}
	body := s[:i]
	var chips []string
	rest := s[i:]
	for {
		j := strings.Index(rest, "path=\"")
		if j < 0 {
			break
		}
		rest = rest[j+6:]
		k := strings.IndexByte(rest, '"')
		if k < 0 {
			break
		}
		chips = append(chips, rest[:k])
		rest = rest[k:]
	}
	return body, chips
}

func clampLines(s string, w int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(strings.ReplaceAll(l, "\t", "    "), w, "…")
	}
	return strings.Join(lines, "\n")
}

var toolLabels = map[string]string{
	"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "multiedit": "Edit", "ls": "List",
	"glob": "Glob", "grep": "Grep", "webfetch": "Fetch", "todowrite": "Todo", "todoread": "Todo", "task": "Task",
}

var toolIcons = map[string]string{
	"bash": "❯", "read": "◉", "write": "✎", "edit": "✎", "multiedit": "✎", "ls": "▤",
	"glob": "⌕", "grep": "⌕", "webfetch": "⇣", "todowrite": "≡", "todoread": "≡", "task": "◈",
}

func toolLabel(name string) string {
	if l, ok := toolLabels[name]; ok {
		return l
	}
	if i := strings.Index(name, "_"); i > 0 {
		return name[:i] + " · " + name[i+1:]
	}
	return name
}

func toolSubject(name, input string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(input), &m)
	get := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "bash":
		if d := get("description"); d != "" {
			return d
		}
		return get("command")
	case "read", "write", "edit", "multiedit":
		return get("file_path")
	case "ls":
		if p := get("path"); p != "" {
			return p
		}
		return "."
	case "glob":
		return get("pattern")
	case "grep":
		s := get("pattern")
		if inc := get("include"); inc != "" {
			s += "  in " + inc
		}
		return s
	case "webfetch":
		return get("url")
	case "task":
		return get("description")
	case "todowrite", "todoread":
		return ""
	}
	b, _ := json.Marshal(m)
	s := string(b)
	if s == "null" || s == "{}" {
		return ""
	}
	return s
}

func renderTool(ts *toolState, o renderOpts) string {
	t := o.theme
	w := o.width
	var icon string
	var iconStyle lipgloss.Style
	switch ts.status {
	case tsPending, tsRunning:
		icon = spinnerFrames[(o.frame/2)%len(spinnerFrames)]
		iconStyle = lipgloss.NewStyle().Foreground(t.C(t.Primary))
	case tsDone:
		icon = "✓"
		iconStyle = lipgloss.NewStyle().Foreground(t.C(t.Success))
	case tsError:
		icon = "✗"
		iconStyle = lipgloss.NewStyle().Foreground(t.C(t.Error))
	}
	label := toolLabel(ts.name)
	ti := toolIcons[ts.name]
	if ti == "" {
		ti = "⚙"
	}
	subj := toolSubject(ts.name, ts.input)
	if p := subj; ts.name == "read" || ts.name == "write" || ts.name == "edit" || ts.name == "multiedit" {
		if rel, err := filepath.Rel(".", p); err == nil && !strings.HasPrefix(rel, "..") {
			subj = rel
		}
		if r, ok := ts.meta["path"].(string); ok && r != "" {
			subj = r
		}
	}
	head := iconStyle.Render(icon) + " " +
		lipgloss.NewStyle().Foreground(t.C(t.Accent)).Render(ti) + " " +
		lipgloss.NewStyle().Foreground(t.C(t.Text)).Bold(true).Render(label)
	if subj != "" {
		head += "  " + lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(ansi.Truncate(subj, max(w-lipgloss.Width(head)-18, 10), "…"))
	}
	// right-side stat
	stat := ""
	switch ts.status {
	case tsDone:
		stat = toolStat(ts)
	case tsPending:
		stat = "preparing…"
	case tsRunning:
		stat = time.Since(ts.started).Round(100 * time.Millisecond).String()
	case tsError:
		stat = ts.title
	}
	if stat != "" {
		statS := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Faint(true).Render(stat)
		gap := w - 2 - lipgloss.Width(head) - lipgloss.Width(statS)
		if gap > 1 {
			head += strings.Repeat(" ", gap) + statS
		} else {
			head += "  " + statS
		}
	}
	lines := []string{head}
	body := toolBody(ts, o)
	if body != "" {
		gut := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(t.C(t.Subtle)).PaddingLeft(1).MarginLeft(1)
		lines = append(lines, gut.Render(body))
	}
	return lipgloss.NewStyle().PaddingLeft(2).Render(strings.Join(lines, "\n"))
}

func toolStat(ts *toolState) string {
	n := func(k string) int {
		switch v := ts.meta[k].(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
		return 0
	}
	switch ts.name {
	case "read":
		return fmt.Sprintf("%d lines", n("lines"))
	case "write", "edit", "multiedit":
		return fmt.Sprintf("+%d -%d", n("added"), n("removed"))
	case "glob", "grep":
		c := n("count")
		if c == 1 {
			return "1 match"
		}
		return fmt.Sprintf("%d matches", c)
	case "ls":
		return fmt.Sprintf("%d entries", n("count"))
	case "bash":
		if e, ok := ts.meta["elapsed"].(string); ok {
			if code := n("exit"); code != 0 {
				return fmt.Sprintf("exit %d · %s", code, e)
			}
			return e
		}
	case "webfetch":
		if b := n("bytes"); b > 0 {
			return fmt.Sprintf("%.1f KB", float64(b)/1024)
		}
	case "todowrite":
		return ts.title
	}
	if ts.elapsed > 0 {
		return ts.elapsed.Round(100 * time.Millisecond).String()
	}
	return ""
}

func toolBody(ts *toolState, o renderOpts) string {
	t := o.theme
	w := o.width - 8
	muted := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	if ts.status == tsError {
		out := ts.output
		ls := strings.Split(strings.TrimSpace(out), "\n")
		if len(ls) > 6 && !o.showDetails {
			ls = append(ls[:6], "…")
		}
		return lipgloss.NewStyle().Foreground(t.C(t.Error)).Render(clampLines(strings.Join(ls, "\n"), w))
	}
	if ts.status != tsDone {
		if ts.name == "bash" {
			var in struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal([]byte(ts.input), &in)
			if in.Command != "" {
				return lipgloss.NewStyle().Foreground(t.C(t.Warning)).Render(clampLines("$ "+in.Command, w))
			}
		}
		return ""
	}
	switch ts.name {
	case "bash":
		cmd, _ := ts.meta["command"].(string)
		out := strings.TrimSpace(ts.output)
		ls := strings.Split(out, "\n")
		limit := 8
		if o.showDetails {
			limit = 60
		}
		if len(ls) > limit {
			ls = append(ls[:limit], fmt.Sprintf("… %d more lines", len(ls)-limit))
		}
		s := lipgloss.NewStyle().Foreground(t.C(t.Warning)).Render(clampLines("$ "+firstLine(cmd), w))
		if out != "" && out != "(no output)" {
			s += "\n" + muted.Render(clampLines(strings.Join(ls, "\n"), w))
		}
		return s
	case "write", "edit", "multiedit":
		d, _ := ts.meta["diff"].(string)
		return renderDiff(t, d, w, o.showDetails)
	case "todowrite":
		return renderTodos(t, todosFromMeta(ts.meta), w)
	case "task":
		ls := strings.Split(strings.TrimSpace(ts.output), "\n")
		limit := 6
		if o.showDetails {
			limit = 80
		}
		if len(ls) > limit {
			ls = append(ls[:limit], fmt.Sprintf("… %d more lines", len(ls)-limit))
		}
		return muted.Render(clampLines(strings.Join(ls, "\n"), w))
	case "read", "ls", "glob", "grep", "webfetch", "todoread":
		if !o.showDetails {
			return ""
		}
		ls := strings.Split(strings.TrimSpace(ts.output), "\n")
		if len(ls) > 20 {
			ls = append(ls[:20], fmt.Sprintf("… %d more lines", len(ls)-20))
		}
		return muted.Render(clampLines(strings.Join(ls, "\n"), w))
	default: // MCP and others
		ls := strings.Split(strings.TrimSpace(ts.output), "\n")
		limit := 4
		if o.showDetails {
			limit = 40
		}
		if len(ls) > limit {
			ls = append(ls[:limit], fmt.Sprintf("… %d more lines", len(ls)-limit))
		}
		return muted.Render(clampLines(strings.Join(ls, "\n"), w))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func renderDiff(t theme.Theme, d string, w int, full bool) string { return renderDiffOpt(t, d, w, full, true) }

func renderDiffOpt(t theme.Theme, d string, w int, full, nums bool) string {
	if strings.TrimSpace(d) == "" {
		return lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("(no changes)")
	}
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	limit := 24
	if full {
		limit = 400
	}
	more := 0
	if len(lines) > limit {
		more = len(lines) - limit
		lines = lines[:limit]
	}
	add := lipgloss.NewStyle().Foreground(t.C(t.DiffAdd)).Background(t.C(t.DiffAddBg))
	del := lipgloss.NewStyle().Foreground(t.C(t.DiffDel)).Background(t.C(t.DiffDelBg))
	ctx := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	hunk := lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Faint(true)
	var out []string
	ln := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			fmt.Sscanf(l, "@@ -%d +%d @@", new(int), &ln)
			if i > 0 {
				out = append(out, hunk.Render("     ⋯"))
			}
			continue
		}
		if l == "" {
			continue
		}
		body := strings.ReplaceAll(l[1:], "\t", "    ")
		num := "    "
		if !nums {
			num = ""
		}
		switch l[0] {
		case '+':
			if nums {
				num = fmt.Sprintf("%4d", ln)
			}
			ln++
			out = append(out, ctx.Faint(true).Render(num)+" "+add.Render(padRight("+ "+ansi.Truncate(body, w-8, "…"), w-6)))
		case '-':
			out = append(out, ctx.Faint(true).Render(num)+" "+del.Render(padRight("- "+ansi.Truncate(body, w-8, "…"), w-6)))
		default:
			if nums {
				num = fmt.Sprintf("%4d", ln)
			}
			ln++
			out = append(out, ctx.Faint(true).Render(num)+" "+ctx.Render("  "+ansi.Truncate(body, w-8, "…")))
		}
	}
	if more > 0 {
		out = append(out, ctx.Render(fmt.Sprintf("     … %d more diff lines (/details to expand)", more)))
	}
	return strings.Join(out, "\n")
}

func padRight(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func todosFromMeta(meta map[string]any) []tools.Todo {
	raw, ok := meta["todos"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []tools.Todo:
		return v
	default:
		b, _ := json.Marshal(v)
		var out []tools.Todo
		_ = json.Unmarshal(b, &out)
		return out
	}
}

func renderTodos(t theme.Theme, todos []tools.Todo, w int) string {
	var out []string
	for _, td := range todos {
		var icon string
		st := lipgloss.NewStyle().Foreground(t.C(t.Text))
		switch td.Status {
		case "completed":
			icon = lipgloss.NewStyle().Foreground(t.C(t.Success)).Render("●")
			st = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Strikethrough(true)
		case "in_progress":
			icon = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Render("◐")
			st = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true)
		case "cancelled":
			icon = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("⊘")
			st = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Strikethrough(true)
		default:
			icon = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("○")
		}
		out = append(out, icon+" "+st.Render(ansi.Truncate(td.Content, w-3, "…")))
	}
	return strings.Join(out, "\n")
}

// buildBlocks converts session messages to display blocks.
func buildBlocks(msgs []llm.Message, t theme.Theme, agentLabel func(string) string) []*block {
	var out []*block
	byID := map[string]*toolState{}
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			switch m.Kind {
			case "summary":
				out = append(out, &block{kind: bSummary})
			case "shell":
				cmd, _ := m.Meta["command"].(string)
				outp, _ := m.Meta["output"].(string)
				out = append(out, &block{kind: bShell, text: outp, footer: cmd})
			default:
				txt := m.Content
				var att []string
				if m.Display != "" {
					txt = m.Display
					_, att = splitAttachments(m.Content)
				}
				out = append(out, &block{kind: bUser, text: txt, agent: m.Agent, attach: att})
			}
		case llm.RoleAssistant:
			if m.Synthetic {
				continue
			}
			if strings.TrimSpace(m.Reasoning) != "" {
				out = append(out, &block{kind: bReasoning, text: m.Reasoning})
			}
			if strings.TrimSpace(m.Content) != "" {
				b := &block{kind: bAssistant, text: m.Content, agent: m.Agent}
				// footer on the final answer of a turn
				if len(m.ToolCalls) == 0 && (i == len(msgs)-1 || msgs[i+1].Role == llm.RoleUser) {
					b.footer = assistantFooter(t, m, agentLabel)
				}
				out = append(out, b)
			}
			for _, tc := range m.ToolCalls {
				ts := &toolState{id: tc.ID, name: tc.Name, input: tc.Input, status: tsError, output: "interrupted", title: "interrupted"}
				byID[tc.ID] = ts
				out = append(out, &block{kind: bTool, tool: ts})
			}
		case llm.RoleTool:
			if ts, ok := byID[m.ToolCallID]; ok {
				ts.output = m.Content
				ts.meta = m.Meta
				ts.title, _ = m.Meta["title"].(string)
				if m.IsError {
					ts.status = tsError
				} else {
					ts.status = tsDone
				}
			}
		}
	}
	return out
}

func assistantFooter(t theme.Theme, m llm.Message, agentLabel func(string) string) string {
	dot := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Render("◆")
	parts := []string{agentLabel(m.Agent)}
	if m.Model != "" {
		mod := m.Model
		if i := strings.Index(mod, "/"); i >= 0 {
			mod = mod[i+1:]
		}
		parts = append(parts, mod)
	}
	if m.ElapsedMs > 0 {
		parts = append(parts, (time.Duration(m.ElapsedMs) * time.Millisecond).Round(100*time.Millisecond).String())
	}
	if !m.Time.IsZero() {
		parts = append(parts, m.Time.Format("15:04"))
	}
	return dot + " " + lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(strings.Join(parts, " · "))
}
