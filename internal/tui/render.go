package tui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/wetakush/solaragent/internal/theme"
)

var (
	mdMu    sync.Mutex
	mdCache = map[string]*glamour.TermRenderer{}
)

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }
func up(u uint) *uint     { return &u }

func mdStyle(t theme.Theme) ansi.StyleConfig {
	var c ansi.StyleConfig
	if t.Dark {
		c = styles.DarkStyleConfig
	} else {
		c = styles.LightStyleConfig
	}
	c.Document.Margin = up(0)
	c.Document.BlockPrefix, c.Document.BlockSuffix = "", ""
	c.Document.Color = sp(t.Text)
	c.Paragraph.Color = sp(t.Text)
	c.Text.Color = sp(t.Text)
	c.Heading.Color = sp(t.Primary)
	c.Heading.Bold = bp(true)
	c.H1.Prefix, c.H1.Suffix = " ", " "
	c.H1.Color = sp(t.Bg)
	c.H1.BackgroundColor = sp(t.Primary)
	c.H2.Prefix = "▌ "
	c.H2.Color = sp(t.Primary)
	c.H3.Prefix = "◆ "
	c.H3.Color = sp(t.Accent)
	c.H4.Prefix = "◇ "
	c.H5.Prefix = ""
	c.H6.Prefix = ""
	c.Strong.Color = sp(t.Text)
	c.Strong.Bold = bp(true)
	c.Emph.Color = sp(t.Text)
	c.Link.Color = sp(t.Secondary)
	c.Link.Underline = bp(true)
	c.LinkText.Color = sp(t.Accent)
	c.LinkText.Bold = bp(true)
	c.Code.Color = sp(t.Accent)
	c.Code.BackgroundColor = sp(t.Panel)
	c.Code.Prefix, c.Code.Suffix = " ", " "
	c.CodeBlock.Chroma = nil
	c.CodeBlock.Theme = t.CodeTheme
	c.CodeBlock.Margin = up(1)
	c.Item.BlockPrefix = "• "
	c.Item.Color = sp(t.Text)
	c.Enumeration.Color = sp(t.Primary)
	c.Enumeration.BlockPrefix = ". "
	c.BlockQuote.Color = sp(t.Muted)
	c.BlockQuote.Italic = bp(true)
	c.BlockQuote.IndentToken = sp("┃ ")
	c.HorizontalRule.Color = sp(t.Subtle)
	c.HorizontalRule.Format = "\n" + strings.Repeat("─", 40) + "\n"
	c.Table.Color = sp(t.Text)
	c.Task.Ticked = "☑ "
	c.Task.Unticked = "☐ "
	return c
}

func markdown(t theme.Theme, text string, width int) string {
	if width < 20 {
		width = 20
	}
	key := fmt.Sprintf("%s:%d", t.Name, width)
	mdMu.Lock()
	r, ok := mdCache[key]
	if !ok {
		var err error
		r, err = glamour.NewTermRenderer(glamour.WithStyles(mdStyle(t)), glamour.WithWordWrap(width),
			glamour.WithPreservedNewLines(), glamour.WithEmoji())
		if err != nil {
			mdMu.Unlock()
			return lipgloss.NewStyle().Width(width).Render(text)
		}
		mdCache[key] = r
	}
	out, err := r.Render(text)
	mdMu.Unlock()
	if err != nil {
		return lipgloss.NewStyle().Width(width).Render(text)
	}
	return strings.Trim(out, "\n")
}

// ---- logo ----

var logoLines = []string{
	"███████╗ ██████╗ ██╗      █████╗ ██████╗ ",
	"██╔════╝██╔═══██╗██║     ██╔══██╗██╔══██╗",
	"███████╗██║   ██║██║     ███████║██████╔╝",
	"╚════██║██║   ██║██║     ██╔══██║██╔══██╗",
	"███████║╚██████╔╝███████╗██║  ██║██║  ██║",
	"╚══════╝ ╚═════╝ ╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝",
}

var logoSmall = []string{
	"▄▀▀ ▄▀▄ █   ▄▀▄ █▀▄",
	"▄██ ▀▄▀ █▄▄ █▀█ █▀▄",
}

func logo(t theme.Theme, width int) string {
	src := logoLines
	if width < 46 {
		src = logoSmall
	}
	return strings.Join(t.GradientLines(src), "\n")
}
