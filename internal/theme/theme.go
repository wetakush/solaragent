// Package theme defines color palettes for the TUI.
package theme

import (
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

type Theme struct {
	Name      string
	Dark      bool
	Primary   string // brand / build agent
	Secondary string // plan agent
	Accent    string
	Text      string
	Muted     string
	Subtle    string // faint borders, separators
	Panel     string // raised background (user messages, dialogs)
	Bg        string // dialog background
	Border    string
	Success   string
	Error     string
	Warning   string
	Info      string
	DiffAdd   string
	DiffDel   string
	DiffAddBg string
	DiffDelBg string
	Gradient  []string
	CodeTheme string // chroma style for code blocks
}

func (t Theme) C(hex string) lipgloss.Color { return lipgloss.Color(hex) }

var themes = map[string]Theme{
	"solar": {Name: "solar", Dark: true,
		Primary: "#FFB224", Secondary: "#7DD3FC", Accent: "#FF6B3D", Text: "#ECE7DD", Muted: "#8A8378", Subtle: "#3A342C",
		Panel: "#1E1A15", Bg: "#15120E", Border: "#4A4136", Success: "#9BD36A", Error: "#FF5F5F", Warning: "#FFD166", Info: "#7DD3FC",
		DiffAdd: "#9BD36A", DiffDel: "#FF7A7A", DiffAddBg: "#1F2B17", DiffDelBg: "#2E1717",
		Gradient: []string{"#FFE29A", "#FFB224", "#FF7A2F", "#F2434E", "#C2337A"}, CodeTheme: "monokai"},
	"tokyonight": {Name: "tokyonight", Dark: true,
		Primary: "#7AA2F7", Secondary: "#BB9AF7", Accent: "#FF9E64", Text: "#C0CAF5", Muted: "#737AA2", Subtle: "#292E42",
		Panel: "#1F2335", Bg: "#16161E", Border: "#3B4261", Success: "#9ECE6A", Error: "#F7768E", Warning: "#E0AF68", Info: "#7DCFFF",
		DiffAdd: "#9ECE6A", DiffDel: "#F7768E", DiffAddBg: "#1D2B23", DiffDelBg: "#2D1F2A",
		Gradient: []string{"#7DCFFF", "#7AA2F7", "#BB9AF7", "#F7768E"}, CodeTheme: "dracula"},
	"catppuccin": {Name: "catppuccin", Dark: true,
		Primary: "#CBA6F7", Secondary: "#89B4FA", Accent: "#F5C2E7", Text: "#CDD6F4", Muted: "#7F849C", Subtle: "#313244",
		Panel: "#1E1E2E", Bg: "#181825", Border: "#45475A", Success: "#A6E3A1", Error: "#F38BA8", Warning: "#F9E2AF", Info: "#89DCEB",
		DiffAdd: "#A6E3A1", DiffDel: "#F38BA8", DiffAddBg: "#1E2B24", DiffDelBg: "#2E1E28",
		Gradient: []string{"#F5E0DC", "#F5C2E7", "#CBA6F7", "#89B4FA"}, CodeTheme: "catppuccin-mocha"},
	"gruvbox": {Name: "gruvbox", Dark: true,
		Primary: "#FABD2F", Secondary: "#83A598", Accent: "#FE8019", Text: "#EBDBB2", Muted: "#928374", Subtle: "#3C3836",
		Panel: "#282828", Bg: "#1D2021", Border: "#504945", Success: "#B8BB26", Error: "#FB4934", Warning: "#FABD2F", Info: "#8EC07C",
		DiffAdd: "#B8BB26", DiffDel: "#FB4934", DiffAddBg: "#2A2E17", DiffDelBg: "#3A1E1A",
		Gradient: []string{"#FABD2F", "#FE8019", "#FB4934", "#D3869B"}, CodeTheme: "gruvbox"},
	"nord": {Name: "nord", Dark: true,
		Primary: "#88C0D0", Secondary: "#B48EAD", Accent: "#81A1C1", Text: "#ECEFF4", Muted: "#7B88A1", Subtle: "#3B4252",
		Panel: "#2E3440", Bg: "#272C36", Border: "#4C566A", Success: "#A3BE8C", Error: "#BF616A", Warning: "#EBCB8B", Info: "#8FBCBB",
		DiffAdd: "#A3BE8C", DiffDel: "#BF616A", DiffAddBg: "#2C3A30", DiffDelBg: "#3B2A30",
		Gradient: []string{"#8FBCBB", "#88C0D0", "#81A1C1", "#5E81AC"}, CodeTheme: "nord"},
	"dracula": {Name: "dracula", Dark: true,
		Primary: "#BD93F9", Secondary: "#8BE9FD", Accent: "#FF79C6", Text: "#F8F8F2", Muted: "#7D88B6", Subtle: "#343746",
		Panel: "#282A36", Bg: "#21222C", Border: "#44475A", Success: "#50FA7B", Error: "#FF5555", Warning: "#F1FA8C", Info: "#8BE9FD",
		DiffAdd: "#50FA7B", DiffDel: "#FF5555", DiffAddBg: "#1F3326", DiffDelBg: "#3A2026",
		Gradient: []string{"#8BE9FD", "#BD93F9", "#FF79C6", "#FFB86C"}, CodeTheme: "dracula"},
	"rosepine": {Name: "rosepine", Dark: true,
		Primary: "#EBBCBA", Secondary: "#C4A7E7", Accent: "#F6C177", Text: "#E0DEF4", Muted: "#908CAA", Subtle: "#26233A",
		Panel: "#1F1D2E", Bg: "#191724", Border: "#403D52", Success: "#9CCFD8", Error: "#EB6F92", Warning: "#F6C177", Info: "#31748F",
		DiffAdd: "#9CCFD8", DiffDel: "#EB6F92", DiffAddBg: "#1D2B33", DiffDelBg: "#2F1D2A",
		Gradient: []string{"#F6C177", "#EBBCBA", "#EB6F92", "#C4A7E7"}, CodeTheme: "rose-pine"},
	"everforest": {Name: "everforest", Dark: true,
		Primary: "#A7C080", Secondary: "#7FBBB3", Accent: "#DBBC7F", Text: "#D3C6AA", Muted: "#859289", Subtle: "#374145",
		Panel: "#2D353B", Bg: "#272E33", Border: "#475258", Success: "#A7C080", Error: "#E67E80", Warning: "#DBBC7F", Info: "#7FBBB3",
		DiffAdd: "#A7C080", DiffDel: "#E67E80", DiffAddBg: "#2E3B2C", DiffDelBg: "#3D2C2E",
		Gradient: []string{"#DBBC7F", "#A7C080", "#83C092", "#7FBBB3"}, CodeTheme: "monokai"},
	"onedark": {Name: "onedark", Dark: true,
		Primary: "#61AFEF", Secondary: "#C678DD", Accent: "#E5C07B", Text: "#ABB2BF", Muted: "#717A8A", Subtle: "#2C313C",
		Panel: "#21252B", Bg: "#1B1F24", Border: "#3E4451", Success: "#98C379", Error: "#E06C75", Warning: "#E5C07B", Info: "#56B6C2",
		DiffAdd: "#98C379", DiffDel: "#E06C75", DiffAddBg: "#22301F", DiffDelBg: "#33201F",
		Gradient: []string{"#56B6C2", "#61AFEF", "#C678DD", "#E06C75"}, CodeTheme: "onedark"},
	"matrix": {Name: "matrix", Dark: true,
		Primary: "#3DFF6E", Secondary: "#00D9FF", Accent: "#B6FF3D", Text: "#C6F7D0", Muted: "#4E8A5E", Subtle: "#14301C",
		Panel: "#0B1A10", Bg: "#07120B", Border: "#1F4A2C", Success: "#3DFF6E", Error: "#FF4D4D", Warning: "#E6FF3D", Info: "#00D9FF",
		DiffAdd: "#3DFF6E", DiffDel: "#FF4D4D", DiffAddBg: "#0F2A16", DiffDelBg: "#2A0F0F",
		Gradient: []string{"#B6FF3D", "#3DFF6E", "#00C46A", "#00D9FF"}, CodeTheme: "monokai"},
	"daylight": {Name: "daylight", Dark: false,
		Primary: "#D97706", Secondary: "#2563EB", Accent: "#DB2777", Text: "#1C1917", Muted: "#78716C", Subtle: "#E7E5E4",
		Panel: "#F5F5F4", Bg: "#FAFAF9", Border: "#D6D3D1", Success: "#16A34A", Error: "#DC2626", Warning: "#CA8A04", Info: "#0284C7",
		DiffAdd: "#15803D", DiffDel: "#B91C1C", DiffAddBg: "#DCFCE7", DiffDelBg: "#FEE2E2",
		Gradient: []string{"#F59E0B", "#EA580C", "#DC2626", "#BE185D"}, CodeTheme: "github"},
}

func Get(name string) Theme {
	if t, ok := themes[name]; ok {
		return t
	}
	return themes["solar"]
}

func Names() []string {
	var n []string
	for k := range themes {
		n = append(n, k)
	}
	sort.Strings(n)
	// keep the signature theme first
	for i, x := range n {
		if x == "solar" {
			n = append([]string{"solar"}, append(n[:i:i], n[i+1:]...)...)
			break
		}
	}
	return n
}

// Blend returns the gradient color at position t in [0,1].
func (t Theme) Blend(pos float64) lipgloss.Color {
	stops := t.Gradient
	if len(stops) == 0 {
		return lipgloss.Color(t.Primary)
	}
	if len(stops) == 1 || pos <= 0 {
		return lipgloss.Color(stops[0])
	}
	if pos >= 1 {
		return lipgloss.Color(stops[len(stops)-1])
	}
	seg := pos * float64(len(stops)-1)
	i := int(seg)
	f := seg - float64(i)
	a, _ := colorful.Hex(stops[i])
	b, _ := colorful.Hex(stops[i+1])
	return lipgloss.Color(a.BlendLuv(b, f).Clamped().Hex())
}

// Gradient paints each rune of s along the theme gradient, shifted by offset.
func (t Theme) GradientText(s string, offset float64, bold bool) string {
	rs := []rune(s)
	if len(rs) == 0 {
		return ""
	}
	out := ""
	for i, r := range rs {
		if r == ' ' {
			out += " "
			continue
		}
		p := float64(i)/float64(max(len(rs)-1, 1)) + offset
		p = p - float64(int(p))
		// bounce so the gradient loops smoothly
		if p > 0.5 {
			p = 1 - p
		}
		p *= 2
		st := lipgloss.NewStyle().Foreground(t.Blend(p))
		if bold {
			st = st.Bold(true)
		}
		out += st.Render(string(r))
	}
	return out
}

// GradientLines paints a multi-line block horizontally (static, no wrap).
func (t Theme) GradientLines(lines []string) []string {
	w := 0
	for _, l := range lines {
		w = max(w, len([]rune(l)))
	}
	out := make([]string, len(lines))
	for li, l := range lines {
		s := ""
		for i, r := range []rune(l) {
			if r == ' ' {
				s += " "
				continue
			}
			p := float64(i)/float64(max(w-1, 1))*0.85 + float64(li)*0.03
			s += lipgloss.NewStyle().Foreground(t.Blend(p)).Render(string(r))
		}
		out[li] = s
	}
	return out
}
