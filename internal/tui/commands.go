package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/wetakush/solaragent/internal/agent"
	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/setup"
	"github.com/wetakush/solaragent/internal/theme"
	"github.com/wetakush/solaragent/internal/update"
	"github.com/wetakush/solaragent/internal/util"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

type slashCmd struct {
	name   string
	desc   string
	keys   string
	args   bool
	custom *app.Command
}

var builtinCmds = []slashCmd{
	{name: "new", desc: "start a new session", keys: "ctrl+n"},
	{name: "sessions", desc: "browse and resume sessions", keys: "ctrl+x l"},
	{name: "models", desc: "switch model", keys: "ctrl+o"},
	{name: "agents", desc: "switch agent", keys: "tab"},
	{name: "connect", desc: "connect a provider (setup wizard)"},
	{name: "upgrade", desc: "update SolarAgent to the latest release"},
	{name: "themes", desc: "change color theme", keys: "ctrl+t"},
	{name: "effort", desc: "set reasoning effort", args: true},
	{name: "init", desc: "analyze project & write AGENTS.md"},
	{name: "compact", desc: "summarize conversation to free context", keys: "ctrl+x c"},
	{name: "undo", desc: "revert last turn and its file changes", keys: "ctrl+x u"},
	{name: "rename", desc: "rename session", args: true},
	{name: "export", desc: "export session to Markdown", keys: "ctrl+x x"},
	{name: "copy", desc: "copy last response to clipboard"},
	{name: "editor", desc: "compose message in $EDITOR", keys: "ctrl+e"},
	{name: "thinking", desc: "toggle reasoning visibility"},
	{name: "details", desc: "toggle detailed tool output"},
	{name: "yolo", desc: "toggle auto-approve all tools"},
	{name: "mcp", desc: "show MCP servers"},
	{name: "status", desc: "show session & config info"},
	{name: "help", desc: "show help", keys: "ctrl+x h"},
	{name: "quit", desc: "exit SolarAgent", keys: "ctrl+c"},
}

func (m *Model) allCommands() []slashCmd {
	out := append([]slashCmd(nil), builtinCmds...)
	for i := range m.app.Commands {
		c := &m.app.Commands[i]
		out = append(out, slashCmd{name: c.Name, desc: c.Description, args: true, custom: c})
	}
	return out
}

func (m *Model) findCommand(name string) (slashCmd, bool) {
	if name == "exit" || name == "q" {
		name = "quit"
	}
	if name == "clear" {
		name = "new"
	}
	if name == "model" {
		name = "models"
	}
	if name == "theme" {
		name = "themes"
	}
	for _, c := range m.allCommands() {
		if c.name == name {
			return c, true
		}
	}
	return slashCmd{}, false
}

func (m *Model) runCommand(name, args string) tea.Cmd {
	c, ok := m.findCommand(name)
	if !ok {
		return m.notify(toastErr, "Unknown command /%s", name)
	}
	if c.custom != nil {
		if m.busy {
			return m.notify(toastWarn, "Agent is busy")
		}
		var ag *app.AgentDef
		if c.custom.Agent != "" {
			if a, ok := m.app.Agent(c.custom.Agent); ok {
				ag = &a
			}
		}
		body := m.app.ExpandMentions(c.custom.Render(m.app.Cwd, args))
		display := strings.TrimSpace("/" + c.name + " " + args)
		return m.startRun(body, display, ag, c.custom.Model)
	}
	switch c.name {
	case "new":
		if m.busy {
			m.interrupt()
		}
		m.setSession(m.app.Store.New(m.app.Cwd))
		m.queue = nil
		return tea.Batch(m.notify(toastOK, "New session"), tea.SetWindowTitle(m.windowTitle()))
	case "sessions":
		m.openSessions()
	case "models":
		return m.openModels()
	case "agents":
		m.openAgents()
	case "connect":
		return m.openConnect()
	case "themes":
		m.openThemes()
	case "effort":
		if args != "" {
			return m.setEffort(args)
		}
		m.openEffort()
	case "init":
		if m.busy {
			return m.notify(toastWarn, "Agent is busy")
		}
		return m.startRun(agent.InitPrompt, "/init", nil, "")
	case "compact":
		return m.compact()
	case "undo":
		if m.busy {
			return m.notify(toastWarn, "Wait for the agent to finish")
		}
		text, files, ok := m.sess.Undo()
		if !ok {
			return m.notify(toastWarn, "Nothing to undo")
		}
		_ = m.app.Store.Save(m.sess)
		m.notices = nil
		m.rebuild()
		m.input.SetValue(text)
		m.input.CursorEnd()
		m.layout()
		return m.notify(toastOK, "Reverted last turn (%d file(s) restored)", len(files))
	case "rename":
		if args == "" {
			d := newInputDialog("rename", "Rename session", "New title for this session:", m.title, false)
			d.input.SetValue(m.title)
			m.dlg = d
			return nil
		}
		return m.rename(args)
	case "export":
		if len(m.sess.Messages) == 0 {
			return m.notify(toastWarn, "Nothing to export yet")
		}
		p := exportPath(m.app.Cwd, m.sess.ID)
		if err := os.WriteFile(p, []byte(m.sess.Markdown()), 0o644); err != nil {
			return m.notify(toastErr, "%v", err)
		}
		return m.notify(toastOK, "Exported to %s", p)
	case "copy":
		for i := len(m.sess.Messages) - 1; i >= 0; i-- {
			if msg := m.sess.Messages[i]; msg.Role == llm.RoleAssistant && strings.TrimSpace(msg.Content) != "" {
				copyToClipboard(msg.Content)
				return m.notify(toastOK, "Copied last response to clipboard")
			}
		}
		return m.notify(toastWarn, "No response to copy")
	case "editor":
		return m.openEditor()
	case "thinking":
		m.thinking = !m.thinking
		m.refresh(false)
		return m.notify(toastInfo, "Reasoning %s", onOff(m.thinking))
	case "details":
		m.details = !m.details
		m.refresh(false)
		return m.notify(toastInfo, "Detailed tool output %s", onOff(m.details))
	case "yolo":
		m.yolo = !m.yolo
		m.runner.AutoApprove = m.yolo
		if m.yolo {
			return m.notify(toastWarn, "YOLO mode on — all tool calls are auto-approved")
		}
		return m.notify(toastOK, "YOLO mode off")
	case "mcp":
		m.dlg = &textDialog{id: "mcp", title: "MCP servers", body: m.mcpText()}
	case "status":
		m.dlg = &textDialog{id: "status", title: "Status", body: m.statusText()}
	case "help":
		m.dlg = &textDialog{id: "help", title: "SolarAgent help", body: m.helpText()}
	case "upgrade":
		return m.upgrade()
	case "quit":
		m.quitting = true
		m.interrupt()
		return tea.Quit
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (m *Model) rename(title string) tea.Cmd {
	m.title = title
	m.sess.Title = title
	_ = m.app.Store.Save(m.sess)
	return tea.Batch(m.notify(toastOK, "Session renamed"), tea.SetWindowTitle(m.windowTitle()))
}

func (m *Model) setEffort(e string) tea.Cmd {
	switch e {
	case "low", "medium", "high", "xhigh", "max":
		m.effort = e
		_ = config.SaveGlobal("effort", e)
		return m.notify(toastOK, "Effort set to %s", e)
	}
	return m.notify(toastErr, "effort must be low, medium, high, xhigh or max")
}

func (m *Model) compact() tea.Cmd {
	if m.busy {
		return m.notify(toastWarn, "Wait for the agent to finish")
	}
	if len(m.sess.Messages) < 2 {
		return m.notify(toastWarn, "Nothing to compact yet")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.busy = true
	m.runStart = time.Now()
	m.status = "Compacting conversation"
	ch := make(chan agent.Event, 64)
	m.events = ch
	m.runner.Events = ch
	r, model, ag := m.runner, m.model, m.agent()
	go func() {
		defer close(ch)
		if err := r.Compact(ctx, model, ag); err != nil && ctx.Err() == nil {
			ch <- agent.Event{Kind: agent.EvError, Err: err}
		}
	}()
	return tea.Batch(waitEvent(ch, m.sess.ID), m.tick())
}

func copyToClipboard(s string) {
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}, {"pbcopy"}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = strings.NewReader(s)
			if cmd.Run() == nil {
				return
			}
		}
	}
	_, _ = osc52.New(s).WriteTo(os.Stderr)
}

// ---- dialogs ----

func (m *Model) openPalette() {
	var items []listItem
	for _, c := range m.allCommands() {
		sec := "Commands"
		if c.custom != nil {
			sec = "Custom commands"
		}
		desc := c.desc
		if c.keys != "" {
			desc += "  " + c.keys
		}
		items = append(items, listItem{Title: "/" + c.name, Desc: desc, Value: c.name, Section: sec})
	}
	m.dlg = newListDialog("palette", "Commands", items)
}

func (m *Model) openSessions() {
	list, _ := m.app.Store.List()
	var items []listItem
	for _, s := range list {
		day := s.Updated.Format("Monday, Jan 2")
		if time.Since(s.Updated) < 24*time.Hour && s.Updated.Day() == time.Now().Day() {
			day = "Today"
		}
		items = append(items, listItem{Title: s.Title, Desc: fmt.Sprintf("%s · %d msgs", relTime(s.Updated), len(s.Messages)),
			Value: s.ID, Section: day, Current: m.sess != nil && s.ID == m.sess.ID})
	}
	d := newListDialog("sessions", "Sessions", items)
	d.actions = map[string]string{"ctrl+d": "delete"}
	d.width = 76
	m.dlg = d
}

func (m *Model) modelItems(models []llm.ModelInfo) []listItem {
	var items []listItem
	recent := app.RecentModels()
	byRef := map[string]llm.ModelInfo{}
	for _, mi := range models {
		byRef[mi.Ref()] = mi
	}
	desc := func(mi llm.ModelInfo) string {
		var parts []string
		if mi.Context > 0 {
			parts = append(parts, util.HumanTokens(int64(mi.Context))+" ctx")
		}
		if mi.InputPrice > 0 {
			parts = append(parts, fmt.Sprintf("$%g/$%g", mi.InputPrice, mi.OutputPrice))
		}
		return strings.Join(parts, " · ")
	}
	for i, r := range recent {
		if i >= 5 {
			break
		}
		mi, ok := byRef[r]
		if !ok {
			mi = m.app.ModelInfo(r)
		}
		items = append(items, listItem{Title: mi.Name, Desc: r, Value: r, Section: "Recent", Current: r == m.model})
	}
	provName := func(id string) string {
		if s, ok := llm.SpecByID(id); ok {
			return s.Name
		}
		return id
	}
	for _, mi := range models {
		items = append(items, listItem{Title: mi.Name, Desc: desc(mi), Value: mi.Ref(), Section: provName(mi.Provider),
			Current: mi.Ref() == m.model && len(recent) == 0})
	}
	return items
}

func (m *Model) openModels() tea.Cmd {
	var initial []llm.ModelInfo
	for _, id := range m.app.ConfiguredProviders() {
		if s, ok := llm.SpecByID(id); ok {
			for _, mi := range s.Models {
				mi.Provider = id
				initial = append(initial, mi)
			}
		}
	}
	d := newListDialog("models", "Select model", m.modelItems(initial))
	d.loading = true
	d.width = 80
	d.hint = "/connect to add providers"
	m.dlg = d
	a := m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return modelsMsg{a.ListModels(ctx)}
	}
}

func (m *Model) openAgents() {
	var items []listItem
	for i, ag := range m.app.PrimaryAgents() {
		badge := ""
		if ag.ReadOnly {
			badge = "read-only"
		}
		items = append(items, listItem{Title: agentLabel(ag.Name), Desc: ag.Description, Value: ag.Name, Badge: badge, Current: i == m.agentIdx})
	}
	m.dlg = newListDialog("agents", "Select agent", items)
}

func (m *Model) openThemes() {
	m.themeBefore = m.th
	var items []listItem
	for _, n := range theme.Names() {
		t := theme.Get(n)
		desc := "dark"
		if !t.Dark {
			desc = "for light terminals"
		}
		items = append(items, listItem{Title: n, Desc: desc, Value: n, Current: n == m.th.Name})
	}
	d := newListDialog("themes", "Select theme", items)
	d.hint = "live preview"
	d.width = 48
	m.dlg = d
}

func (m *Model) openEffort() {
	var items []listItem
	descs := map[string]string{"low": "fastest, cheapest", "medium": "balanced", "high": "thorough (default)",
		"xhigh": "deep reasoning for hard coding tasks", "max": "maximum effort"}
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		items = append(items, listItem{Title: e, Desc: descs[e], Value: e, Current: e == m.effort})
	}
	d := newListDialog("effort", "Reasoning effort", items)
	d.width = 56
	m.dlg = d
}

// setupExec runs the provider setup wizard while the TUI is suspended.
type setupExec struct {
	a   *app.App
	res setup.Result
}

func (e *setupExec) Run() error {
	var err error
	e.res, err = setup.Run(e.a, "")
	return err
}
func (e *setupExec) SetStdin(io.Reader)  {}
func (e *setupExec) SetStdout(io.Writer) {}
func (e *setupExec) SetStderr(io.Writer) {}

type setupDoneMsg struct {
	res setup.Result
	err error
}

func (m *Model) openConnect() tea.Cmd {
	e := &setupExec{a: m.app}
	return tea.Exec(e, func(err error) tea.Msg { return setupDoneMsg{res: e.res, err: err} })
}

func (m *Model) onSetupDone(msg setupDoneMsg) tea.Cmd {
	m.app.Reload()
	m.refresh(true)
	if msg.err != nil {
		return m.notify(toastErr, "setup: %v", msg.err)
	}
	if !msg.res.Saved {
		return nil
	}
	if msg.res.Model != "" {
		m.model = msg.res.Model
	} else if pid, _, _ := llm.ParseRef(m.model); pid != msg.res.Provider {
		if _, err := m.app.Provider(pid); err != nil {
			m.model = m.app.DefaultModel()
		}
	}
	return m.notify(toastOK, "Connected %s", msg.res.Provider)
}

func (m *Model) onDialogResult(msg dialogResultMsg) tea.Cmd {
	switch msg.id {
	case "palette":
		c, _ := m.findCommand(msg.item.Value)
		if c.args && c.custom == nil && c.name != "effort" && c.name != "rename" {
			m.input.SetValue("/" + c.name + " ")
			m.input.CursorEnd()
			return nil
		}
		if c.custom != nil {
			m.input.SetValue("/" + c.name + " ")
			m.input.CursorEnd()
			return nil
		}
		return m.runCommand(msg.item.Value, "")
	case "sessions":
		s, err := m.app.Store.Load(msg.item.Value)
		if err != nil {
			return m.notify(toastErr, "%v", err)
		}
		if m.busy {
			m.interrupt()
		}
		m.setSession(s)
		m.vp.GotoBottom()
		return tea.Batch(m.notify(toastOK, "Resumed: %s", s.Title), tea.SetWindowTitle(m.windowTitle()))
	case "models":
		m.model = msg.item.Value
		app.PushRecentModel(m.model)
		pid, _, _ := llm.ParseRef(m.model)
		if _, err := m.app.Provider(pid); err != nil {
			return m.notify(toastWarn, "Model set, but %v", err)
		}
		return m.notify(toastOK, "Model: %s", m.app.ModelInfo(m.model).Name)
	case "agents":
		for i, ag := range m.app.PrimaryAgents() {
			if ag.Name == msg.item.Value {
				m.agentIdx = i
			}
		}
		return nil
	case "themes":
		m.th = theme.Get(msg.item.Value)
		m.applyTheme()
		m.refresh(true)
		_ = config.SaveGlobal("theme", msg.item.Value)
		return m.notify(toastOK, "Theme: %s", msg.item.Value)
	case "effort":
		return m.setEffort(msg.item.Value)
	case "rename":
		if msg.value != "" {
			return m.rename(msg.value)
		}
	}
	return nil
}

func (m *Model) onDialogAction(msg dialogActionMsg) tea.Cmd {
	if msg.id == "sessions" && msg.key == "ctrl+d" {
		if m.sess != nil && msg.item.Value == m.sess.ID {
			return m.notify(toastWarn, "Can't delete the current session")
		}
		_ = m.app.Store.Delete(msg.item.Value)
		m.openSessions()
		return m.notify(toastOK, "Session deleted")
	}
	return nil
}

func (m *Model) mcpText() string {
	sts := m.app.MCP.Statuses()
	if len(sts) == 0 {
		return "No MCP servers configured.\n\nAdd servers to solar.json:\n\n" +
			`  "mcp": {` + "\n" +
			`    "fs": { "type": "local", "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."] },` + "\n" +
			`    "docs": { "type": "remote", "url": "https://example.com/mcp" }` + "\n" +
			`  }`
	}
	var sb strings.Builder
	for _, s := range sts {
		kind := "local"
		if s.Remote {
			kind = "remote"
		}
		fmt.Fprintf(&sb, "● %-20s %-10s %-7s %d tools\n", s.Name, s.State, kind, s.Tools)
		if s.Error != "" {
			fmt.Fprintf(&sb, "    %s\n", s.Error)
		}
	}
	var names []string
	for _, t := range m.app.MCP.Tools() {
		names = append(names, t.Name())
	}
	sort.Strings(names)
	if len(names) > 0 {
		sb.WriteString("\nTools:\n")
		for _, n := range names {
			sb.WriteString("  · " + n + "\n")
		}
	}
	return sb.String()
}

func (m *Model) statusText() string {
	var sb strings.Builder
	info := m.app.ModelInfo(m.model)
	pid, _, _ := llm.ParseRef(m.model)
	fmt.Fprintf(&sb, "SolarAgent v%s — open source · %s\n\n", app.Version, app.Repo)
	fmt.Fprintf(&sb, "Session      %s  (%s)\n", m.title, m.sess.ID)
	fmt.Fprintf(&sb, "Agent        %s\n", m.agent().Name)
	fmt.Fprintf(&sb, "Model        %s  (%s)\n", info.Name, m.model)
	fmt.Fprintf(&sb, "Credentials  %s\n", orDash(m.app.KeySource(pid)))
	fmt.Fprintf(&sb, "Effort       %s\n", m.effort)
	fmt.Fprintf(&sb, "Context      %s / %s tokens\n", util.HumanTokens(m.lastCtx), util.HumanTokens(int64(info.Context)))
	fmt.Fprintf(&sb, "Usage        in %s · out %s · cache read %s · write %s\n", util.HumanTokens(m.usage.Input),
		util.HumanTokens(m.usage.Output), util.HumanTokens(m.usage.CacheRead), util.HumanTokens(m.usage.CacheWrite))
	fmt.Fprintf(&sb, "Cost         $%.4f\n", m.cost)
	fmt.Fprintf(&sb, "Auto-approve %s\n\n", onOff(m.yolo))
	fmt.Fprintf(&sb, "Directory    %s\n", m.app.Cwd)
	fmt.Fprintf(&sb, "Config       %s\n", config.GlobalConfigPath())
	fmt.Fprintf(&sb, "Credentials  %s\n", config.AuthFile())
	fmt.Fprintf(&sb, "Sessions     %s\n", m.app.Store.Dir())
	fmt.Fprintf(&sb, "Providers    %s\n", strings.Join(m.app.ConfiguredProviders(), ", "))
	if ins := m.app.Instructions(); ins != "" {
		fmt.Fprintf(&sb, "Instructions loaded (%d chars)\n", len(ins))
	}
	return sb.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (m *Model) helpText() string {
	var sb strings.Builder
	sb.WriteString("SolarAgent — the open-source AI coding agent · made by github.com/wetakush\n\n")
	sb.WriteString("COMMANDS\n")
	for _, c := range m.allCommands() {
		k := ""
		if c.keys != "" {
			k = "  [" + c.keys + "]"
		}
		fmt.Fprintf(&sb, "  /%-12s %s%s\n", c.name, c.desc, k)
	}
	sb.WriteString(`
INPUT
  enter            send message (queued while the agent works)
  alt+enter/ctrl+j newline   (or end a line with \)
  @path            attach a file or directory (fuzzy completion)
  !command         run a shell command and share its output
  ↑ / ↓            input history
  tab / shift+tab  cycle agents (build · plan · custom)

NAVIGATION
  pgup / pgdown    scroll conversation (mouse wheel works too)
  ctrl+k           command palette
  ctrl+x <key>     leader: n new · l sessions · m models · a agents
                   t themes · u undo · c compact · e editor · x export · q quit
  esc esc          interrupt the agent
  ctrl+c           interrupt / clear input / quit

PERMISSIONS
  enter allow once · a always allow (this session) · esc deny
  Configure defaults in solar.json:  "permission": { "bash": "ask", "edit": "allow" }

CUSTOMIZE
  ~/.config/solar/solar.json    global config (model, theme, providers, mcp, agents)
  ./solar.json                  project config
  .solar/command/*.md           custom /commands ($ARGUMENTS, !` + "`cmd`" + `, @file)
  .solar/agent/*.md             custom agents
  AGENTS.md                     project instructions for the agent
`)
	return sb.String()
}

type updateMsg struct {
	out    *update.Outcome
	manual bool
}

func (m *Model) checkUpdate() tea.Cmd {
	cfg := m.app.Cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return updateMsg{out: update.Background(ctx, cfg, 12*time.Hour)}
	}
}

func (m *Model) upgrade() tea.Cmd {
	if update.IsDev() {
		return m.notify(toastWarn, "This is a development build — upgrade with git pull && make install")
	}
	return tea.Batch(m.notify(toastInfo, "Checking for updates…"), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		r, err := update.Latest(ctx, "")
		if err != nil {
			return updateMsg{out: &update.Outcome{Err: err}, manual: true}
		}
		if !update.Newer(r.Version(), app.Version) {
			return updateMsg{out: &update.Outcome{}, manual: true}
		}
		_, err = update.Apply(ctx, r, nil)
		return updateMsg{out: &update.Outcome{Version: r.Version(), Applied: err == nil, Err: err}, manual: true}
	})
}

func (m *Model) onUpdate(msg updateMsg) tea.Cmd {
	o := msg.out
	if o == nil {
		return nil
	}
	switch {
	case o.Err != nil && o.Version != "":
		return m.notify(toastWarn, "v%s is available but auto-update failed: %v — run `solar upgrade`", o.Version, o.Err)
	case o.Err != nil:
		return m.notify(toastErr, "update check failed: %v", o.Err)
	case o.Applied:
		return m.notify(toastOK, "☀ Updated to v%s — restart solar to use it", o.Version)
	case o.Version != "":
		return m.notify(toastInfo, "☀ SolarAgent v%s is available — run /upgrade", o.Version)
	case msg.manual:
		return m.notify(toastOK, "You're on the latest version (v%s)", app.Version)
	}
	return nil
}
