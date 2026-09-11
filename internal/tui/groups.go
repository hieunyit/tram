package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/hieuny/tram/internal/model"
)

// The three rows that are not groups. They are paths no real group can have,
// because a group path never starts with a colon.
const (
	viewAll       = ":all"
	viewFavorites = ":favorites"
	viewRecent    = ":recent"
	viewMarked    = ":marked"
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
	// The row under the cursor is remembered by name, not by number. Rows come
	// and go as hosts are marked and branches are opened, and an index that
	// survives the rebuild would quietly select a different group.
	was := m.selectedGroup().path

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
	// Marked is a view rather than a group: it is where you go to check what an
	// action is about to be applied to, so it appears as soon as there is one.
	if len(m.marked) > 0 {
		rows = append(rows, groupRow{label: m.gl.marked + " Marked", path: viewMarked, count: len(m.marked)})
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
	m.groupCursor = 0
	for i, r := range rows {
		if r.path == was {
			m.groupCursor = i
			break
		}
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
	case viewMarked:
		return m.marked[h.Name]
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

// groupPaneLines draws the pane the design puts on the left: the tree at the
// top, the fleet's health at the bottom, and whatever room is left between
// them.
func (m *Model) groupPaneLines(height, w, x, y int) []string {
	// The health block is four lines and a rule, and it is only worth the room
	// when the pane is tall enough that the tree does not lose by it.
	healthH := 0
	if height >= 14 {
		healthH = 5
	}
	treeH := height - healthH - 2

	out := []string{" " + m.heading("groups"), ""}
	for _, row := range m.groupTreeLines(treeH, w, x, y+2) {
		out = append(out, row)
	}
	if healthH == 0 {
		return out
	}
	for len(out) < height-healthH {
		out = append(out, "")
	}
	out = append(out,
		" "+m.hrule(w),
		" "+m.heading("fleet health"),
		"")
	if bars := m.fleetBars(w); bars != "" {
		out = append(out, " "+bars)
	} else {
		out = append(out, " "+m.st.faint.Render("press P to measure"))
	}
	out = append(out, " "+m.fleetLine())
	return out
}

// groupTreeLines draws the tree itself, one string per row.
func (m *Model) groupTreeLines(height, w, x, y int) []string {
	if height < 1 {
		return nil
	}
	out := make([]string, 0, height)
	start := 0
	if m.groupCursor >= height {
		start = m.groupCursor - height + 1
	}
	for i := start; i < len(m.groupRows) && len(out) < height; i++ {
		r := m.groupRows[i]
		active := i == m.groupCursor

		at := i
		m.hitAt(x-1, y+len(out), w+1, func() (tea.Model, tea.Cmd) {
			// Clicking a branch that is already selected opens or closes it,
			// which is the only way a mouse can reach the tree's second level.
			if m.groupCursor == at {
				m.toggleGroup()
			}
			m.groupCursor = at
			m.focus = focusGroups
			m.cursor, m.offset = 0, 0
			m.applyFilter()
			return m, nil
		})

		marker := " "
		if r.kids {
			marker = m.gl.closed
			if r.open {
				marker = m.gl.opened
			}
		}
		dot := m.st.unmarked.Render(m.gl.dot)
		if active {
			dot = m.st.ok.Render(m.gl.dot)
		}

		count := fmt.Sprintf("%d", r.count)
		label := strings.Repeat("  ", r.depth) + marker + " " + r.label
		room := max(1, w-runewidth.StringWidth(count)-4)

		name, num := m.st.dim, m.st.faint
		if active {
			name, num = m.st.bright, m.st.ok
		}
		if active && m.focus == focusGroups {
			name = m.st.bright.Underline(true)
		}
		out = append(out, " "+dot+" "+name.Render(pad(label, room))+" "+num.Render(count))
	}
	return out
}
