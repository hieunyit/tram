package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
	"github.com/mattn/go-runewidth"
)

func (m *Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m.quitWith(ActionQuit, "")

	case "up", "k", "ctrl+p":
		m.moveCursor(-1)
	case "down", "j", "ctrl+n":
		m.moveCursor(1)
	case "pgup":
		m.moveCursor(-m.listHeight())
	case "pgdown":
		m.moveCursor(m.listHeight())
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(0, len(m.filtered)-1)

	case "enter":
		if h, ok := m.current(); ok {
			return m.quitWith(ActionConnect, h.Name)
		}
	case "shift+enter", "alt+enter", "W":
		if h, ok := m.current(); ok {
			return m.quitWith(ActionWindow, h.Name)
		}
	case "f":
		if h, ok := m.current(); ok {
			return m.quitWith(ActionSFTP, h.Name)
		}

	case "/":
		m.mode = modeSearch
		m.search.SetValue(m.searchQuery)
		m.search.Focus()
		m.search.CursorEnd()
		return m, textinput.Blink

	case "esc":
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.applyFilter()
			return m, nil
		}
		if len(m.marked) > 0 {
			m.marked = map[string]bool{}
		}

	case " ":
		if h, ok := m.current(); ok {
			if m.marked[h.Name] {
				delete(m.marked, h.Name)
			} else {
				m.marked[h.Name] = true
			}
			m.moveCursor(1)
		}

	case "tab", "i":
		m.detail = !m.detail

	case "*":
		if h, ok := m.current(); ok {
			on, err := m.inv.Store.ToggleFavorite(h.Name)
			if err != nil {
				return m, fail(err)
			}
			state := "unpinned"
			if on {
				state = "pinned"
			}
			return m, note(h.Name + " " + state)
		}

	case "a":
		m.openForm(formAdd, model.Host{})
	case "e":
		if h, ok := m.current(); ok {
			m.openForm(formEdit, h)
		}
	case "c":
		if h, ok := m.current(); ok {
			m.openForm(formClone, h)
		}
	case "E":
		sel := m.selection()
		if len(sel) < 2 {
			return m, fail(fmt.Errorf("mark several hosts with space first"))
		}
		m.openBatchForm(sel)
	case "d":
		return m.confirmDelete()

	case "A":
		return m.openAccountPicker()

	case "p":
		return m.runOn("ping", func(hs []model.Host) []Row { return m.Runner.Ping(hs) })
	case "D":
		return m.runOn("doctor", func(hs []model.Host) []Row { return m.Runner.Doctor(hs) })
	case "x":
		m.openExecForm()
	case "r":
		return m.openSnippetPicker()

	case "R":
		m.inv2reload()
		return m, note("reloaded from disk")

	case "?":
		m.mode = modeHelp
	}
	return m, nil
}

func (m *Model) inv2reload() {
	if fresh, err := inventory.Load(inventory.Options{}); err == nil {
		m.inv = fresh
	}
	m.reload()
}

func (m *Model) moveCursor(d int) {
	if len(m.filtered) == 0 {
		m.cursor = 0
		return
	}
	m.cursor += d
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.search.Blur()
		return m, nil
	case "enter":
		m.mode = modeNormal
		m.searchQuery = m.search.Value()
		m.search.Blur()
		m.cursor, m.offset = 0, 0
		m.applyFilter()
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	// Filter as you type so the count in the status line answers the question
	// before you have finished asking it.
	m.searchQuery = m.search.Value()
	m.applyFilter()
	return m, cmd
}

func (m *Model) viewList() string {
	var b strings.Builder

	title := m.st.title.Render("tram")
	sub := m.st.muted.Render("  " + shortenPath(m.inv.Config.Root.Path))
	b.WriteString(title + sub + "\n")

	if m.mode == modeSearch {
		b.WriteString(m.search.View() + "\n")
	} else {
		b.WriteString(m.header() + "\n")
	}

	h := m.listHeight()
	if len(m.filtered) == 0 {
		b.WriteString(m.st.muted.Render("  no hosts match") + "\n")
		for i := 1; i < h; i++ {
			b.WriteString("\n")
		}
	} else {
		end := min(m.offset+h, len(m.filtered))
		for i := m.offset; i < end; i++ {
			b.WriteString(m.renderRow(m.filtered[i], i == m.cursor) + "\n")
		}
		for i := end - m.offset; i < h; i++ {
			b.WriteString("\n")
		}
	}

	if m.detail {
		b.WriteString(m.renderDetail())
	}
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(m.st.help.Render(m.listHelp()))
	return b.String()
}

// columns adapts to the window: the wide fields disappear before the name does.
func (m *Model) columns() (name, target, group, jump int) {
	w := m.width - 6
	name = clamp(w*30/100, 10, 28)
	target = clamp(w*32/100, 12, 34)
	group = clamp(w*18/100, 0, 18)
	jump = w - name - target - group
	if jump < 0 {
		jump = 0
	}
	return
}

func (m *Model) header() string {
	nw, tw, gw, jw := m.columns()
	cols := "  " + pad("NAME", nw) + " " + pad("TARGET", tw) + " " + pad("GROUP", gw)
	if jw > 4 {
		cols += " " + pad("JUMP", jw)
	}
	return m.st.header.Render(cols)
}

func (m *Model) renderRow(h model.Host, sel bool) string {
	nw, tw, gw, jw := m.columns()

	prefix := "  "
	if m.marked[h.Name] {
		prefix = m.st.marked.Render(m.gl.marked) + " "
	} else if sel {
		prefix = m.st.selected.Render(m.gl.arrow[:1]) + " "
	}

	name := h.Name
	if h.Favorite {
		name = m.gl.star + " " + name
	}
	if h.Drift {
		name += m.gl.drift
	}

	line := pad(name, nw) + " " + pad(h.Target(), tw) + " " + pad(h.Group, gw)
	if jw > 4 {
		line += " " + pad(h.ProxyJump, jw)
	}

	style := m.st.row
	switch {
	case sel:
		style = m.st.selected
	case h.ReadOnly:
		style = m.st.muted
	}
	return prefix + style.Render(line)
}

func (m *Model) renderDetail() string {
	h, ok := m.current()
	if !ok {
		return strings.Repeat("\n", 8)
	}
	chain := m.inv.Chain(h.Name)

	var lines []string
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, m.st.label.Render(pad(k, 10))+" "+v)
		}
	}
	add("hostname", h.Addr())
	add("user", h.User)
	add("port", h.PortOr())
	add("identity", strings.Join(h.IdentityFiles, ", "))
	add("desc", h.Desc)
	if h.Account != "" {
		v := h.Account
		if h.Drift {
			v += m.st.warn.Render("  (edited by hand)")
		}
		add("account", v)
	}
	if len(chain.Cycle) > 0 {
		add("route", m.st.bad.Render("loop: "+strings.Join(chain.Cycle, " "+m.gl.arrow+" ")))
	} else if len(chain.Hops) > 0 {
		var route []string
		for _, hop := range chain.Hops {
			route = append(route, hop.Spec)
		}
		route = append(route, h.Name)
		add("route", strings.Join(route, " "+m.gl.arrow+" "))
	}
	if h.LastUsed > 0 {
		add("last used", m.gl.clock+" "+time.Unix(h.LastUsed, 0).Format("2006-01-02 15:04"))
	}
	add("file", shortenPath(fmt.Sprintf("%s:%d", h.File, h.Line)))
	if h.ReadOnly {
		add("access", m.st.muted.Render("read-only; tram does not manage this file"))
	}

	for len(lines) < 7 {
		lines = append(lines, "")
	}
	return strings.Join(lines[:7], "\n") + "\n"
}

func (m *Model) listHelp() string {
	if m.mode == modeSearch {
		return "enter apply  esc cancel   #group filters by group"
	}
	return "enter connect  W window  f sftp  space mark  / search  a add  e edit  c clone  d delete  E batch  A account  p ping  D doctor  x exec  r snippet  * pin  tab detail  ? help  q quit"
}

func (m *Model) viewHelp() string {
	rows := [][2]string{
		{"enter", "connect: tram exits, ssh takes the terminal, tram comes back"},
		{"W", "connect in a new terminal window or tab"},
		{"f", "open sftp against the selected host"},
		{"space", "mark a host; actions then apply to every marked host"},
		{"/", "search; a query starting with # matches groups only"},
		{"esc", "clear the search, then the marks"},
		{"a e c d", "add, edit, clone, delete a host"},
		{"E", "edit every marked host at once"},
		{"A", "link the selection to an account"},
		{"p D x r", "ping, doctor, run a command, run a snippet"},
		{"*", "pin a host to the top of nothing in particular, but mark it"},
		{"tab", "show the detail pane"},
		{"R", "re-read ssh_config from disk"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(m.st.title.Render("keys") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + m.st.selected.Render(pad(r[0], 10)) + " " + r[1] + "\n")
	}
	b.WriteString("\n" + m.st.muted.Render("tram never draws the inside of a session. enter gives the terminal to ssh.") + "\n")
	b.WriteString("\n" + m.st.help.Render("any key to go back"))
	return b.String()
}

func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) > w {
		return runewidth.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-runewidth.StringWidth(s))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func shortenPath(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i > 0 && len(p) > 40 {
		return "…" + p[i:]
	}
	return p
}
