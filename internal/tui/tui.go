// Package tui implements the interactive terminal interface.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/wetakush/solaragent/internal/agent"
	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/session"
	"github.com/wetakush/solaragent/internal/theme"
	"github.com/wetakush/solaragent/internal/tools"
	"github.com/wetakush/solaragent/internal/util"
)

type Options struct {
	Bypass    bool
	SessionID string
	Continue  bool
	Model     string
	Agent     string
	Prompt    string
}

// ---- messages ----

type agentEventMsg struct {
	ev   agent.Event
	sess string
	ch   chan agent.Event
}
type agentClosedMsg struct {
	sess string
	ch   chan agent.Event
}
type tickMsg struct{}
type mcpMsg struct{}
type modelsMsg struct{ models []llm.ModelInfo }
type shellDoneMsg struct {
	cmd string
	res tools.Result
}
type titleMsg struct{ sess, title string }
type editorDoneMsg struct {
	text string
	err  error
}
type filesMsg struct{ files []string }
type submitMsg struct{ text string }

type notice struct {
	anchor int
	b      *block
}

type toastKind int

const (
	toastInfo toastKind = iota
	toastOK
	toastWarn
	toastErr
)

type Model struct {
	app  *app.App
	opts Options
	th   theme.Theme
	w, h int

	sess    *session.Session
	runner  *agent.Runner
	blocks  []*block
	live    []*block
	notices []notice
	title   string

	vp    viewport.Model
	input textarea.Model
	dlg   dialog
	perm  *agent.PermissionRequest

	comp     []listItem
	compSel  int
	compKind byte // '/', '@'

	busy      bool
	cancel    context.CancelFunc
	events    chan agent.Event
	runStart  time.Time
	queue     []string
	pendErr   []string
	status    string
	frame     int
	ticking   bool
	agentIdx  int
	model     string
	effort    string
	thinking  bool
	details   bool
	permMode  int // permAsk | permAcceptEdits | permBypass
	toast     string
	toastK    toastKind
	toastTill time.Time
	history   []string
	histIdx   int
	lastEsc   time.Time
	leader    bool

	usage    llm.Usage
	cost     float64
	lastCtx  int64
	todos    []tools.Todo
	modified []session.FileChange

	files       []string
	themeBefore theme.Theme
	branch      string
	quitting    bool
}

var program atomic.Pointer[tea.Program]

// Run starts the interactive UI.
func Run(cwd string, opts Options) error {
	a := app.New(cwd, func() {
		if p := program.Load(); p != nil {
			p.Send(mcpMsg{})
		}
	})
	defer a.Close()
	m := newModel(a, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	program.Store(p)
	_, err := p.Run()
	return err
}

func newModel(a *app.App, opts Options) *Model {
	m := &Model{app: a, opts: opts, th: theme.Get(a.Cfg.Theme), effort: a.Cfg.Effort, thinking: true, histIdx: -1}
	ta := textarea.New()
	ta.Placeholder = "Ask anything…  (/ commands · @ files · ! shell)"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j", "shift+enter")
	ta.Focus()
	m.input = ta
	m.vp = viewport.New(80, 20)
	m.vp.MouseWheelEnabled = true
	m.vp.MouseWheelDelta = 3
	m.vp.KeyMap = viewport.KeyMap{} // we handle scrolling keys ourselves
	m.applyTheme()
	switch a.Cfg.PermissionMode {
	case "accept-edits", "acceptEdits", "accept_edits":
		m.permMode = permAcceptEdits
	case "bypass", "bypassPermissions", "yolo":
		m.permMode = permBypass
	}
	if opts.Bypass {
		m.permMode = permBypass
	}

	// model selection: flag > config > last used > default
	m.model = opts.Model
	if m.model == "" {
		m.model = a.Cfg.Model
	}
	if m.model == "" {
		if r := app.RecentModels(); len(r) > 0 {
			m.model = r[0]
		}
	}
	if m.model == "" {
		m.model = a.DefaultModel()
	}

	// session
	var sess *session.Session
	if opts.SessionID != "" {
		sess, _ = a.Store.Load(opts.SessionID)
	} else if opts.Continue {
		sess = a.Store.Latest()
	}
	if sess == nil {
		sess = a.Store.New(a.Cwd)
	}
	m.setSession(sess)
	if opts.Agent != "" {
		for i, ag := range a.PrimaryAgents() {
			if ag.Name == opts.Agent {
				m.agentIdx = i
			}
		}
	}
	m.branch = gitBranch(a.Cwd)
	for _, e := range a.CfgErrs {
		m.notices = append(m.notices, notice{anchor: 0, b: &block{kind: bError, text: "config: " + e.Error()}})
	}
	return m
}

func gitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (m *Model) setSession(s *session.Session) {
	// Switching sessions while the agent works: stop the old run and fully
	// reset run state, otherwise the new session would think it is still busy.
	if m.busy {
		m.interrupt()
	}
	m.busy, m.cancel, m.events, m.perm, m.status = false, nil, nil, nil, ""
	m.queue, m.pendErr = nil, nil
	m.sess = s
	m.runner = agent.NewRunner(m.app, s, nil)
	m.applyPermMode()
	m.notices = nil
	m.live = nil
	m.usage, m.cost, m.lastCtx, m.todos = s.Usage, s.Cost, s.LastCtx, s.Todos
	m.title = s.Title
	if s.Agent != "" {
		for i, ag := range m.app.PrimaryAgents() {
			if ag.Name == s.Agent {
				m.agentIdx = i
			}
		}
	}
	if s.Model != "" && len(s.Messages) > 0 && m.opts.Model == "" {
		m.model = s.Model
	}
	m.rebuild()
}

func (m *Model) applyTheme() {
	t := m.th
	st := textarea.Style{
		Base:        lipgloss.NewStyle(),
		CursorLine:  lipgloss.NewStyle().Foreground(t.C(t.Text)),
		Text:        lipgloss.NewStyle().Foreground(t.C(t.Text)),
		Placeholder: lipgloss.NewStyle().Foreground(t.C(t.Muted)),
		Prompt:      lipgloss.NewStyle().Foreground(t.C(t.Primary)),
		EndOfBuffer: lipgloss.NewStyle(),
	}
	m.input.FocusedStyle, m.input.BlurredStyle = st, st
	m.input.Cursor.Style = lipgloss.NewStyle().Foreground(t.C(t.Primary))
}

func (m *Model) agent() app.AgentDef {
	ags := m.app.PrimaryAgents()
	if m.agentIdx >= len(ags) {
		m.agentIdx = 0
	}
	return ags[m.agentIdx]
}

func (m *Model) agentColor(name string) lipgloss.Color {
	t := m.th
	ag, ok := m.app.Agent(name)
	if !ok {
		return t.C(t.Primary)
	}
	switch ag.Color {
	case "", "primary":
		return t.C(t.Primary)
	case "secondary":
		return t.C(t.Secondary)
	case "accent":
		return t.C(t.Accent)
	case "success", "green":
		return t.C(t.Success)
	case "warning", "yellow":
		return t.C(t.Warning)
	case "error", "red":
		return t.C(t.Error)
	case "info", "blue":
		return t.C(t.Info)
	}
	if strings.HasPrefix(ag.Color, "#") {
		return lipgloss.Color(ag.Color)
	}
	return t.C(t.Accent)
}

func agentLabel(name string) string {
	if name == "" {
		return "Build"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// ---- tea ----

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, m.loadFiles(), tea.SetWindowTitle(m.windowTitle()), m.checkUpdate()}
	if m.opts.Prompt != "" {
		p := m.opts.Prompt
		cmds = append(cmds, func() tea.Msg { return submitMsg{p} })
	}
	return tea.Batch(cmds...)
}

func (m *Model) windowTitle() string {
	if m.title != "" && m.title != "New session" {
		return "☀ " + m.title + " · SolarAgent"
	}
	return "☀ SolarAgent"
}

func (m *Model) loadFiles() tea.Cmd {
	cwd := m.app.Cwd
	return func() tea.Msg {
		var files []string
		_ = util.Walk(cwd, func(rel string, d os.DirEntry) bool {
			if len(files) > 25000 {
				return false
			}
			if d.IsDir() {
				files = append(files, rel+"/")
			} else {
				files = append(files, rel)
			}
			return true
		})
		return filesMsg{files}
	}
}

func (m *Model) tick() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func waitEvent(ch chan agent.Event, sess string) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return agentClosedMsg{sess, ch}
		}
		return agentEventMsg{ev, sess, ch}
	}
}

func (m *Model) notify(kind toastKind, format string, a ...any) tea.Cmd {
	m.toast = fmt.Sprintf(format, a...)
	m.toastK = kind
	m.toastTill = time.Now().Add(time.Duration(4+len(m.toast)/40) * time.Second)
	return m.tick()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		m.refresh(true)
		return m, nil

	case tickMsg:
		m.ticking = false
		m.frame++
		if m.busy {
			m.refresh(false)
		}
		if m.busy || time.Now().Before(m.toastTill) {
			return m, m.tick()
		}
		m.toast = ""
		return m, nil

	case mcpMsg:
		return m, nil

	case filesMsg:
		m.files = msg.files
		return m, nil

	case submitMsg:
		return m, m.submit(msg.text)

	case agentEventMsg:
		if m.sess == nil || msg.sess != m.sess.ID || msg.ch != m.events {
			// a run from a session we switched away from: deny pending prompts and drain until it ends
			if msg.ev.Perm != nil {
				msg.ev.Perm.Reply <- agent.Deny
			}
			return m, waitEvent(msg.ch, msg.sess)
		}
		cmd := m.handleEvent(msg.ev)
		return m, tea.Batch(cmd, waitEvent(m.events, msg.sess))

	case agentClosedMsg:
		if m.sess == nil || msg.sess != m.sess.ID || msg.ch != m.events || !m.busy {
			return m, nil
		}
		return m, m.finishRun()

	case titleMsg:
		if m.sess != nil && msg.sess == m.sess.ID && msg.title != "" {
			m.title = msg.title
			if !m.busy {
				m.sess.Title = msg.title
				_ = m.app.Store.Save(m.sess)
			}
			return m, tea.SetWindowTitle(m.windowTitle())
		}
		return m, nil

	case shellDoneMsg:
		m.busy = false
		m.live = nil
		out := strings.TrimSpace(msg.res.Output)
		m.sess.Messages = append(m.sess.Messages, llm.Message{Role: llm.RoleUser, Kind: "shell", Synthetic: true, Time: time.Now(),
			Content: fmt.Sprintf("I ran this shell command myself:\n$ %s\n\nOutput:\n```\n%s\n```", msg.cmd, out),
			Meta:    map[string]any{"command": msg.cmd, "output": out}})
		_ = m.app.Store.Save(m.sess)
		m.rebuild()
		return m, nil

	case editorDoneMsg:
		if msg.err != nil {
			return m, m.notify(toastErr, "editor: %v", msg.err)
		}
		m.input.SetValue(strings.TrimRight(msg.text, "\n"))
		m.input.CursorEnd()
		m.layout()
		return m, nil

	case modelsMsg:
		if d, ok := m.dlg.(*listDialog); ok && d.id == "models" {
			d.setItems(m.modelItems(msg.models))
		}
		return m, nil

	case dialogResultMsg:
		return m, m.onDialogResult(msg)
	case setupDoneMsg:
		return m, m.onSetupDone(msg)
	case updateMsg:
		return m, m.onUpdate(msg)
	case dialogHighlightMsg:
		if msg.id == "themes" {
			m.th = theme.Get(msg.item.Value)
			m.applyTheme()
			m.refresh(true)
		}
		return m, nil
	case dialogCancelMsg:
		if msg.id == "themes" {
			m.th = m.themeBefore
			m.applyTheme()
			m.refresh(true)
		}
		return m, nil
	case dialogActionMsg:
		return m, m.onDialogAction(msg)

	case tea.MouseMsg:
		if m.dlg != nil {
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// ---- keys ----

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.dlg != nil {
		var cmd tea.Cmd
		m.dlg, cmd = m.dlg.Update(msg)
		return cmd
	}
	if m.perm != nil {
		return m.handlePermKey(key)
	}
	if m.leader {
		m.leader = false
		return m.leaderKey(key)
	}

	switch key {
	case "ctrl+c":
		if m.busy {
			m.interrupt()
			return m.notify(toastWarn, "Interrupted")
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.layout()
			return nil
		}
		m.quitting = true
		return tea.Quit
	case "ctrl+x":
		m.leader = true
		return m.notify(toastInfo, "ctrl+x …  n new · l sessions · m models · a agents · t themes · u undo · c compact · e editor · x export · h help · q quit")
	case "ctrl+k", "ctrl+p":
		m.openPalette()
		return nil
	case "ctrl+o":
		return m.openModels()
	case "ctrl+n":
		return m.runCommand("new", "")
	case "ctrl+e":
		return m.openEditor()
	case "ctrl+t":
		m.openThemes()
		return nil
	case "pgup":
		m.vp.HalfViewUp()
		return nil
	case "pgdown":
		m.vp.HalfViewDown()
		return nil
	case "ctrl+home":
		m.vp.GotoTop()
		return nil
	case "ctrl+end":
		m.vp.GotoBottom()
		return nil
	case "esc":
		if len(m.comp) > 0 {
			m.comp = nil
			m.layout()
			return nil
		}
		if m.busy {
			if time.Since(m.lastEsc) < 1500*time.Millisecond {
				m.interrupt()
				return m.notify(toastWarn, "Interrupted")
			}
			m.lastEsc = time.Now()
			return m.notify(toastInfo, "Press esc again to interrupt")
		}
		if m.input.Value() == "" {
			if time.Since(m.lastEsc) < 1500*time.Millisecond {
				m.lastEsc = time.Time{}
				return m.openRewind()
			}
			m.lastEsc = time.Now()
			return m.notify(toastInfo, "Press esc again to rewind to a previous message")
		}
		return nil
	}

	if len(m.comp) > 0 {
		switch key {
		case "up", "ctrl+p":
			m.compSel = (m.compSel - 1 + len(m.comp)) % len(m.comp)
			return nil
		case "down", "ctrl+n":
			m.compSel = (m.compSel + 1) % len(m.comp)
			return nil
		case "tab":
			m.acceptCompletion(false)
			return nil
		case "enter":
			if m.compKind == '/' {
				return m.acceptCompletion(true)
			}
			m.acceptCompletion(false)
			return nil
		}
	}

	switch key {
	case "tab":
		ags := m.app.PrimaryAgents()
		m.agentIdx = (m.agentIdx + 1) % len(ags)
		m.refresh(false)
		return nil
	case "shift+tab":
		return m.setPermMode((m.permMode + 1) % 3)
	case "enter":
		v := m.input.Value()
		if strings.HasSuffix(v, "\\") {
			m.input.SetValue(strings.TrimSuffix(v, "\\") + "\n")
			m.input.CursorEnd()
			m.layout()
			return nil
		}
		m.input.Reset()
		m.comp = nil
		m.histIdx = -1
		m.layout()
		return m.submit(v)
	case "up":
		if m.input.Line() == 0 && (m.input.Value() == "" || m.histIdx >= 0) && len(m.history) > 0 {
			if m.histIdx < len(m.history)-1 {
				m.histIdx++
			}
			m.input.SetValue(m.history[len(m.history)-1-m.histIdx])
			m.input.CursorEnd()
			m.layout()
			return nil
		}
	case "down":
		if m.histIdx >= 0 && m.input.Line() == m.input.LineCount()-1 {
			m.histIdx--
			if m.histIdx < 0 {
				m.input.Reset()
			} else {
				m.input.SetValue(m.history[len(m.history)-1-m.histIdx])
				m.input.CursorEnd()
			}
			m.layout()
			return nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateCompletion()
	m.layout()
	return cmd
}

func (m *Model) leaderKey(key string) tea.Cmd {
	m.toast = ""
	switch key {
	case "n":
		return m.runCommand("new", "")
	case "l":
		m.openSessions()
	case "m":
		return m.openModels()
	case "a":
		m.openAgents()
	case "t":
		m.openThemes()
	case "u":
		return m.runCommand("undo", "")
	case "c":
		return m.runCommand("compact", "")
	case "e":
		return m.openEditor()
	case "x":
		return m.runCommand("export", "")
	case "h":
		return m.runCommand("help", "")
	case "q":
		m.quitting = true
		return tea.Quit
	}
	return nil
}

// handlePermKey answers a permission prompt. Only digits, enter and esc are
// accepted so that text typed by accident can't approve anything.
func (m *Model) handlePermKey(key string) tea.Cmd {
	var d agent.Decision
	switch key {
	case "enter", "1":
		d = agent.Allow
	case "2":
		d = agent.AllowAlways
	case "3":
		m.perm.Reply <- agent.Allow
		m.perm = nil
		m.layout()
		return m.setPermMode(permBypass)
	case "esc", "4":
		d = agent.Deny
	case "ctrl+c":
		m.interrupt()
		return m.notify(toastWarn, "Interrupted")
	case "ctrl+n":
		return m.runCommand("new", "")
	case "pgup":
		m.vp.HalfViewUp()
		return nil
	case "pgdown":
		m.vp.HalfViewDown()
		return nil
	default:
		return nil
	}
	m.perm.Reply <- d
	m.perm = nil
	m.layout()
	return nil
}

func (m *Model) interrupt() {
	m.queue = nil
	if m.cancel != nil {
		m.cancel()
	}
	if m.perm != nil {
		select {
		case m.perm.Reply <- agent.Deny:
		default:
		}
		m.perm = nil
	}
}

// ---- completion ----

func (m *Model) updateCompletion() {
	v := m.input.Value()
	m.comp = nil
	if strings.HasPrefix(v, "/") && !strings.ContainsAny(v, " \n") {
		q := v[1:]
		cmds := m.allCommands()
		names := make([]string, len(cmds))
		for i, c := range cmds {
			names[i] = c.name
		}
		for _, i := range util.FuzzyFilter(q, names) {
			c := cmds[i]
			m.comp = append(m.comp, listItem{Title: "/" + c.name, Desc: c.desc, Value: c.name})
			if len(m.comp) >= 8 {
				break
			}
		}
		m.compKind = '/'
	} else if i := strings.LastIndexAny(v, " \n"); true {
		tok := v[i+1:]
		if strings.HasPrefix(tok, "@") && len(m.files) > 0 {
			q := tok[1:]
			idx := util.FuzzyFilter(q, m.files)
			for _, j := range idx {
				m.comp = append(m.comp, listItem{Title: m.files[j], Value: m.files[j]})
				if len(m.comp) >= 8 {
					break
				}
			}
			m.compKind = '@'
		}
	}
	if m.compSel >= len(m.comp) {
		m.compSel = 0
	}
}

func (m *Model) acceptCompletion(execute bool) tea.Cmd {
	if len(m.comp) == 0 {
		return nil
	}
	it := m.comp[m.compSel]
	m.comp = nil
	if m.compKind == '/' {
		c, _ := m.findCommand(it.Value)
		if execute && !c.args {
			m.input.Reset()
			m.layout()
			return m.runCommand(it.Value, "")
		}
		m.input.SetValue("/" + it.Value + " ")
		m.input.CursorEnd()
		m.layout()
		return nil
	}
	v := m.input.Value()
	i := strings.LastIndexAny(v, " \n")
	m.input.SetValue(v[:i+1] + "@" + it.Value + " ")
	m.input.CursorEnd()
	m.layout()
	return nil
}

// ---- submit / run ----

func (m *Model) submit(raw string) tea.Cmd {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != text {
		m.history = append(m.history, text)
	}
	if strings.HasPrefix(text, "/") {
		name, args, _ := strings.Cut(text[1:], " ")
		if _, ok := m.findCommand(name); ok {
			return m.runCommand(name, strings.TrimSpace(args))
		}
		if !strings.Contains(name, "/") { // allow absolute paths as messages
			return m.notify(toastErr, "Unknown command /%s — try /help", name)
		}
	}
	if strings.HasPrefix(text, "!") {
		if m.busy {
			return m.notify(toastWarn, "Wait for the agent to finish before running shell commands")
		}
		return m.runShell(strings.TrimSpace(text[1:]))
	}
	if m.busy {
		m.queue = append(m.queue, text)
		m.layout()
		return m.notify(toastInfo, "Queued — will send when the agent finishes")
	}
	return m.startRun(m.app.ExpandMentions(text), text, nil, "")
}

func (m *Model) runShell(cmdline string) tea.Cmd {
	if cmdline == "" {
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	m.status = "Running shell"
	ts := &toolState{name: "bash", input: fmt.Sprintf(`{"command":%q}`, cmdline), status: tsRunning, started: time.Now()}
	m.live = []*block{{kind: bTool, tool: ts}}
	m.refresh(true)
	cwd := m.app.Cwd
	return tea.Batch(m.tick(), func() tea.Msg {
		res := tools.RunShell(context.Background(), cwd, cmdline, 2*time.Minute, "")
		return shellDoneMsg{cmd: cmdline, res: res}
	})
}

func (m *Model) startRun(content, display string, agOverride *app.AgentDef, modelOverride string) tea.Cmd {
	ag := m.agent()
	if agOverride != nil {
		ag = *agOverride
	}
	model := m.model
	if ag.Model != "" {
		model = ag.Model
	}
	if modelOverride != "" {
		model = modelOverride
	}
	if pid, _, err := llm.ParseRef(model); err != nil {
		return m.notify(toastErr, "%v", err)
	} else if _, err := m.app.Provider(pid); err != nil {
		m.notices = append(m.notices, notice{anchor: len(m.sess.Messages), b: &block{kind: bError, text: err.Error() + "\nUse /connect to add an API key or /models to pick another model."}})
		m.refresh(true)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.busy = true
	m.runStart = time.Now()
	m.status = ""
	m.pendErr = nil
	ch := make(chan agent.Event, 512)
	m.events = ch
	m.runner.Events = ch
	m.applyPermMode()
	if display != content {
		m.runner.NextDisplay = display
	}
	_, att := splitAttachments(content)
	m.live = []*block{{kind: bUser, text: display, agent: ag.Name, attach: att}}
	m.refresh(true)
	m.vp.GotoBottom()
	r, effort := m.runner, m.effort
	go func() {
		defer close(ch)
		_ = r.Run(ctx, content, ag, model, effort)
	}()
	app.PushRecentModel(model)
	return tea.Batch(waitEvent(ch, m.sess.ID), m.tick())
}

func (m *Model) lastLive(kind blockKind) *block {
	if n := len(m.live); n > 0 && m.live[n-1].kind == kind && m.live[n-1].live {
		return m.live[n-1]
	}
	return nil
}

func (m *Model) endLive() {
	for _, b := range m.live {
		b.live = false
	}
}

func (m *Model) findLiveTool(id string) *toolState {
	for i := len(m.live) - 1; i >= 0; i-- {
		if b := m.live[i]; b.kind == bTool && b.tool.id == id {
			return b.tool
		}
	}
	return nil
}

func (m *Model) handleEvent(ev agent.Event) tea.Cmd {
	switch ev.Kind {
	case agent.EvText:
		if ev.Text == "" {
			return nil
		}
		if b := m.lastLive(bAssistant); b != nil {
			b.text += ev.Text
		} else {
			m.endLive()
			m.live = append(m.live, &block{kind: bAssistant, text: strings.TrimLeft(ev.Text, "\n"), live: true, agent: m.agent().Name})
		}
	case agent.EvReasoning:
		if b := m.lastLive(bReasoning); b != nil {
			b.text += ev.Text
		} else {
			m.endLive()
			m.live = append(m.live, &block{kind: bReasoning, text: ev.Text, live: true})
		}
	case agent.EvToolPending:
		m.endLive()
		if m.findLiveTool(ev.ToolID) == nil {
			m.live = append(m.live, &block{kind: bTool, tool: &toolState{id: ev.ToolID, name: ev.ToolName, status: tsPending, started: time.Now()}})
		}
	case agent.EvToolRun:
		m.endLive()
		ts := m.findLiveTool(ev.ToolID)
		if ts == nil {
			ts = &toolState{id: ev.ToolID, name: ev.ToolName}
			m.live = append(m.live, &block{kind: bTool, tool: ts})
		}
		ts.input, ts.status, ts.started = ev.ToolInput, tsRunning, time.Now()
	case agent.EvToolDone:
		ts := m.findLiveTool(ev.ToolID)
		if ts == nil {
			ts = &toolState{id: ev.ToolID, name: ev.ToolName, input: ev.ToolInput}
			m.live = append(m.live, &block{kind: bTool, tool: ts})
		}
		ts.output, ts.meta, ts.title = ev.Result.Output, withTitleMeta(ev.Result), ev.Result.Title
		ts.elapsed = time.Since(ts.started)
		if ev.Result.IsError {
			ts.status = tsError
		} else {
			ts.status = tsDone
		}
	case agent.EvPermission:
		m.perm = ev.Perm
		m.layout()
		return m.notify(toastWarn, "Permission required")
	case agent.EvStep:
		m.endLive()
	case agent.EvUsage:
		m.usage, m.cost, m.lastCtx = ev.Usage, ev.Cost, ev.LastCtx
	case agent.EvTitle:
		m.title = ev.Text
		return tea.SetWindowTitle(m.windowTitle())
	case agent.EvTodos:
		m.todos = ev.Todos
	case agent.EvCompacting:
		m.status = "Compacting conversation"
	case agent.EvCompacted:
		m.status = ""
		m.live = append(m.live, &block{kind: bSummary})
	case agent.EvError:
		m.pendErr = append(m.pendErr, ev.Err.Error())
		m.live = append(m.live, &block{kind: bError, text: ev.Err.Error()})
	}
	m.refresh(false)
	return nil
}

func withTitleMeta(r *tools.Result) map[string]any {
	out := map[string]any{}
	for k, v := range r.Meta {
		out[k] = v
	}
	return out
}

func (m *Model) finishRun() tea.Cmd {
	m.busy = false
	m.cancel = nil
	m.perm = nil
	m.status = ""
	m.live = nil
	for _, e := range m.pendErr {
		m.notices = append(m.notices, notice{anchor: len(m.sess.Messages), b: &block{kind: bError, text: e}})
	}
	m.pendErr = nil
	m.usage, m.cost, m.lastCtx = m.sess.Usage, m.sess.Cost, m.sess.LastCtx
	m.todos = m.sess.Todos
	if m.sess.Title != "" {
		m.title = m.sess.Title
	}
	m.rebuild()
	m.layout()
	var cmds []tea.Cmd
	if ch := m.runner.PendingTitle(); ch != nil {
		id := m.sess.ID
		cmds = append(cmds, func() tea.Msg {
			select {
			case t := <-ch:
				return titleMsg{id, t}
			case <-time.After(20 * time.Second):
				return nil
			}
		})
	}
	if len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		cmds = append(cmds, func() tea.Msg { return submitMsg{next} })
	}
	cmds = append(cmds, tea.SetWindowTitle(m.windowTitle()))
	return tea.Batch(cmds...)
}

// ---- rendering ----

func (m *Model) rebuild() {
	blocks := buildBlocks(m.sess.Messages, m.th, agentLabel)
	// interleave notices by anchor (approximate: by message count)
	if len(m.notices) > 0 {
		var withNotices []*block
		count := 0
		msgsBefore := countBlocksPerMessage(m.sess.Messages)
		ni := 0
		for i, b := range blocks {
			for ni < len(m.notices) && m.notices[ni].anchor <= msgsBefore[i] {
				withNotices = append(withNotices, m.notices[ni].b)
				ni++
			}
			withNotices = append(withNotices, b)
			count++
		}
		for ; ni < len(m.notices); ni++ {
			withNotices = append(withNotices, m.notices[ni].b)
		}
		blocks = withNotices
	}
	m.blocks = blocks
	if !m.busy {
		m.modified = m.sess.ModifiedFiles()
	}
	m.refresh(true)
}

// countBlocksPerMessage returns, for each display block, the index of the message it came from.
func countBlocksPerMessage(msgs []llm.Message) []int {
	var out []int
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			out = append(out, i)
		case llm.RoleAssistant:
			if m.Synthetic {
				continue
			}
			if strings.TrimSpace(m.Reasoning) != "" {
				out = append(out, i)
			}
			if strings.TrimSpace(m.Content) != "" {
				out = append(out, i)
			}
			for range m.ToolCalls {
				out = append(out, i)
			}
		}
	}
	return out
}

func (m *Model) sidebarWidth() int {
	if m.w >= 120 {
		return 42
	}
	return 0
}

func (m *Model) mainWidth() int { return m.w - m.sidebarWidth() }

func (m *Model) chatWidth() int { return max(m.mainWidth()-4, 20) }

func (m *Model) inputHeight() int {
	return min(max(m.input.LineCount(), 1), 8)
}

func (m *Model) layout() {
	if m.w == 0 {
		return
	}
	mw := m.mainWidth()
	m.input.SetWidth(max(mw-8, 10))
	m.input.SetHeight(m.inputHeight())
	header := 0
	if m.sidebarWidth() == 0 {
		header = 1
	}
	bottom := m.bottomHeight()
	m.vp.Width = mw
	m.vp.Height = max(m.h-header-bottom-1, 3)
}

func (m *Model) bottomHeight() int {
	h := 1 + 2 + m.inputHeight() + 1 // busy line + border + input + meta
	if m.perm != nil {
		h = 1 + lipgloss.Height(m.permView()) + 1
	}
	if len(m.comp) > 0 {
		h += len(m.comp) + 2
	}
	if len(m.queue) > 0 {
		h++
	}
	return h
}

func (m *Model) renderOpts() renderOpts {
	return renderOpts{theme: m.th, width: m.chatWidth(), showThinking: m.thinking, showDetails: m.details,
		frame: m.frame, agentColor: m.agentColor}
}

func (m *Model) refresh(force bool) {
	if m.w == 0 {
		return
	}
	atBottom := m.vp.AtBottom() || force && m.busy
	var parts []string
	o := m.renderOpts()
	for _, b := range m.blocks {
		if s := b.render(o); s != "" {
			parts = append(parts, s)
		}
	}
	for _, b := range m.live {
		if s := b.render(o); s != "" {
			parts = append(parts, s)
		}
	}
	var content string
	if len(parts) == 0 {
		content = m.emptyView()
	} else {
		content = "\n" + strings.Join(parts, "\n\n") + "\n"
		content = lipgloss.NewStyle().PaddingLeft(2).Render(content)
	}
	m.vp.SetContent(content)
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *Model) emptyView() string {
	t := m.th
	w, h := m.mainWidth(), m.vp.Height
	muted := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	text := lipgloss.NewStyle().Foreground(t.C(t.Text))
	key := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true)
	var lines []string
	lines = append(lines, logo(t, w))
	lines = append(lines, lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("A   G   E   N   T"))
	lines = append(lines, "")
	lines = append(lines, text.Render("The open-source AI coding agent for your terminal"))
	lines = append(lines, "")
	rows := [][2]string{
		{"/help", "all commands"},
		{"/models", "switch model"},
		{"/connect", "add a provider"},
		{"/sessions", "browse sessions"},
		{"/init", "create AGENTS.md"},
		{"tab", "build ⇄ plan agent"},
		{"ctrl+k", "command palette"},
	}
	var tbl []string
	for _, r := range rows {
		tbl = append(tbl, key.Render(fmt.Sprintf("%-10s", r[0]))+"  "+muted.Render(r[1]))
	}
	lines = append(lines, lipgloss.NewStyle().Align(lipgloss.Left).Render(strings.Join(tbl, "\n")))
	lines = append(lines, "")
	if len(m.app.ConfiguredProviders()) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.C(t.Warning)).Bold(true).Render("⚠ No provider connected — run /connect or `solar auth login`"))
		lines = append(lines, "")
	}
	star := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Render("★")
	lines = append(lines, star+" "+muted.Render("open source · made by ")+
		lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Underline(true).Render(app.Author))
	block := lipgloss.JoinVertical(lipgloss.Center, lines...)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
}

func (m *Model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.quitting {
		return ""
	}
	t := m.th
	mw := m.mainWidth()
	var main []string
	if m.sidebarWidth() == 0 {
		main = append(main, m.headerView(mw))
	}
	main = append(main, m.vp.View())
	main = append(main, m.busyLine(mw))
	if len(m.queue) > 0 {
		q := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(fmt.Sprintf("  ⧗ %d queued: ", len(m.queue))) +
			lipgloss.NewStyle().Foreground(t.C(t.Text)).Render(ansi.Truncate(m.queue[0], mw-20, "…"))
		main = append(main, q)
	}
	if len(m.comp) > 0 {
		main = append(main, m.completionView(mw))
	}
	if m.perm != nil {
		main = append(main, m.permView())
	} else {
		main = append(main, m.inputView(mw))
	}
	main = append(main, m.metaLine(mw))
	left := lipgloss.NewStyle().Width(mw).Height(m.h - 1).MaxHeight(m.h - 1).Render(strings.Join(main, "\n"))
	body := left
	if sw := m.sidebarWidth(); sw > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, m.sidebarView(sw, m.h-1))
	}
	screen := body + "\n" + m.statusBar()
	if m.dlg != nil {
		screen = overlay(screen, m.dlg.View(t, m.w, m.h), m.w, m.h)
	}
	return screen
}

func (m *Model) headerView(w int) string {
	t := m.th
	left := t.GradientText("☀ SolarAgent", 0, true)
	title := m.title
	if title == "" {
		title = "New session"
	}
	left += lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("  ›  ") + lipgloss.NewStyle().Foreground(t.C(t.Text)).Bold(true).Render(ansi.Truncate(title, max(w/2, 10), "…"))
	right := m.ctxSummary()
	gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	return " " + left + strings.Repeat(" ", max(gap, 1)) + right + " "
}

func (m *Model) ctxSummary() string {
	t := m.th
	info := m.app.ModelInfo(m.model)
	pct := 0
	if info.Context > 0 {
		pct = int(float64(m.lastCtx) / float64(info.Context) * 100)
	}
	s := fmt.Sprintf("%s ctx · %d%%", util.HumanTokens(m.lastCtx), pct)
	if m.cost > 0 {
		s += fmt.Sprintf(" · $%.3f", m.cost)
	}
	return lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(s)
}

func (m *Model) busyVerb() string {
	if m.status != "" {
		return m.status
	}
	for i := len(m.live) - 1; i >= 0; i-- {
		b := m.live[i]
		switch b.kind {
		case bTool:
			if b.tool.status == tsRunning || b.tool.status == tsPending {
				return "Running " + strings.ToLower(toolLabel(b.tool.name))
			}
			return "Thinking"
		case bAssistant:
			if b.live {
				return "Writing"
			}
		case bReasoning:
			return "Reasoning"
		case bUser:
			return "Thinking"
		}
	}
	return "Working"
}

func (m *Model) busyLine(w int) string {
	t := m.th
	if !m.busy {
		return ""
	}
	suns := []string{"◜", "◝", "◞", "◟"}
	sun := lipgloss.NewStyle().Foreground(t.Blend(float64(m.frame%20) / 20)).Bold(true).Render(suns[(m.frame/2)%len(suns)])
	verb := t.GradientText(m.busyVerb()+"…", float64(m.frame)/30, true)
	el := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(fmt.Sprintf("  %s", time.Since(m.runStart).Round(time.Second)))
	hint := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Faint(true).Render("esc esc to interrupt")
	left := "  " + sun + " " + verb + el
	gap := w - lipgloss.Width(left) - lipgloss.Width(hint) - 2
	return left + strings.Repeat(" ", max(gap, 1)) + hint
}

func (m *Model) inputView(w int) string {
	t := m.th
	col := m.agentColor(m.agent().Name)
	prompt := lipgloss.NewStyle().Foreground(col).Bold(true).Render("❯ ")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(col).
		Padding(0, 1).Width(w - 4).Render(lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.input.View()))
	_ = t
	return lipgloss.NewStyle().PaddingLeft(1).Render(box)
}

func (m *Model) metaLine(w int) string {
	t := m.th
	ag := m.agent()
	col := m.agentColor(ag.Name)
	pill := lipgloss.NewStyle().Background(col).Foreground(t.C(t.Bg)).Bold(true).Padding(0, 1).Render(strings.ToUpper(ag.Name))
	info := m.app.ModelInfo(m.model)
	name := info.Name
	if name == "" {
		name = m.model
	}
	prov, _, _ := llm.ParseRef(m.model)
	model := lipgloss.NewStyle().Foreground(t.C(t.Text)).Bold(true).Render(name)
	if prov != name {
		model += lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(" " + prov)
	}
	if info.Effort || prov != "anthropic" {
		model += lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(" · " + m.effort)
	}
	left := "  " + pill + " " + model
	switch m.permMode {
	case permAcceptEdits:
		left += " " + lipgloss.NewStyle().Foreground(t.C(t.Bg)).Background(t.C(t.Warning)).Bold(true).Padding(0, 1).Render("⏵⏵ ACCEPT EDITS")
	case permBypass:
		left += " " + lipgloss.NewStyle().Foreground(t.C(t.Bg)).Background(t.C(t.Error)).Bold(true).Padding(0, 1).Render("⚡ BYPASS PERMISSIONS")
	}
	hints := "tab agent · ctrl+k commands · ctrl+o models"
	if m.busy {
		hints = "enter queue · esc esc interrupt"
	}
	right := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(hints)
	if lipgloss.Width(left)+lipgloss.Width(right)+4 > w {
		right = ""
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	return left + strings.Repeat(" ", max(gap, 1)) + right
}

func (m *Model) completionView(w int) string {
	t := m.th
	inner := min(w-6, 80)
	var rows []string
	for i, it := range m.comp {
		sel := i == m.compSel
		st := lipgloss.NewStyle().Foreground(t.C(t.Text)).Background(t.C(t.Panel))
		dst := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(t.C(t.Panel))
		if sel {
			st = st.Foreground(t.C(t.Bg)).Background(t.C(t.Primary)).Bold(true)
			dst = dst.Foreground(t.C(t.Bg)).Background(t.C(t.Primary))
		}
		title := ansi.Truncate(" "+it.Title, inner/2+10, "…")
		desc := ""
		if it.Desc != "" {
			desc = ansi.Truncate(it.Desc, inner-lipgloss.Width(title)-3, "…") + " "
		}
		pad := inner - lipgloss.Width(title) - lipgloss.Width(desc)
		rows = append(rows, st.Render(title)+st.Render(strings.Repeat(" ", max(pad, 1)))+dst.Render(desc))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Border)).
		BorderBackground(t.C(t.Panel)).Render(strings.Join(rows, "\n"))
	return lipgloss.NewStyle().PaddingLeft(2).Render(box)
}

func (m *Model) permView() string {
	t := m.th
	w := m.mainWidth() - 6
	p := m.perm
	if p == nil {
		return ""
	}
	warn := lipgloss.NewStyle().Foreground(t.C(t.Warning)).Bold(true)
	title := warn.Render("⚠ Permission required") + lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("  ·  ") +
		lipgloss.NewStyle().Foreground(t.C(t.Text)).Bold(true).Render(toolLabel(p.Tool))
	var body string
	var in map[string]any
	_ = jsonUnmarshal(p.Input, &in)
	s := func(k string) string { v, _ := in[k].(string); return v }
	switch p.Tool {
	case "bash":
		body = lipgloss.NewStyle().Foreground(t.C(t.Warning)).Render(clampLines("$ "+s("command"), w-4))
		if d := s("description"); d != "" {
			body = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(d) + "\n" + body
		}
	case "edit":
		body = lipgloss.NewStyle().Foreground(t.C(t.Text)).Render(s("file_path")) + "\n" +
			renderDiffOpt(t, util.Diff(s("old_string"), s("new_string")), w-4, false, false)
	case "multiedit":
		body = lipgloss.NewStyle().Foreground(t.C(t.Text)).Render(s("file_path"))
		if eds, ok := in["edits"].([]any); ok {
			body += lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(fmt.Sprintf("  (%d edits)", len(eds)))
			for i, e := range eds {
				if i >= 2 {
					break
				}
				em, _ := e.(map[string]any)
				o, _ := em["old_string"].(string)
				n, _ := em["new_string"].(string)
				body += "\n" + renderDiffOpt(t, util.Diff(o, n), w-4, false, false)
			}
		}
	case "write":
		c := s("content")
		body = lipgloss.NewStyle().Foreground(t.C(t.Text)).Render(s("file_path")) +
			lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(fmt.Sprintf("  (%d lines)", strings.Count(c, "\n")+1))
		body += "\n" + renderDiff(t, util.Diff("", c), w-4, false)
	case "webfetch":
		body = lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Render(s("url"))
	default:
		body = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(clampLines(ansi.Truncate(p.Input, 600, "…"), w-4))
	}
	if lines := strings.Split(body, "\n"); len(lines) > 16 {
		body = strings.Join(lines[:16], "\n") + "\n" + lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("…")
	}
	k := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true)
	mu := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	keys := k.Render("1") + mu.Render("/enter allow once   ") + k.Render("2") + mu.Render(" always allow "+p.Category+"   ") +
		k.Render("3") + mu.Render(" bypass all   ") + k.Render("esc") + mu.Render(" deny")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Warning)).Padding(0, 1).Width(w).
		Render(title + "\n\n" + body + "\n\n" + keys)
	return lipgloss.NewStyle().PaddingLeft(1).Render(box)
}

func (m *Model) statusBar() string {
	t := m.th
	bg := t.C(t.Panel)
	base := lipgloss.NewStyle().Background(bg)
	cwd := m.app.Cwd
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(cwd, h) {
		cwd = "~" + cwd[len(h):]
	}
	left := base.Foreground(t.C(t.Bg)).Background(t.C(t.Primary)).Bold(true).Render(" ☀ SOLAR ") +
		base.Foreground(t.C(t.Text)).Render(" "+ansi.Truncate(cwd, max(m.w/3, 10), "…"))
	if m.branch != "" {
		left += base.Foreground(t.C(t.Secondary)).Render("  " + m.branch)
	}
	if m.toast != "" && time.Now().Before(m.toastTill) {
		c := t.C(t.Info)
		icon := "●"
		switch m.toastK {
		case toastOK:
			c, icon = t.C(t.Success), "✓"
		case toastWarn:
			c, icon = t.C(t.Warning), "!"
		case toastErr:
			c, icon = t.C(t.Error), "✗"
		}
		left += base.Render("   ") + base.Foreground(c).Bold(true).Render(icon+" "+ansi.Truncate(m.toast, max(m.w-lipgloss.Width(left)-40, 10), "…"))
	}
	right := base.Foreground(t.C(t.Muted)).Render("v"+app.Version+" · open source · ") +
		base.Foreground(t.C(t.Secondary)).Render(app.Author+" ")
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		right = ""
		gap = max(m.w-lipgloss.Width(left), 0)
	}
	return ansi.Truncate(left+base.Render(strings.Repeat(" ", gap))+right, m.w, "")
}

func (m *Model) sidebarView(w, h int) string {
	t := m.th
	inner := w - 4
	head := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Bold(true)
	text := lipgloss.NewStyle().Foreground(t.C(t.Text))
	muted := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	var s []string
	s = append(s, t.GradientText("☀ SolarAgent", float64(m.frame)/60, true))
	s = append(s, muted.Render("v"+app.Version+" · open source"))
	s = append(s, "")
	title := m.title
	if title == "" {
		title = "New session"
	}
	s = append(s, head.Render("SESSION"))
	s = append(s, text.Bold(true).Width(inner).Render(title))
	if m.sess != nil && len(m.sess.Messages) > 0 {
		s = append(s, muted.Render(m.sess.ID))
	}
	s = append(s, "")

	info := m.app.ModelInfo(m.model)
	s = append(s, head.Render("CONTEXT"))
	pct := 0.0
	if info.Context > 0 {
		pct = float64(m.lastCtx) / float64(info.Context)
	}
	barW := inner - 6
	filled := int(pct * float64(barW))
	if pct > 0 && filled == 0 {
		filled = 1
	}
	var bar strings.Builder
	for i := 0; i < barW; i++ {
		if i < filled {
			bar.WriteString(lipgloss.NewStyle().Foreground(t.Blend(float64(i) / float64(max(barW, 1)))).Render("━"))
		} else {
			bar.WriteString(lipgloss.NewStyle().Foreground(t.C(t.Subtle)).Render("━"))
		}
	}
	s = append(s, bar.String()+muted.Render(fmt.Sprintf(" %3d%%", int(pct*100))))
	ctxs := util.HumanTokens(m.lastCtx) + " / " + util.HumanTokens(int64(info.Context)) + " tokens"
	s = append(s, text.Render(ctxs))
	costS := fmt.Sprintf("$%.4f spent", m.cost)
	if info.InputPrice == 0 && m.cost == 0 {
		costS = "cost unknown for this model"
	}
	s = append(s, muted.Render(costS))
	s = append(s, "")

	s = append(s, head.Render("MODEL"))
	name := info.Name
	if name == "" {
		name = m.model
	}
	s = append(s, text.Bold(true).Render(ansi.Truncate(name, inner, "…")))
	prov, _, _ := llm.ParseRef(m.model)
	s = append(s, muted.Render(prov+" · effort "+m.effort))
	s = append(s, "")

	if sts := m.app.MCP.Statuses(); len(sts) > 0 {
		s = append(s, head.Render("MCP"))
		for _, st := range sts {
			dot := lipgloss.NewStyle().Foreground(t.C(t.Success)).Render("●")
			detail := fmt.Sprintf("%d tools", st.Tools)
			switch st.State {
			case "connecting":
				dot = lipgloss.NewStyle().Foreground(t.C(t.Warning)).Render(spinnerFrames[(m.frame/2)%len(spinnerFrames)])
				detail = "connecting"
			case "failed":
				dot = lipgloss.NewStyle().Foreground(t.C(t.Error)).Render("●")
				detail = "failed"
			case "disabled":
				dot = muted.Render("○")
				detail = "disabled"
			}
			s = append(s, dot+" "+text.Render(ansi.Truncate(st.Name, inner-14, "…"))+" "+muted.Render(detail))
		}
		s = append(s, "")
	}

	if len(m.todos) > 0 {
		done := 0
		for _, td := range m.todos {
			if td.Status == "completed" {
				done++
			}
		}
		s = append(s, head.Render(fmt.Sprintf("TODO  %d/%d", done, len(m.todos))))
		s = append(s, renderTodos(t, m.todos, inner))
		s = append(s, "")
	}

	if len(m.modified) > 0 {
		s = append(s, head.Render("MODIFIED FILES"))
		for i, f := range m.modified {
			if i >= 10 {
				s = append(s, muted.Render(fmt.Sprintf("… %d more", len(m.modified)-10)))
				break
			}
			stat := lipgloss.NewStyle().Foreground(t.C(t.DiffAdd)).Render(fmt.Sprintf("+%d", f.Added)) + " " +
				lipgloss.NewStyle().Foreground(t.C(t.DiffDel)).Render(fmt.Sprintf("-%d", f.Removed))
			name := ansi.TruncateLeft(f.Path, max(len(f.Path)-(inner-lipgloss.Width(stat)-2), 0), "…")
			gap := inner - lipgloss.Width(name) - lipgloss.Width(stat)
			s = append(s, text.Render(name)+strings.Repeat(" ", max(gap, 1))+stat)
		}
		s = append(s, "")
	}

	content := strings.Join(s, "\n")
	foot := muted.Render("made with ") + lipgloss.NewStyle().Foreground(t.C(t.Error)).Render("♥") + muted.Render(" by") + "\n" +
		lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Render(app.Author)
	space := h - 2 - lipgloss.Height(content) - lipgloss.Height(foot)
	if space > 0 {
		content += strings.Repeat("\n", space) + "\n" + foot
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(t.C(t.Subtle)).
		Padding(1, 1, 0, 2).Width(w).Height(h).MaxHeight(h).Render(content)
}

// ---- editor ----

func (m *Model) openEditor() tea.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		for _, c := range []string{"nvim", "vim", "nano", "vi"} {
			if _, err := exec.LookPath(c); err == nil {
				ed = c
				break
			}
		}
	}
	if ed == "" {
		return m.notify(toastErr, "Set $EDITOR to use the external editor")
	}
	f, err := os.CreateTemp("", "solar-*.md")
	if err != nil {
		return m.notify(toastErr, "%v", err)
	}
	_, _ = f.WriteString(m.input.Value())
	f.Close()
	parts := strings.Fields(ed)
	cmd := exec.Command(parts[0], append(parts[1:], f.Name())...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return editorDoneMsg{err: err}
		}
		b, err := os.ReadFile(f.Name())
		return editorDoneMsg{text: string(b), err: err}
	})
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2")
}

func exportPath(cwd, id string) string { return filepath.Join(cwd, "solar-session-"+id+".md") }

const (
	permAsk = iota
	permAcceptEdits
	permBypass
)

var permModeNames = []string{"ask before edits & commands", "accept edits automatically", "bypass permissions (approve everything)"}

func (m *Model) applyPermMode() {
	if m.runner != nil {
		m.runner.AutoApprove = m.permMode == permBypass
		m.runner.AcceptEdits = m.permMode == permAcceptEdits
	}
}

func (m *Model) setPermMode(mode int) tea.Cmd {
	m.permMode = mode
	m.applyPermMode()
	switch mode {
	case permAcceptEdits:
		return m.notify(toastInfo, "Accept edits: file changes are applied without asking (shift+tab to switch)")
	case permBypass:
		return m.notify(toastWarn, "Bypass permissions: every tool call runs without asking (shift+tab to switch)")
	}
	return m.notify(toastOK, "Ask mode: edits and commands need approval")
}
