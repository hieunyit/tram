package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/store"
	"github.com/mattn/go-runewidth"
)

// The details pane, section for section as the design has it: who the host is,
// how you reach it, whether it has been answering, and what has happened to it
// lately.
//
// Every number in here was measured. A host nobody has probed shows dashes and
// says so, rather than drawing a zero that reads like a reading.

// detailPaneLines draws the right-hand pane, section by section as the design
// has it.
//
// Sections are added whole or not at all. A heading with its contents cut off
// below it reads as a bug, so when the window is short the last sections are
// left out and the row of actions stays pinned to the bottom, where it is.
func (m *Model) detailPaneLines(height, w, x, y int) []string {
	if m.tab == tabKeys {
		return m.keyDetailLines(height, w)
	}
	h, ok := m.current()
	if !ok {
		return []string{" " + m.st.dim.Render("nothing selected")}
	}
	f := m.fact(h.Name)

	// One line is kept back for the actions and one for the rule above them.
	room := height - 2

	var out []string
	add := func(lines ...string) {
		for _, l := range lines {
			out = append(out, " "+l)
		}
	}
	// fits reports whether a whole section still has somewhere to go.
	fits := func(n int) bool { return len(out)+n <= room }

	label := func(k, v string, style func(string) string) string {
		if v == "" {
			v = "-"
		}
		return m.st.label.Render(pad(k, 11)) + " " + style(pad(v, max(1, w-12)))
	}
	plain := func(s string) string { return m.st.value.Render(s) }

	// Who it is.
	title := m.healthStyle(healthOf(f)).Render(m.gl.dot) + " " + m.st.bright.Render(h.Name)
	if h.Favorite {
		title += " " + m.st.warn.Render(m.gl.star)
	}
	add(title, m.st.faint.Render(pad(userHost(h)+":"+h.PortOr(), w)), m.hrule(w))

	// How you reach it.
	//
	// A field with nothing in it is drawn only when its absence changes what
	// ssh does: no user means your local login, no jump means straight there.
	// The rest are left out, because a column of dashes is a row of the pane
	// each and the pane is twenty-two rows tall.
	var conn []string
	conn = append(conn,
		label("address", h.Addr(), plain),
		label("user", h.User, plain),
		label("auth", authOf(h), func(s string) string { return m.st.ok.Render(s) }),
		label("proxyjump", h.ProxyJump, plain))
	if len(h.IdentityFiles) > 0 {
		conn = append(conn, label("identity", shortenPath(strings.Join(h.IdentityFiles, ", ")), plain))
	}
	if h.Group != "" {
		conn = append(conn, label("group", h.Group, plain))
	}
	if len(h.Tags) > 0 {
		conn = append(conn, label("tags", strings.Join(h.Tags, " "), func(s string) string { return m.st.tag.Render(s) }))
	}
	if h.Account != "" {
		v := h.Account
		if h.Drift {
			v += " (edited by hand)"
		}
		conn = append(conn, label("account", v, plain))
	}
	if h.Desc != "" {
		conn = append(conn, label("desc", h.Desc, plain))
	}
	// Which file and which line. With ssh_config split across Include files,
	// this is the difference between editing a host and hunting for it.
	conn = append(conn, label("source", fmt.Sprintf("%s:%d", shortenPath(h.File), h.Line), func(s string) string {
		if h.ReadOnly {
			return m.st.faint.Render(s)
		}
		return m.st.faint.Render(s)
	}))
	// What breaks if this one does. One line rather than a section: it belongs
	// with the rest of what the file says about this host, and for a jump
	// station it is the most important line on the screen.
	if deps := model.Names(model.Dependents(h.Name, m.hosts)); len(deps) > 0 {
		list := strings.Join(deps, ", ")
		if len(deps) > 3 {
			list = fmt.Sprintf("%s +%d", strings.Join(deps[:3], ", "), len(deps)-3)
		}
		conn = append(conn, label("used by", list, func(s string) string { return m.st.warn.Render(s) }))
	}
	if chain := m.inv.Chain(h.Name); len(chain.Cycle) > 0 {
		conn = append(conn, m.st.bad.Render(pad("loop: "+strings.Join(chain.Cycle, " "+m.gl.arrow+" "), w)))
	}
	add(m.heading("connection"))
	add(conn...)

	tiles := m.tiles(w, f)

	// Why it can or cannot be reached. This is a reading of the last probe
	// rather than a chart of many: tram measures when you ask it to, so a
	// series over time is a series of two or three points, and what actually
	// helps is the last answer said in words.
	// The readings are dropped before the diagnosis is: a number about a
	// machine is worth less than the sentence saying whether you can reach it.
	diag := m.diagnosis(h, f, w)
	switch {
	case fits(len(diag) + len(tiles) + 3):
		add(m.hrule(w))
		add(m.spreadIn(w, m.heading("diagnosis"), m.st.faint.Render(measuredAgo(f))))
		add(diag...)
		add("")
		add(tiles...)
	case fits(len(diag) + 2):
		add(m.hrule(w))
		add(m.spreadIn(w, m.heading("diagnosis"), m.st.faint.Render(measuredAgo(f))))
		add(diag...)
	}

	// What you can do about it, pinned to the foot of the pane.
	for len(out) < room {
		out = append(out, "")
	}
	if len(out) > room {
		out = out[:room]
	}
	add(m.hrule(w), m.actionChips(w, x, y+len(out)+1))
	return out
}

// diagnosis is the last probe said in words: what happened, what ssh itself
// wrote about it, and where along the route it happened.
func (m *Model) diagnosis(h model.Host, f store.Fact, w int) []string {
	if !f.Measured() {
		return []string{
			m.st.faint.Render(pad("not measured yet", w)),
			m.st.faint.Render(pad("press p to measure this host, P for all of them", w)),
		}
	}

	var out []string
	head := m.healthStyle(healthOf(f)).Render(strings.ToLower(f.Class))
	if f.Reachable() {
		head += m.st.faint.Render("  "+m.gl.dot+"  ") + m.st.value.Render(fmt.Sprintf("%d ms", f.Millis))
		if f.Slow() {
			head += m.st.warn.Render("  (slow)")
		}
	}
	out = append(out, head)

	// What the failure means, then the line ssh wrote. The explanation first,
	// because it is the part that says where to go and look.
	if f.Explain != "" {
		out = append(out, wrapTo(f.Explain, w, 2, m.st.dim)...)
	}
	if f.Detail != "" {
		out = append(out, wrapTo(f.Detail, w, 2, m.st.faint)...)
	}

	// Every hop of the route carries its own last reading, so a destination
	// that times out behind a station that is down explains itself without
	// anything being probed again.
	if chain := m.inv.Chain(h.Name); len(chain.Hops) > 0 {
		var parts []string
		for _, hop := range chain.Hops {
			parts = append(parts, m.hopState(hop.Spec))
		}
		parts = append(parts, m.hopState(h.Name))
		out = append(out, "", m.st.label.Render(pad("route", w)))
		out = append(out, wrapTo(strings.Join(parts, " "+m.gl.arrow+" "), w, 2, m.st.row)...)
		out = append(out, m.st.faint.Render(pad("press D to walk it one station at a time", w)))
	}

	// The chart earns its line only once there is a series to draw. One bar is
	// not a history, it is a single reading drawn sideways.
	if len(f.Samples) >= 5 {
		pct, n := f.Uptime24h()
		out = append(out, "", m.spreadIn(w, m.sampleBars(f.Samples, w-12),
			m.st.faint.Render(fmt.Sprintf("%d%% of %d", pct, n))))
	}
	return out
}

// hopState names one station along a route and what was last known of it.
func (m *Model) hopState(spec string) string {
	name := spec
	if i := strings.LastIndex(name, "@"); i >= 0 {
		name = name[i+1:]
	}
	f := m.fact(name)
	mark := m.gl.dot
	switch healthOf(f) {
	case healthUp, healthSlow:
		mark = m.gl.check
	case healthDown:
		mark = m.gl.cross
	}
	return m.st.row.Render(name) + m.healthStyle(healthOf(f)).Render(" "+mark)
}

// measuredAgo says how old the reading is, because a green host measured on
// Tuesday is not a green host.
func measuredAgo(f store.Fact) string {
	if !f.Measured() {
		return ""
	}
	return ago(f.At)
}

// wrapTo breaks a sentence across the pane, indented under itself, and gives up
// after a few lines rather than filling the pane with one long error.
func wrapTo(s string, w, maxLines int, style lipgloss.Style) []string {
	var out []string
	for _, line := range wrapWords(s, w) {
		if len(out) == maxLines {
			out[len(out)-1] = style.Render(pad(runewidth.Truncate(line, w, "…"), w))
			break
		}
		out = append(out, style.Render(pad(line, w)))
	}
	return out
}

func wrapWords(s string, w int) []string {
	if w < 8 {
		return []string{s}
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case runewidth.StringWidth(line)+1+runewidth.StringWidth(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// tiles are the readings the design boxes off, three to a row: the round trip,
// the load, how full the disk and the memory are, and what the machine is. They
// stay empty until something has run there, because a dash is honest and a zero
// is not.
func (m *Model) tiles(w int, f store.Fact) []string {
	type tile struct {
		name, value string
		style       lipgloss.Style
	}
	full := func(pct string) lipgloss.Style {
		switch {
		case store.Full(pct, 90):
			return m.st.bad
		case store.Full(pct, 75):
			return m.st.warn
		}
		return m.st.bright
	}

	tiles := []tile{
		{"RTT", rttText(f), m.st.bright},
		{"LOAD", f.Load, m.st.bright},
		{"DISK", f.Disk, full(f.Disk)},
		{"RAM", f.RAM, full(f.RAM)},
		{"SYSTEM", f.OS, m.st.bright},
	}
	if f.Uptime != "" {
		tiles = append(tiles, tile{"UP", f.Uptime, m.st.bright})
	}

	// Four to a row rather than the design's three: a terminal row is an
	// expensive thing and every one of these fits in nine columns.
	const perRow = 4
	cell := (w - perRow + 1) / perRow

	var out []string
	for i := 0; i < len(tiles); i += perRow {
		end := min(i+perRow, len(tiles))
		var top, bottom []string
		for _, t := range tiles[i:end] {
			v := t.value
			if v == "" {
				v = "-"
			}
			top = append(top, m.st.section.Render(pad(t.name, cell)))
			bottom = append(bottom, t.style.Render(pad(v, cell)))
		}
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, strings.Join(top, " "), strings.Join(bottom, " "))
	}
	return out
}

func rttText(f store.Fact) string {
	if !f.Measured() || !f.Reachable() {
		return ""
	}
	return fmt.Sprintf("%d ms", f.Millis)
}

// authOf names how this host proves who it is, which ssh spreads across three
// different places.
func authOf(h model.Host) string {
	switch {
	case len(h.IdentityFiles) > 0:
		return "key"
	case h.Account != "":
		return "account"
	}
	return "agent"
}

// actionChips are the design's row of buttons under the details, named by the
// key that performs each one. The ones that do not fit are left out rather than
// cut in half.
func (m *Model) actionChips(w, x, y int) string {
	pairs := [][3]string{
		{m.gl.enter, "connect", "enter"},
		{"f", "sftp", "f"},
		{"e", "edit", "e"},
		{"p", "ping", "p"},
	}
	var b strings.Builder
	used := 0
	for i, p := range pairs {
		cost := runewidth.StringWidth(p[0]) + runewidth.StringWidth(p[1]) + 5
		if used+cost > w {
			break
		}
		if i > 0 {
			b.WriteString(" ")
			used++
		}
		st := m.st.chip
		if i == 0 {
			st = m.st.chipOn
		}
		b.WriteString(m.st.key.Render(" "+p[0]+" ") + st.Render(" "+p[1]+" "))
		k := p[2]
		m.hitAt(x+used, y, cost-1, func() (tea.Model, tea.Cmd) { return m.updateList(keyMsg(k)) })
		used += cost - 1
	}
	return b.String()
}

// ---- the keys tab's details ----------------------------------------------

func (m *Model) keyDetailLines(height, w int) []string {
	if m.cursor < 0 || m.cursor >= len(m.accounts) {
		return []string{" " + m.st.dim.Render("no key selected")}
	}
	a := m.accounts[m.cursor]

	var out []string
	add := func(s string) { out = append(out, " "+s) }

	add(m.st.ok.Render(m.gl.dot) + " " + m.st.bright.Render(a.Name))
	add(m.st.faint.Render(pad(a.User+" "+m.gl.dot+" "+string(a.Auth), w)))
	add(m.hrule(w))
	add(m.heading("identity"))
	add("")
	label := func(k, v string) {
		if v == "" {
			v = "-"
		}
		add(m.st.label.Render(pad(k, 11)) + " " + m.st.value.Render(pad(v, max(1, w-12))))
	}
	label("user", a.User)
	label("auth", string(a.Auth))
	label("key", shortenPath(a.KeyPath))
	label("desc", a.Desc)
	add(m.hrule(w))
	add(m.heading("hosts using it"))
	add("")
	hosts := m.inv.Store.LinkedHosts(a.Name)
	if len(hosts) == 0 {
		add(m.st.faint.Render("none yet; mark hosts and press A to link them"))
	}
	for i, h := range hosts {
		if i >= height-14 {
			add(m.st.faint.Render(fmt.Sprintf("and %d more", len(hosts)-i)))
			break
		}
		add(m.st.value.Render(pad(h, w)))
	}

	if len(out) > height {
		out = out[:height]
	}
	return out
}
