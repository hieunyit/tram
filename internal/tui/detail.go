package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
	var conn []string
	conn = append(conn,
		label("address", h.Addr(), plain),
		label("user", h.User, plain),
		label("auth", authOf(h), func(s string) string { return m.st.ok.Render(s) }),
		label("identity", shortenPath(strings.Join(h.IdentityFiles, ", ")), plain),
		label("proxyjump", h.ProxyJump, plain),
		label("group", h.Group, plain))
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
	if chain := m.inv.Chain(h.Name); len(chain.Cycle) > 0 {
		conn = append(conn, "", m.st.bad.Render(pad("loop: "+strings.Join(chain.Cycle, " "+m.gl.arrow+" "), w)))
	} else if len(chain.Hops) > 0 {
		var route []string
		for _, hop := range chain.Hops {
			route = append(route, hop.Spec)
		}
		route = append(route, h.Name)
		conn = append(conn, label("route", strings.Join(route, " "+m.gl.arrow+" "), plain))
	}
	add(m.heading("connection"), "")
	add(conn...)

	// Whether it has been answering.
	reach := []string{"", "", ""}
	if bars := m.sampleBars(f.Samples, w); bars != "" {
		reach[2] = bars
	} else {
		reach[2] = m.st.faint.Render("press p to measure this host")
	}
	pct, samples := f.Uptime24h()
	right := m.st.faint.Render("not measured")
	if samples > 0 {
		right = m.st.ok.Render(fmt.Sprintf("%d%% of %d", pct, samples))
	}
	reach[0] = m.spreadIn(w, m.heading("reachability"), right)
	tiles := m.tiles(w, f)
	if f.Uptime != "" {
		tiles = append(tiles, m.st.label.Render(pad("uptime", 11))+" "+m.st.value.Render(pad(f.Uptime, max(1, w-12))))
	}
	if fits(len(reach) + len(tiles) + 2) {
		add(m.hrule(w))
		add(reach...)
		add("")
		add(tiles...)
	}

	// What has happened to it lately.
	events := m.inv.Store.Events(h.Name)
	var recent []string
	if len(events) == 0 {
		recent = append(recent, m.st.faint.Render("nothing recorded yet"))
	}
	for _, e := range events {
		recent = append(recent, m.st.faint.Render(pad(ago(e.At), 10))+" "+m.st.value.Render(pad(e.What, max(1, w-11))))
	}
	if fits(len(recent) + 3) {
		add(m.hrule(w), m.heading("recent"), "")
		add(recent...)
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

// tiles are the three readings the design boxes off: round trip, load average
// and operating system. They stay empty until something has run on the machine,
// because a dash is honest and a zero is not.
func (m *Model) tiles(w int, f store.Fact) []string {
	cell := (w - 2) / 3
	names := []string{"RTT", "LOAD", "SYSTEM"}
	values := []string{rttText(f), f.Load, f.OS}

	var top, bottom []string
	for i, n := range names {
		v := values[i]
		if v == "" {
			v = "-"
		}
		top = append(top, m.st.section.Render(pad(n, cell)))
		bottom = append(bottom, m.st.bright.Render(pad(v, cell)))
	}
	return []string{strings.Join(top, " "), strings.Join(bottom, " ")}
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
