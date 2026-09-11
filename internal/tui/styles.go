// Package tui draws tram's own data and nothing else.
//
// The boundary this package must not cross: it never renders the contents of a
// remote session. Opening a session means leaving the interface entirely and
// handing the terminal to ssh, then coming back. Everything drawn here is a
// host list, a form, or the output of a command that has already finished.
package tui

import "github.com/charmbracelet/lipgloss"

// The palette is taken from the SSHFleet Console design in giaodien/, hex for
// hex. On a terminal that cannot show sixteen million colours the layout
// library converts each one to the nearest the terminal has, so naming them
// exactly costs nothing on an old console and is exact on a new one.
//
// One thing the design does that this does not: paint the page background.
// tram draws on the terminal's own background, so a transparent or themed
// terminal keeps looking like itself. Only the selected row and the chips carry
// a background of their own, as they do in the design.
const (
	hexAccent = "#7ecfc0" // teal: reachable, selected, and every key
	hexGold   = "#d9a441" // marked, and slow
	hexRed    = "#e0665f" // unreachable
	hexWhite  = "#ffffff" // the alias of the row under the cursor
	hexBright = "#e6edf5" // headings and names
	hexText   = "#dfe7ef"
	hexNormal = "#c6d0db" // values
	hexDim    = "#8b96a4" // secondary text
	hexFaint  = "#5c6775" // ports, dates
	hexLabel  = "#4d5866" // the label column in the details pane
	hexGhost  = "#48525f" // section headings
	hexLine   = "#2b3540" // rules and pane separators
	hexRowBG  = "#141c24" // the selected row
	hexChipBG = "#10151b" // a key chip
	hexKeyBG  = "#0a0d12" // the key inside a chip
	hexTabBG  = "#1a2129" // the tab that is showing
	hexGoBG   = "#13282a" // the chip for the action enter performs
	hexOff    = "#333c47" // an unmarked box, an inactive dot
)

var (
	colAccent = lipgloss.Color(hexAccent)
	colGold   = lipgloss.Color(hexGold)
	colRed    = lipgloss.Color(hexRed)
	colBright = lipgloss.Color(hexBright)
	colNormal = lipgloss.Color(hexNormal)
	colDim    = lipgloss.Color(hexDim)
	colFaint  = lipgloss.Color(hexFaint)
	colGhost  = lipgloss.Color(hexGhost)
	colLine   = lipgloss.Color(hexLine)
)

type styles struct {
	// The frame: rules, section headings, the two bars.
	rule    lipgloss.Style
	section lipgloss.Style
	brand   lipgloss.Style
	version lipgloss.Style
	tabOn   lipgloss.Style
	tabOff  lipgloss.Style
	key     lipgloss.Style
	chip    lipgloss.Style
	chipOn  lipgloss.Style

	// The data.
	row      lipgloss.Style
	selected lipgloss.Style
	rowBar   lipgloss.Style
	dim      lipgloss.Style
	faint    lipgloss.Style
	label    lipgloss.Style
	value    lipgloss.Style
	marked   lipgloss.Style
	unmarked lipgloss.Style
	tag      lipgloss.Style
	bright   lipgloss.Style

	// Health.
	ok   lipgloss.Style
	warn lipgloss.Style
	bad  lipgloss.Style

	// The forms and the result screen are not part of what the design
	// describes, so they keep drawing with these.
	muted lipgloss.Style
	help  lipgloss.Style
}

func newStyles() styles {
	base := lipgloss.NewStyle()
	return styles{
		rule:    base.Foreground(colLine),
		section: base.Foreground(colGhost),
		brand:   base.Bold(true).Foreground(colBright),
		version: base.Foreground(lipgloss.Color(hexLabel)),
		tabOn:   base.Bold(true).Foreground(colBright).Background(lipgloss.Color(hexTabBG)),
		tabOff:  base.Foreground(colFaint),
		key:     base.Foreground(colAccent).Background(lipgloss.Color(hexKeyBG)),
		chip:    base.Foreground(colDim).Background(lipgloss.Color(hexChipBG)),
		chipOn:  base.Bold(true).Foreground(colAccent).Background(lipgloss.Color(hexGoBG)),

		row:      base.Foreground(lipgloss.Color(hexText)),
		selected: base.Bold(true).Foreground(lipgloss.Color(hexWhite)).Background(lipgloss.Color(hexRowBG)),
		rowBar:   base.Foreground(colAccent).Background(lipgloss.Color(hexRowBG)),
		dim:      base.Foreground(colDim),
		faint:    base.Foreground(colFaint),
		label:    base.Foreground(lipgloss.Color(hexLabel)),
		value:    base.Foreground(colNormal),
		marked:   base.Foreground(colGold),
		unmarked: base.Foreground(lipgloss.Color(hexOff)),
		tag:      base.Foreground(colDim).Background(lipgloss.Color(hexChipBG)),
		bright:   base.Bold(true).Foreground(colBright),

		ok:   base.Foreground(colAccent),
		warn: base.Foreground(colGold),
		bad:  base.Foreground(colRed),

		muted: base.Foreground(colDim),
		help:  base.Foreground(colDim),
	}
}

// onRow paints a style onto the selected row's band, so that a coloured cell
// does not punch a hole in it.
func (s styles) onRow(st lipgloss.Style, selected bool) lipgloss.Style {
	if selected {
		return st.Background(lipgloss.Color(hexRowBG))
	}
	return st
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
	point    string
	crumb    string
	enter    string
	more     string
	up       string
	down     string

	// bar is the left edge of the selected row, vline the rule between panes,
	// hline the rule between sections.
	bar   string
	vline string
	hline string

	// bars are the eight heights a sparkline is drawn with, shortest first.
	bars []string
}

func newGlyphs(ascii bool) glyphs {
	if ascii {
		return glyphs{
			star: "*", clock: "~", check: "ok", cross: "!!", dot: "-", arrow: "->",
			marked: "[x]", unmarked: "[ ]", drift: "*", plus: "+",
			opened: "-", closed: "+", point: ">", crumb: ">", enter: "enter", more: "...",
			up: "^", down: "v",
			bar: "|", vline: "|", hline: "-",
			bars: []string{".", ".", ":", ":", "|", "|", "#", "#"},
		}
	}
	return glyphs{
		star: "★", clock: "🕒", check: "✓", cross: "✗", dot: "·", arrow: "→",
		marked: "◼", unmarked: "◻", drift: "*", plus: "＋",
		opened: "▾", closed: "▸", point: "▸", crumb: "›", enter: "⏎", more: "···",
		up: "↑", down: "↓",
		bar: "▌", vline: "│", hline: "─",
		bars: []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"},
	}
}
