package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/teaspotato/jules/internal/render"
)

type styles struct {
	header   lipgloss.Style
	brand    lipgloss.Style
	selected lipgloss.Style
	section  lipgloss.Style
	border   lipgloss.Style
	focus    lipgloss.Style
	key      lipgloss.Style
	help     lipgloss.Style
}

func newStyles(t *render.Theme) styles {
	r := t.R
	accent := lipgloss.AdaptiveColor{Light: "#5A3FD9", Dark: "#A99BFF"}
	faint := lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#30363D"}
	sel := lipgloss.AdaptiveColor{Light: "#EEEAFF", Dark: "#2A2540"}
	return styles{
		header:   r.NewStyle().Bold(true).Padding(0, 1),
		brand:    r.NewStyle().Bold(true).Foreground(accent),
		selected: r.NewStyle().Background(sel),
		section:  r.NewStyle().Bold(true).Foreground(accent).Padding(0, 1),
		border:   r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).Padding(0, 1),
		focus:    r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1),
		key:      r.NewStyle().Bold(true).Foreground(accent),
		help:     r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 3),
	}
}

func truncate(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// fit pads or cuts s to exactly h lines.
func fit(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Sessions", [][2]string{
		{"j/k ↑/↓", "move"}, {"g/G", "top / bottom"}, {"enter", "open"},
		{"/", "search"}, {"tab", "cycle state filter"}, {"n", "new session"},
		{"a", "approve plan"}, {"o", "open in browser"}, {"p", "open PR"},
		{"r", "refresh"}, {"q", "quit"},
	}},
	{"Session", [][2]string{
		{"j/k ctrl+d/u", "scroll"}, {"g/G", "top / bottom"}, {"i or m", "write a message"},
		{"enter", "send (in composer)"}, {"alt+enter", "newline (in composer)"},
		{"a", "approve plan"}, {"d", "toggle full diffs"}, {"e", "expand prompt & output"},
		{"y", "copy latest patch"}, {"o / p", "open session / PR"}, {"esc / q", "back"},
	}},
	{"New session", [][2]string{
		{"tab / shift+tab", "next / previous field"}, {"↑/↓", "pick repo or branch"},
		{"space", "toggle option"}, {"ctrl+r", "recall a recent prompt"},
		{"ctrl+s", "start session"}, {"esc", "cancel"},
	}},
}

func (m *model) helpView() string {
	var cols []string
	for _, sec := range helpSections {
		var b strings.Builder
		b.WriteString(m.st.brand.Render(sec.title) + "\n")
		for _, k := range sec.keys {
			b.WriteString(m.st.key.Render(padTo(k[0], 16)) + " " + k[1] + "\n")
		}
		cols = append(cols, strings.TrimRight(b.String(), "\n"))
	}
	var body string
	switch {
	case m.width >= 130:
		body = lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "    ", cols[1], "    ", cols[2])
	case m.width >= 88:
		body = lipgloss.JoinHorizontal(lipgloss.Top, cols[0]+"\n\n"+cols[2], "    ", cols[1])
	default:
		body = strings.Join(cols, "\n\n")
	}
	return m.st.help.Render(body + "\n\n" + m.theme.Faint.Render("press any key to close"))
}

func padTo(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
