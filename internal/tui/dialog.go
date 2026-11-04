package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/wetakush/solaragent/internal/theme"
	"github.com/wetakush/solaragent/internal/util"
)

type dialog interface {
	Update(msg tea.KeyMsg) (dialog, tea.Cmd)
	View(t theme.Theme, w, h int) string
	ID() string
}

type dialogResultMsg struct {
	id    string
	item  listItem
	value string
}
type dialogHighlightMsg struct {
	id   string
	item listItem
}
type dialogCancelMsg struct{ id string }
type dialogActionMsg struct {
	id   string
	key  string
	item listItem
}

type listItem struct {
	Title   string
	Desc    string
	Section string
	Badge   string
	Value   string
	Current bool
}

// ---------- list dialog ----------

type listDialog struct {
	id       string
	title    string
	hint     string
	items    []listItem
	filtered []int
	cursor   int
	offset   int
	input    textinput.Model
	actions  map[string]string // key -> label, e.g. "ctrl+d" -> "delete"
	width    int
	loading  bool
}

func newListDialog(id, title string, items []listItem) *listDialog {
	ti := textinput.New()
	ti.Placeholder = "Search…"
	ti.Prompt = "  "
	ti.Focus()
	d := &listDialog{id: id, title: title, items: items, input: ti, width: 64}
	d.refilter()
	for i, fi := range d.filtered {
		if d.items[fi].Current {
			d.cursor = i
		}
	}
	return d
}

func (d *listDialog) ID() string { return d.id }

func (d *listDialog) setItems(items []listItem) {
	cur := ""
	if len(d.filtered) > 0 && d.cursor < len(d.filtered) {
		cur = d.items[d.filtered[d.cursor]].Value
	}
	d.items = items
	d.loading = false
	d.refilter()
	for i, fi := range d.filtered {
		if d.items[fi].Value == cur {
			d.cursor = i
		}
	}
}

func (d *listDialog) refilter() {
	q := strings.TrimSpace(d.input.Value())
	d.filtered = d.filtered[:0]
	if q == "" {
		for i := range d.items {
			d.filtered = append(d.filtered, i)
		}
	} else {
		keys := make([]string, len(d.items))
		for i, it := range d.items {
			keys[i] = it.Title + " " + it.Value + " " + it.Section
		}
		d.filtered = util.FuzzyFilter(q, keys)
	}
	if d.cursor >= len(d.filtered) {
		d.cursor = max(len(d.filtered)-1, 0)
	}
}

func (d *listDialog) current() (listItem, bool) {
	if len(d.filtered) == 0 {
		return listItem{}, false
	}
	return d.items[d.filtered[d.cursor]], true
}

func (d *listDialog) highlight() tea.Cmd {
	it, ok := d.current()
	if !ok {
		return nil
	}
	id := d.id
	return func() tea.Msg { return dialogHighlightMsg{id: id, item: it} }
}

func (d *listDialog) Update(msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		id := d.id
		return nil, func() tea.Msg { return dialogCancelMsg{id} }
	case "enter":
		it, ok := d.current()
		if !ok {
			return d, nil
		}
		id := d.id
		return nil, func() tea.Msg { return dialogResultMsg{id: id, item: it} }
	case "up", "ctrl+p", "shift+tab":
		if d.cursor > 0 {
			d.cursor--
		} else {
			d.cursor = max(len(d.filtered)-1, 0)
		}
		return d, d.highlight()
	case "down", "ctrl+n", "tab":
		if d.cursor < len(d.filtered)-1 {
			d.cursor++
		} else {
			d.cursor = 0
		}
		return d, d.highlight()
	case "pgup":
		d.cursor = max(d.cursor-8, 0)
		return d, d.highlight()
	case "pgdown":
		d.cursor = min(d.cursor+8, max(len(d.filtered)-1, 0))
		return d, d.highlight()
	}
	if _, ok := d.actions[msg.String()]; ok {
		it, ok := d.current()
		if ok {
			id, k := d.id, msg.String()
			return d, func() tea.Msg { return dialogActionMsg{id: id, key: k, item: it} }
		}
	}
	prev := d.input.Value()
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	if d.input.Value() != prev {
		d.cursor = 0
		d.offset = 0
		d.refilter()
		return d, tea.Batch(cmd, d.highlight())
	}
	return d, cmd
}

func (d *listDialog) View(t theme.Theme, w, h int) string {
	width := min(d.width, w-4)
	inner := width - 4
	bg := t.C(t.Bg)
	base := lipgloss.NewStyle().Background(bg)
	title := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Background(bg).Bold(true).Render(d.title)
	esc := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(bg).Render("esc")
	gap := inner - lipgloss.Width(title) - lipgloss.Width(esc)
	header := title + base.Render(strings.Repeat(" ", max(gap, 1))) + esc

	d.input.Width = inner - 4
	d.input.PromptStyle = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Background(t.C(t.Panel))
	d.input.TextStyle = lipgloss.NewStyle().Foreground(t.C(t.Text)).Background(t.C(t.Panel))
	d.input.PlaceholderStyle = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(t.C(t.Panel))
	d.input.Cursor.Style = lipgloss.NewStyle().Foreground(t.C(t.Primary))
	d.input.Prompt = "⌕ "
	search := lipgloss.NewStyle().Background(t.C(t.Panel)).Width(inner).Padding(0, 1).Render(d.input.View())

	maxRows := max(min(h-10, 14), 3)
	// keep cursor visible
	if d.cursor < d.offset {
		d.offset = d.cursor
	}
	if d.cursor >= d.offset+maxRows {
		d.offset = d.cursor - maxRows + 1
	}
	var rows []string
	lastSection := ""
	if d.offset > 0 && d.offset < len(d.filtered) {
		lastSection = d.items[d.filtered[d.offset]].Section
	}
	shown := 0
	for i := d.offset; i < len(d.filtered) && shown < maxRows; i++ {
		it := d.items[d.filtered[i]]
		if it.Section != "" && it.Section != lastSection && strings.TrimSpace(d.input.Value()) == "" {
			rows = append(rows, base.Width(inner).Render(""))
			rows = append(rows, lipgloss.NewStyle().Foreground(t.C(t.Secondary)).Background(bg).Bold(true).Width(inner).Render(it.Section))
			lastSection = it.Section
		}
		sel := i == d.cursor
		rowBg := bg
		fg := t.C(t.Text)
		if sel {
			rowBg = t.C(t.Primary)
			fg = t.C(t.Bg)
		}
		rs := lipgloss.NewStyle().Background(rowBg)
		mark := "  "
		if it.Current {
			mark = "● "
		}
		left := rs.Foreground(fg).Bold(sel).Render(mark + it.Title)
		if it.Badge != "" {
			left += rs.Render(" ") + rs.Foreground(t.C(t.Accent)).Render(it.Badge)
			if sel {
				left = rs.Foreground(fg).Bold(true).Render(mark+it.Title+" ") + rs.Foreground(fg).Render(it.Badge)
			}
		}
		desc := ""
		if it.Desc != "" {
			dc := t.C(t.Muted)
			if sel {
				dc = t.C(t.Bg)
			}
			avail := inner - lipgloss.Width(left) - 3
			if avail > 6 {
				desc = rs.Foreground(dc).Render(ansi.Truncate(it.Desc, avail, "…"))
			}
		}
		left = ansi.Truncate(left, inner, "…")
		pad := inner - lipgloss.Width(left) - lipgloss.Width(desc)
		rows = append(rows, left+rs.Render(strings.Repeat(" ", max(pad, 1)))+desc)
		shown++
	}
	if len(d.filtered) == 0 {
		msg := "No results"
		if d.loading {
			msg = "Loading…"
		}
		rows = append(rows, lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(bg).Width(inner).Render("  "+msg))
	}
	footer := []string{"↑↓ navigate", "enter select"}
	for k, v := range d.actions {
		footer = append(footer, k+" "+v)
	}
	if d.loading {
		footer = append(footer, "loading models…")
	}
	if d.hint != "" {
		footer = append(footer, d.hint)
	}
	foot := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(bg).Width(inner).Render(strings.Join(footer, " · "))
	body := strings.Join([]string{header, base.Width(inner).Render(""), search, base.Width(inner).Render("")}, "\n") + "\n" +
		strings.Join(rows, "\n") + "\n" + base.Width(inner).Render("") + "\n" + foot
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Primary)).
		BorderBackground(bg).Background(bg).Padding(1, 2).Width(width).Render(body)
}

// ---------- input dialog ----------

type inputDialog struct {
	id      string
	title   string
	prompt  string
	input   textinput.Model
	value   string // extra context carried with the result
}

func newInputDialog(id, title, prompt, placeholder string, secret bool) *inputDialog {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = "› "
	if secret {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	ti.Focus()
	return &inputDialog{id: id, title: title, prompt: prompt, input: ti}
}

func (d *inputDialog) ID() string { return d.id }

func (d *inputDialog) Update(msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		id := d.id
		return nil, func() tea.Msg { return dialogCancelMsg{id} }
	case "enter":
		id, v, ctx := d.id, strings.TrimSpace(d.input.Value()), d.value
		return nil, func() tea.Msg { return dialogResultMsg{id: id, value: v, item: listItem{Value: ctx}} }
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return d, cmd
}

func (d *inputDialog) View(t theme.Theme, w, h int) string {
	width := min(64, w-4)
	inner := width - 4
	bg := t.C(t.Bg)
	base := lipgloss.NewStyle().Background(bg)
	title := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Background(bg).Bold(true).Render(d.title)
	prompt := lipgloss.NewStyle().Foreground(t.C(t.Text)).Background(bg).Width(inner).Render(d.prompt)
	d.input.Width = inner - 4
	d.input.PromptStyle = lipgloss.NewStyle().Foreground(t.C(t.Primary)).Background(t.C(t.Panel))
	d.input.TextStyle = lipgloss.NewStyle().Foreground(t.C(t.Text)).Background(t.C(t.Panel))
	d.input.PlaceholderStyle = lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(t.C(t.Panel))
	field := lipgloss.NewStyle().Background(t.C(t.Panel)).Width(inner).Padding(0, 1).Render(d.input.View())
	foot := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(bg).Width(inner).Render("enter confirm · esc cancel")
	body := strings.Join([]string{title, base.Width(inner).Render(""), prompt, base.Width(inner).Render(""), field, base.Width(inner).Render(""), foot}, "\n")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Primary)).
		BorderBackground(bg).Background(bg).Padding(1, 2).Width(width).Render(body)
}

// ---------- text (read-only) dialog ----------

type textDialog struct {
	id     string
	title  string
	body   string
	scroll int
}

func (d *textDialog) ID() string { return d.id }

func (d *textDialog) Update(msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter", "q", "ctrl+c":
		return nil, nil
	case "up", "k":
		d.scroll = max(d.scroll-1, 0)
	case "down", "j":
		d.scroll++
	case "pgup":
		d.scroll = max(d.scroll-10, 0)
	case "pgdown", " ":
		d.scroll += 10
	}
	return d, nil
}

func (d *textDialog) View(t theme.Theme, w, h int) string {
	width := min(84, w-4)
	inner := width - 4
	bg := t.C(t.Bg)
	base := lipgloss.NewStyle().Background(bg)
	title := lipgloss.NewStyle().Foreground(t.C(t.Primary)).Background(bg).Bold(true).Render(d.title)
	lines := strings.Split(d.body, "\n")
	maxRows := max(h-10, 4)
	if d.scroll > max(len(lines)-maxRows, 0) {
		d.scroll = max(len(lines)-maxRows, 0)
	}
	end := min(d.scroll+maxRows, len(lines))
	var rows []string
	for _, l := range lines[d.scroll:end] {
		rows = append(rows, base.Width(inner).Render(ansi.Truncate(l, inner, "…")))
	}
	hint := "esc close"
	if len(lines) > maxRows {
		hint = "↑↓ scroll · " + hint
	}
	foot := lipgloss.NewStyle().Foreground(t.C(t.Muted)).Background(bg).Width(inner).Render(hint)
	body := title + "\n" + base.Width(inner).Render("") + "\n" + strings.Join(rows, "\n") + "\n" + base.Width(inner).Render("") + "\n" + foot
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.C(t.Primary)).
		BorderBackground(bg).Background(bg).Padding(1, 2).Width(width).Render(body)
}

// overlay draws fg centered on top of bg.
func overlay(bg, fg string, w, h int) string {
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < h {
		bgLines = append(bgLines, "")
	}
	fgLines := strings.Split(fg, "\n")
	fw := lipgloss.Width(fg)
	x := max((w-fw)/2, 0)
	y := max((h-len(fgLines))/2-1, 0)
	for i, fl := range fgLines {
		row := y + i
		if row >= len(bgLines) {
			break
		}
		line := bgLines[row]
		left := ansi.Truncate(line, x, "")
		if lw := lipgloss.Width(left); lw < x {
			left += strings.Repeat(" ", x-lw)
		}
		right := ansi.TruncateLeft(line, x+fw, "")
		bgLines[row] = left + fl + "\x1b[0m" + right
	}
	return strings.Join(bgLines[:h], "\n")
}
