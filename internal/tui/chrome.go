package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/hieuny/tram/internal/store"
	"github.com/mattn/go-runewidth"
)

// The chrome is the frame every screen is drawn in: a bar naming the program
// and the file, a rule, the panes, a rule, and a bar of keys with a status line
// on the right. The design has no boxes around the panes, only a hairline
// between them, so neither does this.

// Version is what the top bar prints beside the name. The command layer sets it,
// because that is where the build stamps it; anything else would be a second
// version number to keep in step with the first.
var Version = "dev"

// versionLabel writes the version the way the design does, with one v.
func versionLabel() string {
	if strings.HasPrefix(Version, "v") {
		return Version
	}
	return "v" + Version
}

// shell assembles a screen from the four pieces.
func (m *Model) shell(body string, keys [][2]string, status string) string {
	return m.topBar() + "\n" +
		m.st.rule.Render(strings.Repeat(m.gl.hline, m.width)) + "\n" +
		body + "\n" +
		m.st.rule.Render(strings.Repeat(m.gl.hline, m.width)) + "\n" +
		m.keyBar(keys, status)
}

// bodyHeight is the room between the two rules.
func (m *Model) bodyHeight() int {
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	return h
}

// ---- the top bar ----------------------------------------------------------

// topBar names the program, says which view is showing, and reports what the
// file holds.
//
// When the window is too narrow for all of it, the readings on the right are
// dropped one at a time, widest and least urgent first. The tabs are never cut:
// a half-drawn word where the navigation should be looks like a bug, and the
// path to a config file is not worth that.
func (m *Model) topBar() string {
	left := " " + m.st.ok.Render(m.gl.dot) + " " + m.st.brand.Render("TRAM") + " " + m.st.version.Render(versionLabel())

	tabs := ""
	at := lipgloss.Width(left) + 2
	for _, t := range allTabs {
		label := " " + t.title() + " "
		if t == m.tab {
			tabs += m.st.tabOn.Render(label)
		} else {
			tabs += m.st.tabOff.Render(label)
		}
		to := t
		m.hitAt(at, 0, runewidth.StringWidth(label), func() (tea.Model, tea.Cmd) {
			m.setTab(to)
			return m, nil
		})
		at += runewidth.StringWidth(label)
	}

	// The filter box sits between the tabs and the readings, as in the design,
	// and is only there when there is something to say about it.
	filter := ""
	switch {
	case m.mode == modeSearch:
		filter = "  " + m.st.ok.Render("/") + " " + m.search.View()
	case m.searchQuery != "":
		filter = "  " + m.st.ok.Render("/") + " " + m.st.value.Render(m.searchQuery)
	}

	// Ordered so that dropping from the front loses the least first.
	parts := []string{
		m.st.label.Render("config") + " " + m.st.value.Render(shortenPath(m.inv.Config.Root.Path)),
		m.st.label.Render("shown") + " " + m.st.value.Render(strconv.Itoa(len(m.filtered))) +
			m.st.label.Render("/"+strconv.Itoa(len(m.hosts))),
	}
	if len(m.marked) > 0 {
		parts = append(parts, m.st.label.Render("marked")+" "+m.st.warn.Render(strconv.Itoa(len(m.marked))))
	}
	if m.agent != "" {
		dot, style := m.st.ok, m.st.value
		if strings.HasPrefix(m.agent, "no ") {
			dot, style = m.st.faint, m.st.faint
		}
		parts = append(parts, dot.Render(m.gl.dot)+" "+style.Render(m.agent))
	}
	if m.inv.Managed == nil {
		parts = append(parts, m.st.warn.Render("no managed file; run tram init"))
	}

	head := left + "  " + tabs + filter
	for start := 0; start < len(parts); start++ {
		meta := strings.Join(parts[start:], "   ")
		if lipgloss.Width(head)+lipgloss.Width(meta)+3 <= m.width {
			return m.spread(head, meta)
		}
	}
	return ansiTruncate(head, m.width)
}

// spread puts one string against the left edge and another against the right,
// and drops the right one when the line is too narrow to hold both.
func (m *Model) spread(left, right string) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+2 > m.width {
		if rw+2 <= m.width {
			return ansiTruncate(left, m.width-rw-2) + "  " + right
		}
		return ansiTruncate(left, m.width)
	}
	return left + strings.Repeat(" ", m.width-lw-rw-1) + right + " "
}

// ---- the key bar ----------------------------------------------------------

// keyBar draws the keys as chips, with the status line against the right edge.
// Chips that do not fit are dropped rather than wrapped, and the last one is
// kept whatever happens: it is the one that gets you out.
func (m *Model) keyBar(pairs [][2]string, status string) string {
	if len(pairs) == 0 {
		return m.spread("", m.st.label.Render(status))
	}
	chip := func(p [2]string, first bool) string {
		st := m.st.chip
		if first {
			st = m.st.chipOn
		}
		return " " + m.st.key.Render(" "+p[0]+" ") + st.Render(" "+p[1]+" ")
	}
	cost := func(p [2]string) int {
		return runewidth.StringWidth(p[0]) + runewidth.StringWidth(p[1]) + 5
	}

	statusW := lipgloss.Width(status) + 3
	room := m.width - statusW
	if room < m.width/2 {
		room = m.width / 2
	}

	last := pairs[len(pairs)-1]
	var b strings.Builder
	w := cost(last)
	at, y := 0, m.height-1
	press := func(p [2]string, x int) {
		k := p[0]
		if k == m.gl.enter {
			k = "enter"
		}
		m.hitAt(x+1, y, cost(p)-1, func() (tea.Model, tea.Cmd) {
			if m.mode != modeNormal || m.screen != screenList {
				return m, nil
			}
			return m.updateList(keyMsg(k))
		})
	}
	for i, p := range pairs[:len(pairs)-1] {
		if w+cost(p) > room {
			break
		}
		b.WriteString(chip(p, i == 0))
		press(p, at)
		at += cost(p)
		w += cost(p)
	}
	b.WriteString(chip(last, len(pairs) == 1))
	press(last, at)
	return m.spread(b.String(), m.st.label.Render(status))
}

// ---- panes ----------------------------------------------------------------

// pane is one column of the body: a stack of lines, each padded to its width so
// that the hairline beside it stays straight.
func (m *Model) pane(lines []string, w, h int) string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out[i] = ansiPad(ansiTruncate(line, w), w)
	}
	return strings.Join(out, "\n")
}

// joinPanes sets the panes side by side with a hairline between them.
func (m *Model) joinPanes(height int, panes ...string) string {
	var blocks []string
	sep := make([]string, height)
	for i := range sep {
		sep[i] = m.st.rule.Render(m.gl.vline)
	}
	rule := strings.Join(sep, "\n")

	for i, p := range panes {
		if i > 0 {
			blocks = append(blocks, rule)
		}
		blocks = append(blocks, p)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

// hrule is the line the design draws under a section heading.
func (m *Model) hrule(w int) string { return m.st.rule.Render(strings.Repeat(m.gl.hline, max(0, w))) }

// heading is a section title: dim and upper case. The design letterspaces these;
// a terminal cell is already as wide as a letter, so spacing them out here reads
// as a gap rather than as emphasis.
func (m *Model) heading(s string) string {
	return m.st.section.Render(strings.ToUpper(s))
}

// ---- sparklines -----------------------------------------------------------

// sparkline draws one block per value, coloured by the caller. It is the only
// chart tram draws, and it draws nothing at all rather than a flat line when
// there is nothing measured.
func (m *Model) sparkline(values []float64, colours []lipgloss.Style, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
		if len(colours) > width {
			colours = colours[len(colours)-width:]
		}
	}
	var b strings.Builder
	for i, v := range values {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		idx := int(v * float64(len(m.gl.bars)-1))
		g := m.gl.bars[idx]
		if i < len(colours) {
			b.WriteString(colours[i].Render(g))
		} else {
			b.WriteString(g)
		}
	}
	return b.String()
}

// sampleBars turns measurements into a sparkline: height by round trip against
// the slow threshold, colour by whether the host answered at all.
func (m *Model) sampleBars(samples []store.Sample, width int) string {
	vals := make([]float64, 0, len(samples))
	cols := make([]lipgloss.Style, 0, len(samples))
	for _, s := range samples {
		switch {
		case !s.OK:
			vals = append(vals, 1)
			cols = append(cols, m.st.bad)
		case s.Millis >= store.SlowMillis:
			vals = append(vals, 0.8)
			cols = append(cols, m.st.warn)
		default:
			// A fast host is a short bar, so a row of stubs reads as health.
			vals = append(vals, 0.15+0.5*float64(s.Millis)/float64(store.SlowMillis))
			cols = append(cols, m.st.ok)
		}
	}
	return m.sparkline(vals, cols, width)
}

// ---- small helpers --------------------------------------------------------

// ago renders a moment the way the design's LAST SEEN column does.
func ago(unix int64) string {
	if unix <= 0 {
		return ""
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}

// ansiPad widens a line that may already carry colour to a visible width.
// lipgloss.Width counts what the eye sees, which is the only count that lines
// two panes up.
func ansiPad(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// ansiTruncate cuts a coloured line to a visible width. It walks the string
// escape by escape, because cutting in the middle of one leaves the rest of the
// terminal wearing the colour.
func ansiTruncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	width := 0
	for i := 0; i < len(s); {
		if s[i] == 27 {
			j := i
			for j < len(s) && !isANSIFinal(s[j]) {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if width+rw > w {
			break
		}
		b.WriteString(s[i : i+size])
		width += rw
		i += size
	}
	// Whatever colour was in force is dropped at the cut.
	b.WriteString("\x1b[0m")
	return b.String()
}

func isANSIFinal(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tildePath writes a path the way the user would say it. The bar has room for
// one long thing, and it is more useful for that to be the file name than the
// road to the home directory.
func tildePath(p string) string {
	homeOnce.Do(func() {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	})
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

var (
	home     string
	homeOnce sync.Once
)
