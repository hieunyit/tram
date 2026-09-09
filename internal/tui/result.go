package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	case "enter":
		if r.cursor >= 0 && r.cursor < len(r.rows) {
			return m.quitWith(ActionConnect, r.rows[r.cursor].Host)
		}
	}
	return m, nil
}

func (m *Model) viewResult() string {
	r := m.result
	var b strings.Builder

	ok := 0
	for _, row := range r.rows {
		if row.OK {
			ok++
		}
	}
	b.WriteString(m.st.title.Render(r.title))
	b.WriteString(m.st.muted.Render(fmt.Sprintf("   %d of %d ok", ok, len(r.rows))) + "\n\n")

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
			marker = m.st.selected.Render(m.gl.arrow[:1]) + " "
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

	h := max(3, r.height)
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
	for i := r.offset; i < end; i++ {
		b.WriteString(lines[i].text + "\n")
	}
	for i := end - r.offset; i < h; i++ {
		b.WriteString("\n")
	}

	b.WriteString(m.statusLine() + "\n")
	b.WriteString(m.st.help.Render("space expand  o expand all  enter connect to this host  esc back"))
	return b.String()
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
