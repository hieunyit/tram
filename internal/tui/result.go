package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
)

// Row is one host's result, in the shape the interface needs. The command layer
// builds these from probe results so the two do not each have their own idea of
// what a failure looks like.
type Row struct {
	Host string
	// Status is the class, already rendered: OK, AUTH, TIMEOUT and so on.
	Status string
	// OK drives the colour and the summary count.
	OK bool
	// Summary is the one-line detail shown next to the host.
	Summary string
	// Body is the full output, shown when the block is expanded.
	Body string
}

// resultView is the second screen: one collapsible block per host, over a
// scrolling area, with enter connecting to whichever host is selected.
type resultView struct {
	title    string
	rows     []Row
	st       styles
	gl       glyphs
	cursor   int
	offset   int
	expanded map[int]bool
	width    int
	height   int
}

func newResultView(title string, rows []Row, st styles, gl glyphs) *resultView {
	return &resultView{title: title, rows: rows, st: st, gl: gl, expanded: map[int]bool{}}
}

func (r *resultView) resize(w, h int) { r.width, r.height = w, h }

func (m *Model) updateResult(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.result
	switch msg.String() {
	case "q", "esc":
		m.screen = screenList
		m.result = nil
		m.pendingImport = nil
		return m, nil
	case "ctrl+c":
		return m.quitWith(ActionQuit, "")
	case "up", "k":
		r.cursor = max(0, r.cursor-1)
	case "down", "j":
		r.cursor = min(len(r.rows)-1, r.cursor+1)
	case " ", "tab":
		r.expanded[r.cursor] = !r.expanded[r.cursor]
	case "o":
		all := true
		for i := range r.rows {
			if !r.expanded[i] {
				all = false
				break
			}
		}
		for i := range r.rows {
			r.expanded[i] = !all
		}
	case "w":
		if m.pendingImport != nil {
			return m, m.applyImport()
		}
	case "enter":
		// An import preview has nothing to connect to yet: the hosts on screen
		// do not exist. So enter writes them instead of opening a session to a
		// name that is not in any configuration file.
		if m.pendingImport != nil {
			return m, m.applyImport()
		}
		if r.cursor >= 0 && r.cursor < len(r.rows) {
			return m.quitWith(ActionConnect, r.rows[r.cursor].Host)
		}
	}
	return m, nil
}

func (m *Model) viewResult() string {
	r := m.result
	bodyH := m.bodyHeight()

	ok := 0
	for _, row := range r.rows {
		if row.OK {
			ok++
		}
	}
	// The tally goes in the bar rather than the border: a title is a path
	// often enough that a count let into the border would be the part that got
	// cut off.
	tally := fmt.Sprintf("%d of %d ok", ok, len(r.rows))
	if p := m.pendingImport; p != nil {
		tally = strings.Join(importTally(p), ", ")
	}

	// Render every line, then window onto the cursor, so an expanded block
	// scrolls the way a reader expects rather than jumping.
	type line struct {
		text string
		row  int
	}
	var lines []line
	for i, row := range r.rows {
		marker := "  "
		if i == r.cursor {
			marker = m.st.selected.Render(m.gl.point) + " "
		}
		badge := m.st.ok.Render(m.gl.check)
		if !row.OK {
			badge = m.st.bad.Render(m.gl.cross)
		}
		status := row.Status
		if !row.OK {
			status = m.st.bad.Render(status)
		}
		head := marker + badge + " " + pad(row.Host, 22) + " " + pad(status, 10) + " " + m.st.muted.Render(row.Summary)
		lines = append(lines, line{head, i})

		if r.expanded[i] && row.Body != "" {
			for _, l := range strings.Split(strings.TrimRight(row.Body, "\n"), "\n") {
				lines = append(lines, line{"      " + l, i})
			}
		}
	}

	h := max(3, bodyH-3)
	// Keep the cursor's own header line on screen.
	cursorLine := 0
	for i, l := range lines {
		if l.row == r.cursor {
			cursorLine = i
			break
		}
	}
	if cursorLine < r.offset {
		r.offset = cursorLine
	}
	if cursorLine >= r.offset+h {
		r.offset = cursorLine - h + 1
	}
	end := min(r.offset+h, len(lines))

	body := make([]string, 0, h)
	for i := r.offset; i < end; i++ {
		body = append(body, lines[i].text)
	}

	keys := [][2]string{{"space", "expand"}, {"o", "expand all"}, {"enter", "connect to this host"}, {"esc", "back"}}
	if p := m.pendingImport; p != nil {
		keys = [][2]string{{"space", "expand"}, {"o", "expand all"},
			{"enter or w", fmt.Sprintf("write %d host(s)", p.Writes())}, {"esc", "cancel"}}
	}

	w := m.width - 2
	out := []string{" " + m.spreadIn(w, m.heading(r.title), m.st.dim.Render(tally)), " " + m.hrule(w), ""}
	for _, l := range body {
		out = append(out, " "+l)
	}
	return m.shell(m.pane(out, m.width, bodyH), keys, tally)
}

// importTally summarises a pending import, naming only the outcomes that
// happened so the header does not read as a row of zeroes.
func importTally(p *inventory.ImportPlan) []string {
	c := p.Counts()
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(c[inventory.ImportAdd], "to add")
	add(c[inventory.ImportUpdate], "to update")
	add(c[inventory.ImportUnchanged], "already match")
	add(c[inventory.ImportSkip], "left alone")
	add(c[inventory.ImportReject], "rejected")
	add(len(p.Skipped), "not reachable over ssh")
	if len(parts) == 0 {
		return []string{"nothing to do"}
	}
	return parts
}

// runOn performs an action across the current selection and switches to the
// result screen.
func (m *Model) runOn(title string, fn func([]model.Host) []Row) (tea.Model, tea.Cmd) {
	sel := m.selection()
	if len(sel) == 0 {
		return m, nil
	}
	// The work is synchronous: these commands finish in seconds and a spinner
	// over a list that cannot be used meanwhile buys nothing.
	return m, results(fmt.Sprintf("%s: %d host(s)", title, len(sel)), fn(sel))
}
