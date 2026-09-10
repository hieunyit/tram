package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/hieuny/tram/internal/model"
)

// The three rows that are not groups. They are paths no real group can have,
// because a group path never starts with a colon.
const (
	viewAll       = ":all"
	viewFavorites = ":favorites"
	viewRecent    = ":recent"
	viewUngrouped = ":ungrouped"
)

// groupRow is one line of the sidebar: a heading, or a group at some depth.
type groupRow struct {
	label string
	path  string
	depth int
	count int
	// kids and open drive the disclosure marker; a group with nothing under it
	// gets no marker at all rather than one that does nothing.
	kids bool
	open bool
}

// rebuildGroups recomputes the sidebar from the current hosts.
//
// The three headings come first because they are how most people actually
// navigate: everything, the ones pinned, the ones opened lately. The tree below
// is for when the fleet is large enough that scanning it is not an option.
func (m *Model) rebuildGroups() {
	rows := []groupRow{{label: "All", path: viewAll, count: len(m.hosts)}}

	fav, recent := 0, 0
	for _, h := range m.hosts {
		if h.Favorite {
			fav++
		}
		if h.LastUsed > 0 {
			recent++
		}
	}
	if fav > 0 {
		rows = append(rows, groupRow{label: m.gl.star + " Favorites", path: viewFavorites, count: fav})
	}
	if recent > 0 {
		rows = append(rows, groupRow{label: m.gl.clock + " Recent", path: viewRecent, count: recent})
	}

	var walk func(nodes []*model.GroupNode)
	walk = func(nodes []*model.GroupNode) {
		for _, n := range nodes {
			open := m.openGroups[n.Path]
			rows = append(rows, groupRow{
				label: n.Name,
				path:  n.Path,
				depth: n.Depth,
				count: n.Total,
				kids:  len(n.Children) > 0,
				open:  open,
			})
			if open {
				walk(n.Children)
			}
		}
	}
	walk(model.BuildGroupTree(m.hosts))

	if n := model.Ungrouped(m.hosts); n > 0 {
		rows = append(rows, groupRow{label: "(no group)", path: viewUngrouped, count: n})
	}

	m.groupRows = rows
	if m.groupCursor >= len(rows) {
		m.groupCursor = len(rows) - 1
	}
	if m.groupCursor < 0 {
		m.groupCursor = 0
	}
}

// selectedGroup is the sidebar row the host list is filtered by.
func (m *Model) selectedGroup() groupRow {
	if m.groupCursor < 0 || m.groupCursor >= len(m.groupRows) {
		return groupRow{label: "All", path: viewAll}
	}
	return m.groupRows[m.groupCursor]
}

// inSelectedGroup reports whether a host belongs in the current view.
func (m *Model) inSelectedGroup(h model.Host) bool {
	switch g := m.selectedGroup(); g.path {
	case viewAll, "":
		return true
	case viewFavorites:
		return h.Favorite
	case viewRecent:
		return h.LastUsed > 0
	case viewUngrouped:
		return model.NormaliseGroup(h.Group) == ""
	default:
		return h.InGroup(g.path)
	}
}

// toggleGroup opens or closes a branch of the tree.
func (m *Model) toggleGroup() {
	g := m.selectedGroup()
	if !g.kids {
		return
	}
	m.openGroups[g.path] = !m.openGroups[g.path]
	m.rebuildGroups()
}

// sidebarWidth is how much room the group pane gets, or zero when the window is
// too narrow to spare any. The host list matters more than the tree does.
func (m *Model) sidebarWidth() int {
	if m.hideGroups || m.width < 70 {
		return 0
	}
	w := 22
	for _, r := range m.groupRows {
		if n := runewidth.StringWidth(r.label) + r.depth*2 + 8; n > w {
			w = n
		}
	}
	return clamp(w, 18, m.width/3)
}

// renderSidebar draws the group pane, one string per row. Padding it to a
// width or a height is not its job: joinPanes does that, because doing it by
// hand is what put the two panes two columns out of step.
func (m *Model) renderSidebar(height int) []string {
	w := m.sidebarWidth()
	if w == 0 {
		return nil
	}

	out := make([]string, 0, height)
	start := 0
	if m.groupCursor >= height {
		start = m.groupCursor - height + 1
	}
	for i := start; i < len(m.groupRows) && len(out) < height; i++ {
		r := m.groupRows[i]

		marker := " "
		if r.kids {
			marker = m.gl.closed
			if r.open {
				marker = m.gl.opened
			}
		}
		indent := strings.Repeat("  ", r.depth)
		count := fmt.Sprintf("%d", r.count)

		label := indent + marker + " " + r.label
		room := w - runewidth.StringWidth(count) - 2
		line := " " + pad(label, max(1, room)) + " " + count

		switch {
		case i == m.groupCursor && m.focus == focusGroups:
			line = m.st.selected.Render(line)
		case i == m.groupCursor:
			line = m.st.marked.Render(line)
		default:
			line = m.st.row.Render(line)
		}
		out = append(out, line)
	}
	return out
}

// joinPanes puts the group pane beside the host list.
//
// The layout library does the padding, in both directions. Counting spaces by
// hand is how the two panes ended up misaligned: a row with a group in it and a
// row without were built by different code and came out different widths, so
// every host below the last group sat two columns to the right.
func (m *Model) joinPanes(side, rows []string, height int) string {
	hosts := strings.Join(rows, "\n")
	w := m.sidebarWidth()
	if w == 0 || len(side) == 0 {
		return hosts
	}

	pane := lipgloss.NewStyle().Width(w).Height(height).Render(strings.Join(side, "\n"))

	bars := make([]string, height)
	for i := range bars {
		bars[i] = m.gl.vbar
	}
	bar := lipgloss.NewStyle().Foreground(colMuted).Render(strings.Join(bars, "\n"))

	return lipgloss.JoinHorizontal(lipgloss.Top, pane, bar, hosts)
}
