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
		m.move(-1)
	case "down", "j", "ctrl+n":
		m.move(1)

	case "left", "h":
		if m.sidebarWidth() > 0 {
			m.focus = focusGroups
		}
	case "right", "l":
		m.focus = focusHosts
	case "tab":
		if m.sidebarWidth() > 0 {
			if m.focus == focusGroups {
				m.focus = focusHosts
			} else {
				m.focus = focusGroups
			}
		}
	case "g":
		m.hideGroups = !m.hideGroups
		if m.hideGroups {
			m.focus = focusHosts
		}
	case "pgup":
		m.moveCursor(-m.listHeight())
	case "pgdown":
		m.moveCursor(m.listHeight())
	case "home":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(0, len(m.filtered)-1)

	case "enter":
		// In the group pane enter opens the branch; there is nothing to
		// connect to there.
		if m.focus == focusGroups {
			m.toggleGroup()
			m.cursor, m.offset = 0, 0
			m.applyFilter()
			return m, nil
		}
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

	case "i":
		m.detail = !m.detail

	case "*":
		if h, ok := m.current(); ok {
			on, err := m.inv.Store.ToggleFavorite(h.Name)
			if err != nil {
				return m, fail(err)
			}
			// The file did not change, so only the inventory's own view of it
			// has to be rebuilt for the star to appear.
			m.inv.Refresh()
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

	case "I":
		m.openImportForm()

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

// move sends the keystroke to whichever pane has the keyboard.
func (m *Model) move(d int) {
	if m.focus == focusGroups {
		m.groupCursor = clamp(m.groupCursor+d, 0, max(0, len(m.groupRows)-1))
		m.cursor, m.offset = 0, 0
		m.applyFilter()
		return
	}
	m.moveCursor(d)
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
	side := m.renderSidebar(h)

	var rows []string
	if len(m.filtered) == 0 {
		rows = append(rows, m.st.muted.Render("  nothing here"))
	} else {
		end := min(m.offset+h, len(m.filtered))
		for i := m.offset; i < end; i++ {
			rows = append(rows, m.renderRow(m.filtered[i], i == m.cursor))
		}
	}
	for len(rows) < h {
		rows = append(rows, "")
	}

	// The two panes are written a line at a time so they stay level, whatever
	// either of them contains.
	for i := 0; i < h; i++ {
		if len(side) == h {
			b.WriteString(side[i] + m.st.muted.Render(m.gl.vbar) + rows[i] + "\n")
			continue
		}
		b.WriteString(rows[i] + "\n")
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
	w := m.width - 6 - m.sidebarWidth() - 2
	if w < 30 {
		w = 30
	}
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
	// The headings sit over the host list, not over the group pane.
	if w := m.sidebarWidth(); w > 0 {
		return m.st.header.Render(pad("GROUPS", w+2)) + m.st.header.Render(cols)
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
	if m.focus == focusGroups {
		return "enter open  right hosts  tab switch  g hide groups  / search  ? help  q quit"
	}
	return "enter connect  W window  f sftp  space mark  tab groups  / search  a add  e edit  c clone  d delete  E batch  A account  p ping  D doctor  x exec  r snippet  I import  * pin  i detail  ? help  q quit"
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
		{"A", "link the selection to an account, or make one"},
		{"p D x r", "ping, doctor, run a command, run a snippet"},
		{"I", "import an Ansible inventory or a CSV export"},
		{"*", "pin a host to the top of nothing in particular, but mark it"},
		{"tab / left / right", "move between the group pane and the host list"},
		{"enter (groups)", "open or close a branch of the group tree"},
		{"g", "hide the group pane, for a narrow window"},
		{"i", "show the detail pane"},
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
