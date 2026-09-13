package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atotto/clipboard"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/store"
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

	case "1":
		m.setTab(tabHosts)
	case "2":
		m.setTab(tabKeys)

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
		m.cursor = max(0, m.rowCount()-1)

	case "s":
		m.sortKey = sortKey((int(m.sortKey) + 1) % 4)
		m.applyFilter()
	case "S":
		m.sortDir = -m.sortDir
		m.applyFilter()

	case "enter":
		// In the group pane enter opens the branch; there is nothing to
		// connect to there.
		if m.focus == focusGroups {
			m.toggleGroup()
			m.cursor, m.offset = 0, 0
			m.applyFilter()
			return m, nil
		}
		if m.tab == tabKeys {
			return m.editAccount()
		}
		if h, ok := m.current(); ok {
			return m.connect(h)
		}
	case "shift+enter", "alt+enter", "W":
		return m.openElsewhere(m.selection(), false)
	case "V":
		return m.openElsewhere(m.selection(), true)
	case "f":
		if h, ok := m.current(); ok {
			return m.openFiles(h)
		}
	case "F":
		// The other sftp: tram steps out and the real client takes the
		// terminal, for the things a browser does not do.
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
			m.rebuildGroups()
			m.applyFilter()
		}

	case " ":
		if h, ok := m.current(); ok {
			if m.marked[h.Name] {
				delete(m.marked, h.Name)
			} else {
				m.marked[h.Name] = true
			}
			m.rebuildGroups()
			m.moveCursor(1)
		}

	case "y":
		if h, ok := m.current(); ok {
			line := "ssh " + h.Name
			if err := clipboard.WriteAll(line); err != nil {
				return m, fail(fmt.Errorf("nothing here can reach the clipboard: %w", err))
			}
			return m, note("copied  " + line)
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
		if m.tab == tabKeys {
			m.openAccountForm(nil)
			return m, nil
		}
		m.openForm(formAdd, model.Host{})
	case "e":
		if m.tab == tabKeys {
			return m.editAccount()
		}
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
		return m.startMeasure(m.selection())
	case "P":
		return m.startMeasure(m.filtered)
	case "D":
		hosts := append([]model.Host(nil), m.selection()...)
		return m.unlockThen("diagnosing", hosts, func() (tea.Model, tea.Cmd) {
			return m.runOn("doctor", hosts, func(hs []model.Host) []Row { return m.Runner.Doctor(hs) })
		})
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

// tabsWithoutAsking is how many tabs a single keystroke may open before it
// stops and asks. Marking forty hosts and pressing W by accident should not
// cost you forty tabs.
const tabsWithoutAsking = 6

// openElsewhere opens hosts in the terminal tram is already running in, and
// stays on screen while it happens.
//
// This is the whole point of it: a new tab or a pane beside the list costs tram
// nothing, because the terminal does the work. Only enter gives up the screen,
// and only because ssh needs it.
func (m *Model) openElsewhere(hosts []model.Host, beside bool) (tea.Model, tea.Cmd) {
	if len(hosts) == 0 {
		return m, nil
	}
	if len(hosts) > tabsWithoutAsking && !beside {
		what := fmt.Sprintf("open %d tabs, one for each marked host?", len(hosts))
		return m.confirm(what, func() tea.Cmd { return m.openCmd(hosts, beside) })
	}
	return m, m.openCmd(hosts, beside)
}

func (m *Model) openCmd(hosts []model.Host, beside bool) tea.Cmd {
	runner := m.Runner
	list := append([]model.Host(nil), hosts...)
	return func() tea.Msg {
		said, err := runner.Open(list, beside)
		if err != nil {
			return errMsg{err}
		}
		return reloadMsg(said)
	}
}

// connect gives the terminal to ssh, asking first for a key passphrase tram
// does not yet know.
//
// Asking here rather than leaving it to ssh is what makes one answer cover the
// whole run: the hosts that share the key file, the file browser, and the tabs
// opened from this list. Escape goes ahead anyway and lets ssh ask in its own
// way, which is the path that has always worked.
func (m *Model) connect(h model.Host) (tea.Model, tea.Cmd) {
	open := func() (tea.Model, tea.Cmd) { return m.quitWith(ActionConnect, h.Name) }
	if m.Runner != nil {
		if key := m.Runner.Locked(h); key != "" {
			m.openPassphraseForm(h, key, open, open)
			return m, nil
		}
	}
	return open()
}

// setTab switches the view and puts the cursor back at the top, because the row
// it was on belongs to a list that is no longer showing.
func (m *Model) setTab(t tab) {
	if m.tab == t {
		return
	}
	m.tab = t
	m.cursor, m.offset = 0, 0
	m.applyFilter()
}

// startMeasure probes hosts and fills in the latency, load and system columns.
//
// The list is copied before it is handed over. The sweep reads it from another
// goroutine, and sorting the table in the meantime rearranges the very slice it
// is walking.
func (m *Model) startMeasure(hosts []model.Host) (tea.Model, tea.Cmd) {
	if len(hosts) == 0 {
		return m, nil
	}
	list := append([]model.Host(nil), hosts...)
	return m.unlockThen("measuring", list, func() (tea.Model, tea.Cmd) {
		m.measuring = len(list)
		return m, m.measure(list)
	})
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
	n := m.rowCount()
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor += d
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
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

// rowCount is how many rows the table has, which is not the same list on every
// tab.
func (m *Model) rowCount() int {
	if m.tab == tabKeys {
		return len(m.accounts)
	}
	return len(m.filtered)
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
	// Filter as you type, so the count in the bar answers the question before
	// you have finished asking it.
	m.searchQuery = m.search.Value()
	m.applyFilter()
	return m, cmd
}

// ---- the screen -----------------------------------------------------------

// The widths the layout will not go below, and the ones the side panes take
// when there is room. The design gives these in pixels; these are the same
// proportions counted in cells.
const (
	tablePaneMin  = 46
	groupPaneWide = 26
	detailPane    = 42
)

// viewList draws the console: a bar, a rule, the panes divided by hairlines, a
// rule, and the keys. It is the layout of the SSHFleet Console design in
// giaodien/, with the pixels turned into cells.
func (m *Model) viewList() string {
	bodyH := m.bodyHeight()
	gw := m.groupPaneWidth()
	dw := m.detailPaneWidth()

	seps := 0
	if gw > 0 {
		seps++
	}
	if dw > 0 {
		seps++
	}
	tw := max(tablePaneMin, m.width-gw-dw-seps)

	// bodyTop is the first line of the panes: the bar above them and the rule
	// under it. Every clickable strip is registered against these, so a click
	// lands on the row the eye is on rather than on the row that was there
	// before the last scroll.
	const bodyTop = 2
	x := 0

	var panes []string
	if gw > 0 {
		panes = append(panes, m.pane(m.groupPaneLines(bodyH, gw-2, x+1, bodyTop), gw, bodyH))
		x += gw + 1
	}
	panes = append(panes, m.pane(m.tablePaneLines(bodyH, tw-2, x+1, bodyTop), tw, bodyH))
	x += tw + 1
	if dw > 0 {
		panes = append(panes, m.pane(m.detailPaneLines(bodyH, dw-2, x+1, bodyTop), dw, bodyH))
	}

	return m.shell(m.joinPanes(bodyH, panes...), m.listKeys(), m.statusLine())
}

func (m *Model) groupPaneWidth() int {
	if m.hideGroups || m.tab == tabKeys || m.width < 96 {
		return 0
	}
	w := groupPaneWide
	for _, r := range m.groupRows {
		if n := runewidth.StringWidth(r.label) + r.depth*2 + 10; n > w {
			w = n
		}
	}
	return clamp(w, 20, m.width/4)
}

// sidebarWidth is the room inside the group pane. Zero means it is not drawn.
func (m *Model) sidebarWidth() int { return max(0, m.groupPaneWidth()-2) }

func (m *Model) detailPaneWidth() int {
	if !m.detail {
		return 0
	}
	seps := 1
	if m.groupPaneWidth() > 0 {
		seps = 2
	}
	if m.width-m.groupPaneWidth()-detailPane-seps < tablePaneMin {
		return 0
	}
	return detailPane
}

// listHeight is how many rows the table shows: the body, less the toolbar, the
// column heading and the two rules under them.
func (m *Model) listHeight() int { return max(1, m.bodyHeight()-4) }

// ---- the table ------------------------------------------------------------

// tablePaneLines is the middle pane: what is being shown, what the columns are,
// and the rows.
func (m *Model) tablePaneLines(height, w, x, y int) []string {
	out := []string{
		" " + m.spreadIn(w, m.scopeLine(w), m.toolbarChips(x, y, w)),
		" " + m.hrule(w),
	}
	if m.tab == tabKeys {
		out = append(out, " "+m.keysHeader(w), " "+m.hrule(w))
		return append(out, m.keysRows(height-4, w, x, y+4)...)
	}
	out = append(out, " "+m.tableHeader(w, x, y+2), " "+m.hrule(w))
	return append(out, m.tableRows(height-4, w, x, y+4)...)
}

// scopeLine says which set of rows is showing, and on the two tabs whose point
// is not obvious from their name, what that set is.
func (m *Model) scopeLine(w int) string {
	label := m.st.ok.Render(m.scopeLabel())
	note := ""
	if m.tab == tabKeys {
		note = "identities, and the hosts linked to each"
	}
	if note == "" || w < len(note)+24 {
		return label
	}
	return label + "  " + m.st.faint.Render(m.gl.dot+"  "+note)
}

// spreadIn is spread for one pane rather than the whole window.
func (m *Model) spreadIn(w int, left, right string) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+1 > w {
		return ansiTruncate(left, w)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

// scopeLabel says which set of hosts the table is showing.
func (m *Model) scopeLabel() string {
	if m.tab == tabKeys {
		return "keys"
	}
	if g := m.selectedGroup(); g.path != viewAll {
		return g.label
	}
	return "all hosts"
}

// toolbarChips are the design's buttons, named by the key that performs them,
// because a terminal has no pointer to click one with.
func (m *Model) toolbarChips(x, y, w int) string {
	pairs := [][2]string{{"a", "add host"}, {"P", "ping all"}, {"I", "import"}, {"ctrl+k", "commands"}}
	if m.tab == tabKeys {
		pairs = [][2]string{{"a", "new key"}, {"e", "edit"}}
	}

	var b strings.Builder
	for _, p := range pairs {
		b.WriteString("  " + m.st.key.Render(" "+p[0]+" ") + m.st.chip.Render(" "+p[1]+" "))
	}
	// The strip is right-aligned, so the chips are measured from the far end.
	used := lipgloss.Width(b.String())
	at := x + w - used
	for _, p := range pairs {
		cost := runewidth.StringWidth(p[0]) + runewidth.StringWidth(p[1]) + 6
		k := p[0]
		m.hitAt(at+2, y, cost-2, func() (tea.Model, tea.Cmd) {
			if k == "ctrl+k" {
				return m.openPalette()
			}
			return m.updateList(keyMsg(k))
		})
		at += cost
	}
	return b.String()
}

// tableColumns is how much of the pane each column gets. The design drops one
// column at a time as the window narrows, in this order, and so does this.
type tableColumns struct {
	alias, host, port, lat, seen, tags int
}

// rowPrefix is the selection bar, the mark box and the spaces around them.
const rowPrefix = 4

func columnsFor(w int) tableColumns {
	c := tableColumns{}
	rest := w - rowPrefix
	take := func(width, min int) int {
		if w < min {
			return 0
		}
		rest -= width + 1
		return width
	}
	c.lat = take(8, 46)
	c.port = take(5, 56)
	c.tags = take(20, 66)
	c.seen = take(10, 78)
	if rest < 16 {
		rest = 16
	}
	c.alias = rest * 48 / 100
	c.host = rest - c.alias
	return c
}

func (m *Model) tableHeader(w, x, y int) string {
	c := columnsFor(w)
	// Each heading sorts by its own column, the way the design's arrows say it
	// does. The strips are registered left to right as the line is built.
	at := x + rowPrefix
	sortable := func(width int, k sortKey) {
		key := k
		m.hitAt(at, y, width, func() (tea.Model, tea.Cmd) {
			if m.sortKey == key {
				m.sortDir = -m.sortDir
			} else {
				m.sortKey, m.sortDir = key, 1
			}
			m.applyFilter()
			return m, nil
		})
		at += width + 1
	}
	m.hitAt(x+2, y, 2, func() (tea.Model, tea.Cmd) { return m.markAll() })
	arrow := func(k sortKey) string {
		if m.sortKey != k {
			return ""
		}
		if m.sortDir < 0 {
			return " " + m.gl.down
		}
		return " " + m.gl.up
	}
	line := "  " + m.st.unmarked.Render(m.gl.unmarked) + " " +
		m.st.section.Render(pad("ALIAS"+arrow(sortAlias), c.alias)) + " " +
		m.st.section.Render(pad("USER@HOST"+arrow(sortHost), c.host))
	sortable(c.alias, sortAlias)
	sortable(c.host, sortHost)
	if c.port > 0 {
		line += " " + m.st.section.Render(pad("PORT", c.port))
		at += c.port + 1
	}
	if c.lat > 0 {
		line += " " + m.st.section.Render(pad("LATENCY"+arrow(sortLatency), c.lat))
		sortable(c.lat, sortLatency)
	}
	if c.seen > 0 {
		line += " " + m.st.section.Render(pad("LAST SEEN"+arrow(sortSeen), c.seen))
		sortable(c.seen, sortSeen)
	}
	if c.tags > 0 {
		line += " " + m.st.section.Render(padLeft("TAGS", c.tags))
	}
	return line
}

// markAll marks every row shown, or clears them all when they already are,
// which is what the box at the head of the column does.
func (m *Model) markAll() (tea.Model, tea.Cmd) {
	all := len(m.filtered) > 0
	for _, h := range m.filtered {
		if !m.marked[h.Name] {
			all = false
			break
		}
	}
	for _, h := range m.filtered {
		if all {
			delete(m.marked, h.Name)
		} else {
			m.marked[h.Name] = true
		}
	}
	m.rebuildGroups()
	return m, nil
}

func (m *Model) tableRows(height, w, x, y int) []string {
	if len(m.filtered) == 0 {
		return []string{"   " + m.st.dim.Render(m.emptyLine())}
	}
	out := make([]string, 0, height)
	end := min(m.offset+height, len(m.filtered))
	for i := m.offset; i < end; i++ {
		row := y + i - m.offset
		at := i
		// The whole row selects; the box at its left end marks.
		m.hitAt(x, row, w, func() (tea.Model, tea.Cmd) {
			m.cursor = at
			if m.doubleClick {
				// Two presses on a row open it, the way two presses on a file
				// open the file.
				return m.quitWith(ActionConnect, m.filtered[at].Name)
			}
			return m, nil
		})
		m.hitAt(x+2, row, 2, func() (tea.Model, tea.Cmd) {
			m.cursor = at
			h := m.filtered[at]
			if m.marked[h.Name] {
				delete(m.marked, h.Name)
			} else {
				m.marked[h.Name] = true
			}
			m.rebuildGroups()
			return m, nil
		})
		out = append(out, m.renderRow(m.filtered[i], i == m.cursor, w, x, row))
	}
	return out
}

// emptyLine says why the table is empty, which is never the same reason twice.
func (m *Model) emptyLine() string {
	switch {
	case m.searchQuery != "":
		return "nothing matches " + m.searchQuery
	case len(m.hosts) == 0:
		return "no hosts in this file yet; press a to add one"
	}
	return "nothing in this group"
}

// renderRow draws one host. The row under the cursor carries the accent bar and
// the band behind it, which is how the design says where you are, and its tags
// give way to the two buttons that act on it.
func (m *Model) renderRow(h model.Host, sel bool, w, x, y int) string {
	c := columnsFor(w)
	f := m.fact(h.Name)

	bar := " "
	if sel {
		bar = m.st.rowBar.Render(m.gl.bar)
	}
	box := m.st.onRow(m.st.unmarked, sel).Render(m.gl.unmarked)
	if m.marked[h.Name] {
		box = m.st.onRow(m.st.marked, sel).Render(m.gl.marked)
	}

	name := h.Name
	if h.Favorite {
		name = m.gl.star + " " + name
	}
	if h.Drift {
		name += m.gl.drift
	}

	nameStyle := m.st.onRow(m.st.row, sel)
	if sel {
		nameStyle = m.st.selected
	}
	dim := m.st.onRow(m.st.dim, sel)
	faint := m.st.onRow(m.st.faint, sel)

	line := bar + " " + box + " " + nameStyle.Render(pad(name, c.alias)) +
		dim.Render(" "+pad(userHost(h), c.host))
	if c.port > 0 {
		line += faint.Render(" " + pad(h.PortOr(), c.port))
	}
	if c.lat > 0 {
		line += m.st.onRow(m.healthStyle(healthOf(f)), sel).Render(" " + pad(latencyText(f), c.lat))
	}
	if c.seen > 0 {
		line += faint.Render(" " + pad(ago(h.LastUsed), c.seen))
	}
	if c.tags > 0 {
		if sel {
			line += " " + m.rowButtons(h, c.tags, x+w-c.tags, y)
		} else {
			line += " " + m.tagPills(h.Tags, c.tags)
		}
	}
	if sel {
		// The band runs to the edge of the pane, as it does in the design.
		line = m.st.selected.Render(ansiPad(line, w))
	}
	return " " + line
}

// tagPills draws the labels the way the design does: at most two, each on its
// own little ground, right-aligned against the edge of the pane.
func (m *Model) tagPills(tags []string, w int) string {
	if len(tags) == 0 {
		return strings.Repeat(" ", w)
	}
	shown := tags
	more := 0
	if len(shown) > 2 {
		more = len(shown) - 2
		shown = shown[:2]
	}

	var parts []string
	for _, t := range shown {
		parts = append(parts, m.st.tag.Render(" "+t+" "))
	}
	if more > 0 {
		parts = append(parts, m.st.faint.Render(fmt.Sprintf("+%d", more)))
	}
	out := strings.Join(parts, " ")

	// Right-aligned, and dropped rather than cut when the column is too narrow
	// for even one label.
	if gap := w - lipgloss.Width(out); gap > 0 {
		return strings.Repeat(" ", gap) + out
	}
	return padLeft(strings.Join(shown, " "), w)
}

// rowButtons are the two the design puts on the row under the cursor, in place
// of its tags: the one enter performs, and the one that opens the rest.
func (m *Model) rowButtons(h model.Host, w, x, y int) string {
	connect := m.st.chipOn.Render(" connect ")
	more := m.st.chip.Render(" " + m.gl.more + " ")
	out := connect + " " + more
	if lipgloss.Width(out) > w {
		out = more
	}

	gap := w - lipgloss.Width(out)
	if gap < 0 {
		return strings.Repeat(" ", max(0, w))
	}
	at := x + gap
	if lipgloss.Width(out) > lipgloss.Width(more) {
		name := h.Name
		m.hitAt(at, y, lipgloss.Width(connect), func() (tea.Model, tea.Cmd) {
			return m.quitWith(ActionConnect, name)
		})
		at += lipgloss.Width(connect) + 1
	}
	mx := at
	m.hitAt(mx, y, lipgloss.Width(more), func() (tea.Model, tea.Cmd) {
		return m.openMenu(mx, y)
	})
	return strings.Repeat(" ", gap) + out
}

func (m *Model) healthStyle(h health) lipgloss.Style {
	switch h {
	case healthUp:
		return m.st.ok
	case healthSlow, healthLocked:
		return m.st.warn
	case healthDown:
		return m.st.bad
	}
	return m.st.faint
}

// latencyText is the round trip, or the reason there is not one.
func latencyText(f store.Fact) string {
	switch {
	case !f.Measured():
		return "-"
	case !f.Reachable():
		return strings.ToLower(f.Class)
	}
	return fmt.Sprintf("%d ms", f.Millis)
}

// userHost is the address as ssh will dial it, without the port: the port has a
// column of its own and printing it twice reads as a disagreement.
func userHost(h model.Host) string {
	s := h.Addr()
	if h.User != "" {
		s = h.User + "@" + s
	}
	return s
}

// ---- the keys tab ---------------------------------------------------------

// keyColumns splits the keys table, which has its own shape: an account is not
// a host and does not have a port or a latency.
func keyColumns(w int) (name, user, auth, path int) {
	name, user, auth = 22, 18, 10
	path = max(10, w-rowPrefix-name-user-auth-3)
	return
}

func (m *Model) keysHeader(w int) string {
	nw, uw, aw, pw := keyColumns(w)
	return strings.Repeat(" ", rowPrefix) +
		m.st.section.Render(pad("NAME", nw)) + " " +
		m.st.section.Render(pad("USER", uw)) + " " +
		m.st.section.Render(pad("AUTH", aw)) + " " +
		m.st.section.Render(pad("IDENTITY", pw))
}

func (m *Model) keysRows(height, w, x, y int) []string {
	if len(m.accounts) == 0 {
		return []string{"   " + m.st.dim.Render("no keys yet; press a to make one")}
	}
	nw, uw, aw, pw := keyColumns(w)

	out := make([]string, 0, height)
	end := min(m.offset+height, len(m.accounts))
	for i := m.offset; i < end; i++ {
		a := m.accounts[i]
		sel := i == m.cursor

		at := i
		m.hitAt(x, y+i-m.offset, w, func() (tea.Model, tea.Cmd) {
			m.cursor = at
			if m.doubleClick {
				return m.editAccount()
			}
			return m, nil
		})

		bar := " "
		if sel {
			bar = m.st.rowBar.Render(m.gl.bar)
		}
		nameStyle := m.st.onRow(m.st.row, sel)
		if sel {
			nameStyle = m.st.selected
		}
		used := len(m.inv.Store.LinkedHosts(a.Name))

		line := bar + "   " + nameStyle.Render(pad(a.Name, nw-1)) +
			m.st.onRow(m.st.dim, sel).Render(" "+pad(a.User, uw)) +
			m.st.onRow(m.st.ok, sel).Render(" "+pad(string(a.Auth), aw)) +
			m.st.onRow(m.st.faint, sel).Render(" "+pad(keyIdentity(a, used), pw))
		if sel {
			line = m.st.selected.Render(ansiPad(line, w))
		}
		out = append(out, " "+line)
	}
	return out
}

func keyIdentity(a model.Account, used int) string {
	s := shortenPath(a.KeyPath)
	if s == "" {
		s = "-"
	}
	return fmt.Sprintf("%s  (%d host)", s, used)
}

// editAccount opens the identity under the cursor for editing.
func (m *Model) editAccount() (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.accounts) {
		return m, nil
	}
	m.openAccountFormFor(m.accounts[m.cursor])
	return m, nil
}

// ---- sorting --------------------------------------------------------------

// sortRows orders the table by the chosen column. A host nobody has measured
// sorts last under latency rather than first, because an empty cell is not a
// fast one.
func (m *Model) sortRows() {
	dir := m.sortDir
	if dir == 0 {
		dir = 1
	}
	less := func(a, b model.Host) bool {
		switch m.sortKey {
		case sortHost:
			return strings.ToLower(userHost(a)) < strings.ToLower(userHost(b))
		case sortLatency:
			return latencyRank(m.fact(a.Name)) < latencyRank(m.fact(b.Name))
		case sortSeen:
			return a.LastUsed > b.LastUsed
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	}
	sort.SliceStable(m.filtered, func(i, j int) bool {
		if dir < 0 {
			return less(m.filtered[j], m.filtered[i])
		}
		return less(m.filtered[i], m.filtered[j])
	})
}

// latencyRank puts the quick first, the slow after them, and everything that
// did not answer or was never asked at the end.
func latencyRank(f store.Fact) int64 {
	switch {
	case !f.Measured():
		return 1 << 40
	case !f.Reachable():
		return 1 << 39
	}
	return f.Millis
}

// ---- the bottom bar -------------------------------------------------------

func (m *Model) listKeys() [][2]string {
	switch {
	case m.mode == modeConfirm:
		return [][2]string{{"y", "yes"}, {"n", "no"}, {"esc", "cancel"}}
	case m.mode == modeSearch:
		return [][2]string{{"enter", "apply"}, {"esc", "cancel"}, {"#name", "group or tag"}}
	case m.focus == focusGroups:
		return [][2]string{{"enter", "open"}, {"tab", "hosts"}, {"g", "hide pane"}, {"/", "filter"}, {"?", "help"}, {"q", "quit"}}
	case m.tab == tabKeys:
		return [][2]string{{"a", "new key"}, {"e", "edit"}, {"1", "hosts"}, {"?", "help"}, {"q", "quit"}}
	}
	return [][2]string{
		{m.gl.enter, "connect"}, {"space", "mark"}, {"W", "tab"}, {"V", "beside"},
		{"f", "files"}, {"p", "ping"}, {"a", "add"}, {"e", "edit"}, {"d", "del"},
		{"I", "import"}, {"x", "exec"}, {"D", "doctor"}, {"s", "sort"},
		{"/", "filter"}, {"?", "help"}, {"q", "quit"},
	}
}

// statusLine is the sentence against the right edge of the bottom bar.
func (m *Model) statusLine() string {
	switch {
	// A question first: while one is standing, nothing else on this line
	// matters, and a prompt nobody can see is a prompt nobody can answer.
	case m.mode == modeConfirm:
		return m.confirmText + "  [y/n]"
	case m.problem != "":
		return m.problem
	case m.running != "":
		return "running " + m.running + m.gl.ellipsis
	case m.measuring > 0:
		return fmt.Sprintf("measuring %d host(s)", m.measuring)
	case m.status != "":
		return m.status
	}
	// What the last sweep found, once there has been one. It sits here rather
	// than in a chart of its own because it is three numbers, and three numbers
	// are a sentence.
	if up, slow, down, unknown := m.inv.Store.FleetHealth(model.Names(m.hosts)); unknown < len(m.hosts) {
		line := fmt.Sprintf("%d up", up)
		if slow > 0 {
			line += fmt.Sprintf(" %s %d slow", m.gl.dot, slow)
		}
		if down > 0 {
			line += fmt.Sprintf(" %s %d down", m.gl.dot, down)
		}
		if unknown > 0 {
			line += fmt.Sprintf(" %s %d not measured", m.gl.dot, unknown)
		}
		return line + fmt.Sprintf(" %s sorted by %s", m.gl.dot, m.sortKey)
	}
	return fmt.Sprintf("ready %s %d hosts indexed %s sorted by %s",
		m.gl.dot, len(m.hosts), m.gl.dot, m.sortKey)
}

// ---- help -----------------------------------------------------------------

func (m *Model) viewHelp() string {
	rows := [][2]string{
		{"enter", "connect: tram exits, ssh takes the terminal, tram comes back"},
		{"ctrl+k", "the command palette: every action, by name"},
		{"W", "open a tab for every marked host; tram stays where it is"},
		{"V", "open beside the list, in a pane of the same window"},
		{"f", "the file browser: this machine on the left, the host on the right"},
		{"F", "hand the terminal to the real sftp client instead"},
		{"y", "copy the ssh command for the selected host"},
		{"space", "mark a host; actions then apply to every marked host"},
		{"p / P", "measure the selection, or everything shown: latency, load, system"},
		{"s / S", "change the column the table is sorted by, and reverse it"},
		{"1 2", "hosts, keys"},
		{"/", "search; a query starting with # matches groups and tags"},
		{"esc", "clear the search, then the marks"},
		{"a e c d", "add, edit, clone, delete a host"},
		{"E", "edit every marked host at once"},
		{"A", "link the selection to an account, or make one"},
		{"D x r", "doctor, run a command, run a snippet"},
		{"I", "import an Ansible inventory or a CSV export"},
		{"*", "pin a host, so that it shows under Favorites"},
		{"tab", "move between the group pane and the table"},
		{"g / i", "put the group pane away, put the details pane away"},
		{"R", "re-read ssh_config from disk"},
		{"q", "quit"},
		{"click", "a row selects it, the box marks it, a heading sorts by it"},
		{"double click", "connect; right click opens the menu for that row"},
	}

	w := m.width - 2
	keyW := clamp(w/5, 8, 16)
	lines := []string{" " + m.heading("keys"), " " + m.hrule(w)}
	for _, r := range rows {
		lines = append(lines, " "+m.st.key.Render(" "+pad(r[0], keyW)+" ")+"  "+m.st.value.Render(r[1]))
	}
	lines = append(lines, "", " "+m.st.dim.Render("tram never draws the inside of a session. enter gives the terminal to ssh."))

	bodyH := m.bodyHeight()
	if len(lines) > bodyH {
		lines = lines[:bodyH]
	}
	return m.shell(m.pane(lines, m.width, bodyH), [][2]string{{"any key", "back"}}, "help")
}

// ---- text helpers ---------------------------------------------------------

func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) > w {
		return runewidth.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-runewidth.StringWidth(s))
}

// padLeft right-aligns, which is what the design does with the tag column.
func padLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) > w {
		return runewidth.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", w-runewidth.StringWidth(s)) + s
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
	p = tildePath(p)
	if i := strings.LastIndexAny(p, `/\`); i > 0 && len(p) > 40 {
		return "…" + p[i:]
	}
	return p
}
