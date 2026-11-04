// Package setup is an interactive wizard for connecting LLM providers.
package setup

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/theme"
	"github.com/wetakush/solaragent/internal/util"
)

type step int

const (
	stProvider step = iota
	stCustom
	stKey
	stVerify
	stModel
	stDone
)

var stepNames = []string{"Provider", "Credentials", "Verify", "Model", "Done"}

func (s step) index() int {
	switch s {
	case stProvider, stCustom:
		return 0
	case stKey:
		return 1
	case stVerify:
		return 2
	case stModel:
		return 3
	}
	return 4
}

type item struct {
	id, name, desc, badge string
	local, custom         bool
}

type Result struct {
	Provider string
	Model    string
	Saved    bool
}

type verifiedMsg struct {
	models []llm.ModelInfo
	err    error
}
type tickMsg struct{}

type model struct {
	a    *app.App
	th   theme.Theme
	w, h int
	st   step

	items    []item
	filtered []int
	cursor   int
	search   textinput.Model

	custom    [3]textinput.Model // id, name, base url
	customFoc int

	key     textinput.Model
	keySrc  string
	chosen  item
	kind    string
	baseURL string

	frame   int
	verErr  string
	models  []llm.ModelInfo
	mFilt   []int
	mCursor int
	mSearch textinput.Model

	result Result
	quit   bool
}

func newInput(ph string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = ph
	ti.Prompt = ""
	return ti
}

// Run launches the wizard. preselect may name a provider id to skip step 1.
func Run(a *app.App, preselect string) (Result, error) {
	m := &model{a: a, th: theme.Get(a.Cfg.Theme)}
	m.search = newInput("type to filter…")
	m.search.Focus()
	m.mSearch = newInput("type to filter…")
	m.key = newInput("paste your API key")
	m.key.EchoMode = textinput.EchoPassword
	m.key.EchoCharacter = '•'
	m.custom[0] = newInput("my-provider")
	m.custom[1] = newInput("My Provider")
	m.custom[2] = newInput("https://api.example.com/v1")
	for _, s := range llm.Builtins {
		it := item{id: s.ID, name: s.Name, desc: strings.TrimPrefix(strings.TrimPrefix(s.Docs, "https://"), "http://"), local: s.Local}
		if s.Local {
			it.desc = "local · " + strings.TrimPrefix(s.BaseURL, "http://")
		}
		if src := a.KeySource(s.ID); src != "" {
			it.badge = "✓ " + src
		}
		m.items = append(m.items, it)
	}
	for id, pc := range a.Cfg.Providers {
		if _, builtin := llm.SpecByID(id); builtin || pc.BaseURL == "" {
			continue
		}
		name := pc.Name
		if name == "" {
			name = id
		}
		m.items = append(m.items, item{id: id, name: name, desc: pc.BaseURL, badge: "custom"})
	}
	m.items = append(m.items, item{id: "", name: "＋ Custom OpenAI-compatible provider", desc: "any base URL", custom: true})
	m.refilter()
	if preselect != "" {
		for _, it := range m.items {
			if it.id == preselect {
				m.choose(it)
			}
		}
		if m.st == stProvider {
			return Result{}, fmt.Errorf("unknown provider %q", preselect)
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return Result{}, err
	}
	return m.result, nil
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, m.tick()}
	if m.st == stVerify {
		cmds = append(cmds, m.verify())
	}
	return tea.Batch(cmds...)
}

func (m *model) tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) refilter() {
	q := strings.TrimSpace(m.search.Value())
	m.filtered = m.filtered[:0]
	if q == "" {
		for i := range m.items {
			m.filtered = append(m.filtered, i)
		}
	} else {
		var keys []string
		for _, it := range m.items {
			keys = append(keys, it.name+" "+it.id)
		}
		m.filtered = util.FuzzyFilter(q, keys)
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
}

func (m *model) refilterModels() {
	q := strings.TrimSpace(m.mSearch.Value())
	m.mFilt = m.mFilt[:0]
	if q == "" {
		for i := range m.models {
			m.mFilt = append(m.mFilt, i)
		}
	} else {
		var keys []string
		for _, mi := range m.models {
			keys = append(keys, mi.Name+" "+mi.ID)
		}
		m.mFilt = util.FuzzyFilter(q, keys)
	}
	if m.mCursor >= len(m.mFilt) {
		m.mCursor = 0
	}
}

func (m *model) choose(it item) {
	m.chosen = it
	m.search.Blur()
	if it.custom {
		m.st = stCustom
		m.customFoc = 0
		m.custom[0].Focus()
		return
	}
	spec, builtin := llm.SpecByID(it.id)
	m.kind, m.baseURL = spec.Kind, spec.BaseURL
	if pc, ok := m.a.Cfg.Providers[it.id]; ok {
		if pc.Kind != "" {
			m.kind = pc.Kind
		}
		if pc.BaseURL != "" {
			m.baseURL = pc.BaseURL
		}
	}
	if m.kind == "" {
		m.kind = "openai"
	}
	if builtin && spec.Local {
		m.st = stVerify
		return
	}
	m.keySrc = m.a.KeySource(it.id)
	m.st = stKey
	m.key.SetValue("")
	m.key.Focus()
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (m *model) existingKey() string {
	if e, ok := m.a.Auth[m.chosen.id]; ok && e.Key != "" {
		return e.Key
	}
	if pc, ok := m.a.Cfg.Providers[m.chosen.id]; ok && pc.APIKey != "" {
		return pc.APIKey
	}
	if spec, ok := llm.SpecByID(m.chosen.id); ok {
		for _, k := range spec.EnvKeys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
	}
	return ""
}

func (m *model) apiKey() string {
	if k := strings.TrimSpace(m.key.Value()); k != "" {
		return k
	}
	return m.existingKey()
}

func (m *model) verify() tea.Cmd {
	m.verErr = ""
	p := llm.Build(m.chosen.id, m.kind, m.apiKey(), m.baseURL, nil)
	if spec, ok := llm.SpecByID(m.chosen.id); ok && m.kind == "openai" {
		p = llm.NewOpenAI(m.chosen.id, m.apiKey(), m.baseURL, nil, spec.Models)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := p.Validate(ctx); err != nil {
			return verifiedMsg{err: err}
		}
		ms, err := p.ListModels(ctx)
		if err == nil && len(ms) > 0 {
			if op, ok := p.(*llm.OpenAI); ok {
				if perr := op.Probe(ctx, ms[0].ID); perr != nil {
					return verifiedMsg{err: perr}
				}
			}
		}
		return verifiedMsg{models: ms, err: err}
	}
}

func (m *model) save() error {
	it := m.chosen
	if it.custom || m.a.Cfg.Providers[it.id].BaseURL != "" {
		if err := config.SaveProvider(it.id, config.ProviderConfig{Name: it.name, BaseURL: m.baseURL}); err != nil {
			return err
		}
	}
	if k := strings.TrimSpace(m.key.Value()); k != "" {
		if err := m.a.SetKey(it.id, k); err != nil {
			return err
		}
	}
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.frame++
		return m, m.tick()
	case verifiedMsg:
		if m.st != stVerify {
			return m, nil
		}
		if msg.err != nil {
			m.verErr = msg.err.Error()
			return m, nil
		}
		if err := m.save(); err != nil {
			m.verErr = "saving failed: " + err.Error()
			return m, nil
		}
		m.result = Result{Provider: m.chosen.id, Saved: true}
		m.models = msg.models
		m.refilterModels()
		if len(m.models) == 0 {
			m.st = stDone
			return m, nil
		}
		m.st = stModel
		m.mSearch.Focus()
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		return m, m.key_(msg)
	}
	return m, nil
}

func (m *model) key_(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	switch m.st {
	case stProvider:
		switch k {
		case "esc":
			return tea.Quit
		case "up", "ctrl+p", "shift+tab":
			if m.cursor > 0 {
				m.cursor--
			}
			return nil
		case "down", "ctrl+n", "tab":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
			return nil
		case "enter":
			if len(m.filtered) > 0 {
				m.choose(m.items[m.filtered[m.cursor]])
				if m.st == stVerify {
					return m.verify()
				}
			}
			return nil
		}
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.refilter()
		return cmd

	case stCustom:
		switch k {
		case "esc":
			m.st = stProvider
			m.search.Focus()
			return nil
		case "tab", "down":
			m.custom[m.customFoc].Blur()
			m.customFoc = (m.customFoc + 1) % 3
			m.custom[m.customFoc].Focus()
			return nil
		case "shift+tab", "up":
			m.custom[m.customFoc].Blur()
			m.customFoc = (m.customFoc + 2) % 3
			m.custom[m.customFoc].Focus()
			return nil
		case "enter":
			if m.customFoc < 2 {
				m.custom[m.customFoc].Blur()
				m.customFoc++
				m.custom[m.customFoc].Focus()
				return nil
			}
			id := strings.ToLower(strings.TrimSpace(m.custom[0].Value()))
			name := strings.TrimSpace(m.custom[1].Value())
			base := strings.TrimRight(strings.TrimSpace(m.custom[2].Value()), "/")
			if !idRe.MatchString(id) {
				m.verErr = "id must be lowercase letters, digits, - or _"
				return nil
			}
			if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
				m.verErr = "base URL must start with http:// or https://"
				return nil
			}
			if name == "" {
				name = id
			}
			m.verErr = ""
			m.chosen = item{id: id, name: name, custom: true}
			m.kind, m.baseURL = "openai", base
			m.keySrc = ""
			m.st = stKey
			m.key.SetValue("")
			m.key.Focus()
			return nil
		}
		var cmd tea.Cmd
		m.custom[m.customFoc], cmd = m.custom[m.customFoc].Update(msg)
		return cmd

	case stKey:
		switch k {
		case "esc":
			m.st = stProvider
			m.search.Focus()
			return nil
		case "ctrl+r":
			if m.key.EchoMode == textinput.EchoPassword {
				m.key.EchoMode = textinput.EchoNormal
			} else {
				m.key.EchoMode = textinput.EchoPassword
			}
			return nil
		case "enter":
			if m.apiKey() == "" && !m.chosen.custom {
				m.verErr = "please paste an API key"
				return nil
			}
			m.st = stVerify
			return m.verify()
		}
		m.verErr = ""
		var cmd tea.Cmd
		m.key, cmd = m.key.Update(msg)
		return cmd

	case stVerify:
		if m.verErr == "" {
			return nil
		}
		switch k {
		case "r", "enter":
			return m.verify()
		case "e", "esc":
			if m.chosen.local {
				m.st = stProvider
				m.search.Focus()
				return nil
			}
			m.st = stKey
			m.key.Focus()
			return nil
		case "s":
			if err := m.save(); err != nil {
				m.verErr = err.Error()
				return nil
			}
			m.result = Result{Provider: m.chosen.id, Saved: true}
			m.st = stDone
			return nil
		}

	case stModel:
		switch k {
		case "esc":
			m.st = stDone
			return nil
		case "up", "ctrl+p":
			if m.mCursor > 0 {
				m.mCursor--
			}
			return nil
		case "down", "ctrl+n":
			if m.mCursor < len(m.mFilt)-1 {
				m.mCursor++
			}
			return nil
		case "pgup":
			m.mCursor = max(m.mCursor-10, 0)
			return nil
		case "pgdown":
			m.mCursor = min(m.mCursor+10, max(len(m.mFilt)-1, 0))
			return nil
		case "enter":
			if len(m.mFilt) == 0 {
				return nil
			}
			mi := m.models[m.mFilt[m.mCursor]]
			ref := m.chosen.id + "/" + mi.ID
			if err := config.SaveGlobal("model", ref); err != nil {
				m.verErr = err.Error()
				return nil
			}
			app.PushRecentModel(ref)
			m.result.Model = ref
			m.st = stDone
			return nil
		}
		var cmd tea.Cmd
		m.mSearch, cmd = m.mSearch.Update(msg)
		m.refilterModels()
		return cmd

	case stDone:
		return tea.Quit
	}
	return nil
}

// ---------- view ----------

var spin = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *model) View() string {
	if m.w == 0 || m.quit {
		return ""
	}
	t := m.th
	cardW := min(76, m.w-4)
	inner := cardW - 6

	logo := strings.Join(t.GradientLines([]string{
		"▄▀▀ ▄▀▄ █   ▄▀▄ █▀▄",
		"▄██ ▀▄▀ █▄▄ █▀█ █▀▄",
	}), "\n")
	sub := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("provider setup")

	// stepper
	var steps []string
	cur := m.st.index()
	for i, n := range stepNames {
		var dot, lbl string
		switch {
		case i < cur:
			dot = lipgloss.NewStyle().Foreground(t.C(t.Success)).Render("●")
			lbl = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(n)
		case i == cur:
			dot = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true).Render("◉")
			lbl = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true).Render(n)
		default:
			dot = lipgloss.NewStyle().Foreground(t.C(t.Subtle)).Render("○")
			lbl = lipgloss.NewStyle().Foreground(t.C(t.Subtle)).Render(n)
		}
		steps = append(steps, dot+" "+lbl)
	}
	sep := lipgloss.NewStyle().Foreground(t.C(t.Subtle)).Render(" ── ")
	stepper := strings.Join(steps, sep)

	title, body, help := m.content(inner)
	titleS := lipgloss.NewStyle().Foreground(t.C(t.Text)).Bold(true).Render(title)
	if m.verErr != "" && m.st != stVerify {
		body += "\n\n" + lipgloss.NewStyle().Foreground(t.C(t.Error)).Render("✗ "+m.verErr)
	}
	card := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Primary)).
		Padding(1, 2).Width(cardW).Render(titleS + "\n\n" + body)
	helpS := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render(help)
	foot := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Render("SolarAgent · open source · ") +
		lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Render(app.Author)

	page := lipgloss.JoinVertical(lipgloss.Center, logo, sub, "", stepper, "", card, helpS, "", foot)
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, page)
}

func (m *model) styleInput(ti *textinput.Model, w int, focused bool) string {
	t := m.th
	ti.Width = w - 4
	ti.TextStyle = lipgloss.NewStyle().Foreground(t.C(t.Text))
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(t.C(t.Muted))
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(t.C(t.Primary))
	bc := t.C(t.Border)
	if focused {
		bc = t.C(t.Primary)
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(bc).Padding(0, 1).Width(w).Render(ti.View())
}

func (m *model) content(inner int) (string, string, string) {
	t := m.th
	muted := lipgloss.NewStyle().Foreground(t.C(t.Muted))
	text := lipgloss.NewStyle().Foreground(t.C(t.Text))
	acc := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true)
	switch m.st {
	case stProvider:
		var rows []string
		rows = append(rows, acc.Render("⌕ ")+m.search.View(), "")
		maxRows := max(min(m.h-22, 12), 4)
		start := 0
		if m.cursor >= maxRows {
			start = m.cursor - maxRows + 1
		}
		for i := start; i < len(m.filtered) && i < start+maxRows; i++ {
			it := m.items[m.filtered[i]]
			sel := i == m.cursor
			name := it.name
			ptr := "  "
			ns := text
			if sel {
				ptr = acc.Render("▸ ")
				ns = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true)
			}
			left := ptr + ns.Render(name)
			if it.badge != "" {
				left += " " + lipgloss.NewStyle().Foreground(t.C(t.Success)).Render(it.badge)
			}
			desc := muted.Render(ansi.Truncate(it.desc, max(inner-lipgloss.Width(left)-2, 0), "…"))
			gap := inner - lipgloss.Width(left) - lipgloss.Width(desc)
			rows = append(rows, left+strings.Repeat(" ", max(gap, 1))+desc)
		}
		if len(m.filtered) == 0 {
			rows = append(rows, muted.Render("  no match"))
		}
		return "Choose a provider", strings.Join(rows, "\n"), "↑↓ move · type to search · enter select · esc quit"

	case stCustom:
		labels := []string{"ID (used in model refs like id/model)", "Display name", "Base URL (OpenAI-compatible, usually ends with /v1)"}
		var parts []string
		for i := range m.custom {
			parts = append(parts, muted.Render(labels[i]), m.styleInput(&m.custom[i], inner, i == m.customFoc))
		}
		return "Custom provider", strings.Join(parts, "\n"), "tab next field · enter continue · esc back"

	case stKey:
		var b strings.Builder
		spec, _ := llm.SpecByID(m.chosen.id)
		b.WriteString(text.Render("Connect ") + acc.Render(m.chosen.name))
		if spec.Docs != "" {
			b.WriteString("\n" + muted.Render("Get a key at ") + lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Underline(true).Render(spec.Docs))
		}
		if m.chosen.custom || m.baseURL != spec.BaseURL {
			b.WriteString("\n" + muted.Render("Endpoint: "+m.baseURL))
		}
		b.WriteString("\n\n" + m.styleInput(&m.key, inner, true))
		if m.keySrc != "" {
			b.WriteString("\n" + lipgloss.NewStyle().Foreground(t.C(t.Success)).Render("✓ a key is already configured ("+m.keySrc+") — press enter to keep it"))
		} else if m.chosen.custom {
			b.WriteString("\n" + muted.Render("leave empty if the endpoint needs no key"))
		}
		if len(spec.EnvKeys) > 0 {
			b.WriteString("\n" + muted.Render("tip: you can also export "+strings.Join(spec.EnvKeys, " or ")))
		}
		return "API key", b.String(), "enter verify · ctrl+r show/hide · esc back"

	case stVerify:
		if m.verErr != "" {
			body := lipgloss.NewStyle().Foreground(t.C(t.Error)).Bold(true).Render("✗ Couldn't verify "+m.chosen.name) + "\n\n" +
				lipgloss.NewStyle().Foreground(t.C(t.Error)).Width(inner).Render(m.verErr)
			return "Verifying", body, "r retry · e edit key · s save anyway · ctrl+c quit"
		}
		sp := lipgloss.NewStyle().Foreground(t.Blend(float64(m.frame%20) / 20)).Render(spin[m.frame%len(spin)])
		body := sp + " " + t.GradientText("Contacting "+m.chosen.name+"…", float64(m.frame)/30, true) + "\n\n" +
			muted.Render("checking the key and fetching available models")
		return "Verifying", body, "please wait"

	case stModel:
		var rows []string
		rows = append(rows, lipgloss.NewStyle().Foreground(t.C(t.Success)).Render(fmt.Sprintf("✓ Connected — %d model(s) available", len(m.models))), "")
		rows = append(rows, acc.Render("⌕ ")+m.mSearch.View(), "")
		maxRows := max(min(m.h-24, 12), 4)
		start := 0
		if m.mCursor >= maxRows {
			start = m.mCursor - maxRows + 1
		}
		for i := start; i < len(m.mFilt) && i < start+maxRows; i++ {
			mi := m.models[m.mFilt[i]]
			sel := i == m.mCursor
			ptr, ns := "  ", text
			if sel {
				ptr, ns = acc.Render("▸ "), lipgloss.NewStyle().Foreground(t.C(t.Primary)).Bold(true)
			}
			left := ptr + ns.Render(ansi.Truncate(mi.Name, inner-24, "…"))
			var d []string
			if mi.Context > 0 {
				d = append(d, util.HumanTokens(int64(mi.Context))+" ctx")
			}
			if mi.InputPrice > 0 {
				d = append(d, fmt.Sprintf("$%g/$%g", mi.InputPrice, mi.OutputPrice))
			}
			desc := muted.Render(strings.Join(d, " · "))
			gap := inner - lipgloss.Width(left) - lipgloss.Width(desc)
			rows = append(rows, left+strings.Repeat(" ", max(gap, 1))+desc)
		}
		return "Choose your default model", strings.Join(rows, "\n"), "↑↓ move · enter set default · esc skip"

	case stDone:
		var b strings.Builder
		b.WriteString(lipgloss.NewStyle().Foreground(t.C(t.Success)).Bold(true).Render("✓ "+m.chosen.name+" is ready") + "\n\n")
		b.WriteString(muted.Render("provider  ") + text.Render(m.chosen.id) + "\n")
		if m.result.Model != "" {
			b.WriteString(muted.Render("model     ") + text.Render(m.result.Model) + "\n")
		}
		where := config.GlobalConfigPath()
		if strings.TrimSpace(m.key.Value()) != "" {
			where = config.AuthFile()
		}
		b.WriteString(muted.Render("saved to  ") + text.Render(where) + "\n\n")
		b.WriteString(text.Render("Start coding with ") + acc.Render("solar"))
		return "All set", b.String(), "press any key"
	}
	return "", "", ""
}
