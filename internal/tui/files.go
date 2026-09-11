package tui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/remote"
)

// The file browser: this side on the left, the far side on the right.
//
// It is the one screen that shows something other than tram's own data, and it
// stays inside the rule the rest of the program follows: nothing here renders a
// session. A listing is the output of `ls` read once, a transfer is scp run
// once, and neither of them owns the terminal.

// filePane is one side of the browser.
type filePane struct {
	dir     string
	entries []remote.Entry
	cursor  int
	offset  int
	marked  map[string]bool
	// want is the name to put the cursor on once the next listing arrives. It
	// is how walking out of a directory leaves you standing on it rather than
	// at the top of a list of its neighbours.
	want string
}

func (p *filePane) at() (remote.Entry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return remote.Entry{}, false
	}
	return p.entries[p.cursor], true
}

// selection is what an action applies to: the marks, or the row under the
// cursor when there are none, which is the same rule the host list follows.
//
// The way up is never part of it. It is a door, not a file, and copying or
// deleting the directory you are standing in is not what anybody meant.
func (p *filePane) selection() []remote.Entry {
	if len(p.marked) == 0 {
		if e, ok := p.at(); ok && !isParent(e) {
			return []remote.Entry{e}
		}
		return nil
	}
	var out []remote.Entry
	for _, e := range p.entries {
		if p.marked[e.Name] && !isParent(e) {
			out = append(out, e)
		}
	}
	return out
}

// isParent reports whether an entry is the way out of this directory.
func isParent(e remote.Entry) bool { return e.Name == ".." }

// withParent puts the way up at the top of a listing.
//
// Neither side lists it: ls -A leaves it out and so does the local read. Nor
// should the keyboard be the only way out of a directory, so the row is put
// there, first, everywhere except a root that has nothing above it.
func withParent(dir string, list []remote.Entry, local bool) []remote.Entry {
	atRoot := dir == "/" || dir == ""
	if local {
		atRoot = filepath.Dir(dir) == dir
	}
	if atRoot {
		return list
	}
	return append([]remote.Entry{{Name: "..", IsDir: true}}, list...)
}

func (p *filePane) move(d, height int) {
	if len(p.entries) == 0 {
		p.cursor = 0
		return
	}
	p.cursor = clamp(p.cursor+d, 0, len(p.entries)-1)
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+height {
		p.offset = p.cursor - height + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// filesView is the whole screen.
type filesView struct {
	host model.Host
	sess FileSystem

	local  filePane
	far    filePane
	onFar  bool
	hidden bool

	busy    string
	problem string
	note    string
}

// ---- opening and closing --------------------------------------------------

type filesOpenedMsg struct {
	host model.Host
	sess FileSystem
	dir  string
	list []remote.Entry
	err  error
}

type filesListedMsg struct {
	far  bool
	dir  string
	list []remote.Entry
	err  error
}

type filesDoneMsg struct {
	note string
	err  error
	// refresh says which side changed and should be read again.
	refreshFar, refreshLocal bool
}

// openFiles starts a shell on the host and reads both sides.
//
// Opening the connection is the slow part, seconds through a jump station, so
// it happens off the main loop like every other thing that talks to a machine.
func (m *Model) openFiles(h model.Host) (tea.Model, tea.Cmd) {
	if m.Runner == nil {
		return m, nil
	}
	runner := m.Runner
	m.status = "opening files on " + h.Name
	return m, func() tea.Msg {
		sess, err := runner.Files(h)
		if err != nil {
			return filesOpenedMsg{host: h, err: err}
		}
		dir, list, err := sess.List("")
		if err != nil {
			_ = sess.Close()
			return filesOpenedMsg{host: h, err: err}
		}
		return filesOpenedMsg{host: h, sess: sess, dir: dir, list: list}
	}
}

// closeFiles ends the connection and goes back to the host list.
func (m *Model) closeFiles() {
	if m.files != nil && m.files.sess != nil {
		_ = m.files.sess.Close()
	}
	m.files = nil
	m.screen = screenList
}

// listLocal reads a directory on this machine.
func listLocal(dir string, hidden bool) (string, []remote.Entry, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return "", nil, err
	}
	var out []remote.Entry
	for _, it := range items {
		name := it.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		e := remote.Entry{Name: name, IsDir: it.IsDir()}
		if info, err := it.Info(); err == nil {
			e.Size = info.Size()
			e.Time = info.ModTime()
			e.Mode = info.Mode().String()
			if info.Mode()&os.ModeSymlink != 0 {
				e.Link = "?"
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return abs, out, nil
}

func (m *Model) localCmd(dir string) tea.Cmd {
	hidden := m.files.hidden
	return func() tea.Msg {
		abs, list, err := listLocal(dir, hidden)
		return filesListedMsg{far: false, dir: abs, list: list, err: err}
	}
}

func (m *Model) farCmd(dir string) tea.Cmd {
	sess := m.files.sess
	return func() tea.Msg {
		cwd, list, err := sess.List(dir)
		return filesListedMsg{far: true, dir: cwd, list: list, err: err}
	}
}

// ---- keys -----------------------------------------------------------------

func (m *Model) updateFiles(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.files
	side := &f.local
	if f.onFar {
		side = &f.far
	}
	h := m.filesRows()

	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.closeFiles()
		return m, nil

	case "tab", "left", "right", "h", "l":
		f.onFar = !f.onFar
	case "up", "k", "ctrl+p":
		side.move(-1, h)
	case "down", "j", "ctrl+n":
		side.move(1, h)
	case "pgup":
		side.move(-h, h)
	case "pgdown":
		side.move(h, h)
	case "home":
		side.cursor, side.offset = 0, 0
	case "end":
		side.move(len(side.entries), h)

	case "enter":
		e, ok := side.at()
		if !ok || !e.IsDir {
			return m, nil
		}
		return m, m.openDir(f.onFar, e.Name)
	case "backspace", "u":
		return m, m.openDir(f.onFar, "..")

	case " ":
		if e, ok := side.at(); ok && !isParent(e) {
			if side.marked[e.Name] {
				delete(side.marked, e.Name)
			} else {
				side.marked[e.Name] = true
			}
			side.move(1, h)
		}

	case "c", "f5":
		return m.copySelection()

	case "n":
		m.openMkdirForm()
	case "r":
		if e, ok := side.at(); ok && !isParent(e) {
			m.openRenameForm(e.Name)
		}
	case "d":
		return m.confirmFileDelete()

	case ".":
		f.hidden = !f.hidden
		return m, m.localCmd(f.local.dir)
	case "R":
		return m, tea.Batch(m.localCmd(f.local.dir), m.farCmd("."))
	}
	return m, nil
}

// openDir walks into a directory on one side, or out of this one.
func (m *Model) openDir(far bool, name string) tea.Cmd {
	f := m.files
	side := &f.local
	if far {
		side = &f.far
	}
	// Going up lands on the directory just left, which is where the eye is.
	if name == ".." {
		side.want = path.Base(strings.ReplaceAll(side.dir, `\`, "/"))
	}
	if far {
		f.busy = "reading " + name
		return m.farCmd(name)
	}
	return m.localCmd(filepath.Join(f.local.dir, name))
}

// filesRows is how many rows of names each side shows.
func (m *Model) filesRows() int { return max(1, m.bodyHeight()-3) }

// copySelection sends what is marked to the other side.
func (m *Model) copySelection() (tea.Model, tea.Cmd) {
	f := m.files
	side := &f.local
	if f.onFar {
		side = &f.far
	}
	items := side.selection()
	if len(items) == 0 {
		return m, nil
	}

	var jobs []remote.Copy
	for _, e := range items {
		jobs = append(jobs, remote.Copy{
			Host:   f.host.Name,
			Local:  filepath.Join(f.local.dir, e.Name),
			Remote: path.Join(f.far.dir, e.Name),
			Up:     !f.onFar,
			Dir:    e.IsDir,
		})
	}

	what := fmt.Sprintf("copy %d item(s) to %s?", len(jobs), sideName(!f.onFar, f.host.Name))
	if len(jobs) == 1 {
		what = "copy " + items[0].Name + " to " + sideName(!f.onFar, f.host.Name) + "?"
	}
	return m.confirm(what, func() tea.Cmd {
		f.busy = fmt.Sprintf("copying %d item(s)", len(jobs))
		runner := m.Runner
		toFar := !f.onFar
		return func() tea.Msg {
			for _, j := range jobs {
				if err := runner.Copy(j); err != nil {
					return filesDoneMsg{err: err, refreshFar: toFar, refreshLocal: !toFar}
				}
			}
			return filesDoneMsg{
				note:         fmt.Sprintf("copied %d item(s)", len(jobs)),
				refreshFar:   toFar,
				refreshLocal: !toFar,
			}
		}
	})
}

func sideName(far bool, host string) string {
	if far {
		return host
	}
	return "this machine"
}

// confirmFileDelete asks before removing anything, on either side.
func (m *Model) confirmFileDelete() (tea.Model, tea.Cmd) {
	f := m.files
	side := &f.local
	if f.onFar {
		side = &f.far
	}
	items := side.selection()
	if len(items) == 0 {
		return m, nil
	}

	var names []string
	for _, e := range items {
		names = append(names, e.Name)
	}
	where := sideName(f.onFar, f.host.Name)
	return m.confirm(fmt.Sprintf("delete %s on %s?", strings.Join(names, ", "), where), func() tea.Cmd {
		onFar := f.onFar
		dir := f.local.dir
		sess := f.sess
		return func() tea.Msg {
			for _, e := range items {
				var err error
				if onFar {
					err = sess.Remove(e.Name, e.IsDir)
				} else {
					err = os.RemoveAll(filepath.Join(dir, e.Name))
				}
				if err != nil {
					return filesDoneMsg{err: err, refreshFar: onFar, refreshLocal: !onFar}
				}
			}
			return filesDoneMsg{
				note:         fmt.Sprintf("deleted %d item(s)", len(items)),
				refreshFar:   onFar,
				refreshLocal: !onFar,
			}
		}
	})
}

// submitFileName performs the two forms the browser opens: a new folder, and a
// rename. Both are one name typed into one box, on whichever side is active.
func (m *Model) submitFileName() (tea.Model, tea.Cmd) {
	f, v := m.form, m.form.get(fPathTo)
	if v == "" {
		f.problem = "a name is needed"
		return m, nil
	}
	if strings.ContainsAny(v, `/\`) {
		f.problem = "a name, not a path"
		return m, nil
	}
	kind := f.kind
	old := ""
	if kind == formRename {
		old = string(f.fields[0].id)
		old = f.fields[0].label
	}

	fv := m.files
	onFar := fv.onFar
	dir := fv.local.dir
	sess := fv.sess

	m.mode = modeNormal
	m.form = nil
	fv.busy = "working"

	return m, func() tea.Msg {
		var err error
		switch {
		case kind == formMkdir && onFar:
			err = sess.Mkdir(v)
		case kind == formMkdir:
			err = os.Mkdir(filepath.Join(dir, v), 0o755)
		case onFar:
			err = sess.Rename(old, v)
		default:
			err = os.Rename(filepath.Join(dir, old), filepath.Join(dir, v))
		}
		if err != nil {
			return filesDoneMsg{err: err, refreshFar: onFar, refreshLocal: !onFar}
		}
		return filesDoneMsg{note: v, refreshFar: onFar, refreshLocal: !onFar}
	}
}

// ---- drawing --------------------------------------------------------------

func (m *Model) viewFiles() string {
	f := m.files
	bodyH := m.bodyHeight()
	half := (m.width - 1) / 2
	right := m.width - 1 - half

	body := m.joinPanes(bodyH,
		m.pane(m.filePaneLines(&f.local, false, bodyH, half-2, 1, 2), half, bodyH),
		m.pane(m.filePaneLines(&f.far, true, bodyH, right-2, half+2, 2), right, bodyH),
	)
	return m.shell(body, m.filesKeys(), m.filesStatus())
}

func (m *Model) filePaneLines(p *filePane, far bool, height, w, x, y int) []string {
	f := m.files
	title := m.heading("this machine")
	if far {
		title = m.heading(f.host.Name)
	}
	active := far == f.onFar
	if active {
		title += " " + m.st.ok.Render(m.gl.point)
	}

	out := []string{
		" " + m.spreadIn(w, title, m.st.faint.Render(fmt.Sprintf("%d", len(p.entries)))),
		" " + m.st.value.Render(pad(tailPath(p.dir, w), w)),
		" " + m.hrule(w),
	}

	rows := height - 3
	if len(p.entries) == 0 {
		return append(out, "   "+m.st.dim.Render("empty"))
	}
	end := min(p.offset+rows, len(p.entries))
	for i := p.offset; i < end; i++ {
		e := p.entries[i]
		sel := i == p.cursor && active

		at, isFar := i, far
		m.hitAt(x, y+3+i-p.offset, w, func() (tea.Model, tea.Cmd) {
			m.files.onFar = isFar
			side := &m.files.local
			if isFar {
				side = &m.files.far
			}
			side.cursor = at
			if m.doubleClick && side.entries[at].IsDir {
				return m, m.openDir(isFar, side.entries[at].Name)
			}
			return m, nil
		})
		out = append(out, " "+m.fileRow(e, p, sel, w))
	}
	return out
}

func (m *Model) fileRow(e remote.Entry, p *filePane, sel bool, w int) string {
	// name, then size and time on the right, with the name giving ground last.
	const sizeW, timeW = 8, 12
	nameW := max(6, w-sizeW-timeW-6)

	bar := " "
	if sel {
		bar = m.st.rowBar.Render(m.gl.bar)
	}
	// The way up carries no box: there is nothing about it to mark, and a box
	// invites a click that would do nothing.
	box := " "
	if !isParent(e) {
		box = m.st.onRow(m.st.unmarked, sel).Render(m.gl.unmarked)
		if p.marked[e.Name] {
			box = m.st.onRow(m.st.marked, sel).Render(m.gl.marked)
		}
	}

	name := e.Name
	style := m.st.onRow(m.st.row, sel)
	switch {
	case sel:
		style = m.st.selected
	case isParent(e):
		style = m.st.onRow(m.st.faint, sel)
	case e.IsDir:
		style = m.st.onRow(m.st.ok, sel)
	case e.Link != "":
		style = m.st.onRow(m.st.warn, sel)
	}
	if e.IsDir {
		name += "/"
	}

	size := ""
	when := ""
	if !e.IsDir {
		size = humanSize(e.Size)
	}
	if !e.Time.IsZero() {
		when = e.Time.Format("2006-01-02")
	}

	line := bar + " " + box + " " + style.Render(pad(name, nameW)) +
		m.st.onRow(m.st.faint, sel).Render(" "+padLeft(size, sizeW)+"  "+pad(when, timeW))
	if sel {
		line = m.st.selected.Render(ansiPad(line, w))
	}
	return line
}

// tailPath keeps the end of a path rather than the start.
//
// The other shortener keeps the last segment alone, which is right for a bar
// naming a config file and wrong here: in a file browser the three directories
// above you are the ones that say where you are.
func tailPath(p string, w int) string {
	if w <= 1 || len(p) <= w {
		return p
	}
	cut := p[len(p)-(w-1):]
	if i := strings.IndexAny(cut, `/\`); i >= 0 {
		cut = cut[i:]
	}
	return "…" + cut
}

// humanSize writes a byte count the way a file manager does.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0fk", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
}

func (m *Model) filesKeys() [][2]string {
	if m.mode == modeConfirm {
		return [][2]string{{"y", "yes"}, {"n", "no"}, {"esc", "cancel"}}
	}
	to := "to " + m.files.host.Name
	if m.files.onFar {
		to = "to this machine"
	}
	return [][2]string{
		{"c", "copy " + to}, {m.gl.enter, "open"}, {"u", "up"}, {"tab", "other side"},
		{"space", "mark"}, {"n", "new folder"}, {"r", "rename"}, {"d", "delete"},
		{".", "hidden"}, {"R", "refresh"}, {"esc", "back"},
	}
}

func (m *Model) filesStatus() string {
	f := m.files
	switch {
	case m.mode == modeConfirm:
		return m.confirmText + "  [y/n]"
	case f.problem != "":
		return f.problem
	case f.busy != "":
		return f.busy + m.gl.ellipsis
	case f.note != "":
		return f.note
	}
	if n := len(f.local.marked) + len(f.far.marked); n > 0 {
		return fmt.Sprintf("%s %s %d marked", f.host.Name, m.gl.dot, n)
	}
	return f.host.Name
}
