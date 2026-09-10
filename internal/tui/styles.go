// Package tui draws tram's own data and nothing else.
//
// The boundary this package must not cross: it never renders the contents of a
// remote session. Opening a session means leaving the interface entirely and
// handing the terminal to ssh, then coming back. Everything drawn here is a
// host list, a form, or the output of a command that has already finished.
package tui

import "github.com/charmbracelet/lipgloss"

// Palette uses the terminal's own sixteen colours rather than a fixed set, so
// tram looks like the rest of the terminal in both light and dark themes.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "4", Dark: "12"}
	colMuted  = lipgloss.AdaptiveColor{Light: "8", Dark: "8"}
	colOK     = lipgloss.AdaptiveColor{Light: "2", Dark: "10"}
	colWarn   = lipgloss.AdaptiveColor{Light: "3", Dark: "11"}
	colBad    = lipgloss.AdaptiveColor{Light: "1", Dark: "9"}
)

type styles struct {
	title    lipgloss.Style
	header   lipgloss.Style
	row      lipgloss.Style
	selected lipgloss.Style
	marked   lipgloss.Style
	muted    lipgloss.Style
	ok       lipgloss.Style
	warn     lipgloss.Style
	bad      lipgloss.Style
	help     lipgloss.Style
	label    lipgloss.Style
	box      lipgloss.Style
	prompt   lipgloss.Style
}

func newStyles() styles {
	return styles{
		title:    lipgloss.NewStyle().Bold(true).Foreground(colAccent),
		header:   lipgloss.NewStyle().Bold(true).Foreground(colMuted),
		row:      lipgloss.NewStyle(),
		selected: lipgloss.NewStyle().Bold(true).Foreground(colAccent),
		marked:   lipgloss.NewStyle().Foreground(colWarn),
		muted:    lipgloss.NewStyle().Foreground(colMuted),
		ok:       lipgloss.NewStyle().Foreground(colOK),
		warn:     lipgloss.NewStyle().Foreground(colWarn),
		bad:      lipgloss.NewStyle().Foreground(colBad),
		help:     lipgloss.NewStyle().Foreground(colMuted),
		label:    lipgloss.NewStyle().Foreground(colMuted),
		box:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colMuted).Padding(0, 1),
		prompt:   lipgloss.NewStyle().Foreground(colAccent),
	}
}

// glyphs are the two symbol sets: the pleasant one, and the one that survives a
// console with a code page from 1995.
type glyphs struct {
	star     string
	clock    string
	check    string
	cross    string
	dot      string
	arrow    string
	marked   string
	unmarked string
	drift    string
	plus     string
	opened   string
	closed   string
	vbar     string
}

func newGlyphs(ascii bool) glyphs {
	if ascii {
		return glyphs{star: "*", clock: "~", check: "ok", cross: "!!", dot: "-", arrow: "->", marked: "[x]", unmarked: "[ ]", drift: "*", plus: "+", opened: "-", closed: "+", vbar: "| "}
	}
	return glyphs{star: "★", clock: "🕒", check: "✓", cross: "✗", dot: "·", arrow: "→", marked: "◉", unmarked: "○", drift: "*", plus: "＋", opened: "▾", closed: "▸", vbar: "│ "}
}
