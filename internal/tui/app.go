package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/remote"
)

// Action is what the caller should do after the interface exits.
//
// Opening a session is not something the interface can do itself: it has to
// give the terminal back first. So the model quits, names what it wants, and
// the command layer performs it and starts the interface again.
type Action int

const (
	// ActionQuit means the user is finished.
	ActionQuit Action = iota
	// ActionConnect means hand the terminal to ssh for Host.
	ActionConnect
	// ActionSFTP means hand the terminal to sftp for Host.
	ActionSFTP
)

// Outcome is what the interface asks the caller to do next.
type Outcome struct {
	Action Action
	Host   string
}

// pane is which half of the list screen the keyboard is driving.
type pane int

const (
	focusHosts pane = iota
	focusGroups
)

// screen is which of the two views is showing.
type screen int

const (
	screenList screen = iota
	screenResult
	screenFiles
)

// mode is the overlay on top of the current screen.
type mode int

const (
	modeNormal mode = iota
	modeSearch
	modeForm
	modePicker
	modeConfirm
	modeHelp
	modeBusy
	// The two overlays. Both are drawn over the screen rather than instead of
	// it, because both are about the row you were already looking at.
	modePalette
	modeMenu
)

// Model is the whole interface. It holds one inventory and reloads it whenever
// a write happens, so the list never shows a stale file.
type Model struct {
	inv *inventory.Inventory

	// Runner performs the work the interface cannot: probing hosts and running
	// commands. It is supplied by the command layer so that the interface and
	// the command line share one implementation.
	Runner Runner

	st styles
	gl glyphs

	width, height int

	screen screen
	mode   mode

	hosts    []model.Host
	filtered []model.Host
	cursor   int
	offset   int
	marked   map[string]bool
	detail   bool

	// tab is which of the three tables is showing, and sortKey and sortDir the
	// column it is ordered by. The keys tab lists identities rather than hosts,
	// so it keeps its own rows.
	tab      tab
	sortKey  sortKey
	sortDir  int
	accounts []model.Account

	// agent is what ssh-add said about the running agent, empty until it has
	// been asked.
	agent string

	// The command palette and the context menu.
	paletteQuery  string
	paletteCursor int
	menuItems     []command
	menuCursor    int
	menuX, menuY  int

	// hits is what the last frame drew that can be clicked. It is rebuilt by
	// every View, so a click can only ever land on something still on screen.
	hits      []hit
	lastClick clickAt
	// doubleClick says whether the press being handled is the second of two in
	// the same place, for the strips where that means something.
	doubleClick bool

	// mouse is whether the user wants the pointer at all, and mouseOn whether
	// the terminal is reporting it at this moment. They differ while a box is
	// taking typing: see syncMouse.
	mouse   bool
	mouseOn bool

	// running names the fleet command working in the background, so that the
	// bar can say what the interface is waiting for rather than going quiet.
	running string

	// measuring is how many hosts a sweep is still working through, or zero.
	// The sweep runs off the main loop, so the interface stays usable while
	// hundreds of connections are attempted.
	measuring int

	// The group pane on the left, and which of the two panes the keyboard is
	// talking to.
	groupRows   []groupRow
	groupCursor int
	openGroups  map[string]bool
	focus       pane
	hideGroups  bool

	search      textinput.Model
	searchQuery string

	form   *form
	picker *picker
	// formStack holds the forms a form was opened from, so that making an
	// account in the middle of adding a host returns to the host.
	formStack []*form

	confirmText string
	confirmFn   func() tea.Cmd

	result *resultView

	// files is the two-pane browser, open only while it is showing. It holds a
	// live connection to one host, which is closed when it goes away.
	files *filesView

	status  string
	problem string

	// pendingImport holds a plan that has been previewed but not written. It
	// takes a second, explicit key to apply, because an import can create
	// dozens of hosts at once.
	pendingImport *inventory.ImportPlan

	outcome Outcome
	quit    bool
}

// Runner is the work the command layer does on the interface's behalf.
//
// Measure replaced a plain ping: the design shows latency, load and the
// operating system in the table and the details, and one ssh round trip can
// answer all three, so asking twice would be asking twice for nothing.
type Runner interface {
	Measure(hosts []model.Host) []Measurement
	// Agent reports what the ssh agent holds, in a few words, or says that
	// there is not one.
	Agent() string
	// Open puts hosts in front of the user without tram giving up the screen:
	// a tab each, or a pane beside the list. It says what it did, because what
	// a terminal can do varies and the answer is worth a line in the bar.
	Open(hosts []model.Host, beside bool) (string, error)
	// Files opens a connection to a host for the two-pane browser, and Copy
	// moves one file or folder in either direction.
	Files(host model.Host) (FileSystem, error)
	Copy(job remote.Copy) error
	Doctor(hosts []model.Host) []Row
	Exec(hosts []model.Host, command string) []Row
}

// FileSystem is the far side of the browser: one open connection, asked for
// listings and for the few changes a file manager makes.
//
// It is an interface rather than the connection itself so that the browser can
// be driven without a machine at the other end, which is the only way any of
// this is testable.
type FileSystem interface {
	// List changes to a directory and reads it, returning where it ended up.
	List(path string) (string, []remote.Entry, error)
	Mkdir(path string) error
	Rename(from, to string) error
	Remove(path string, dir bool) error
	Close() error
}

// New builds the interface over an inventory.
func New(inv *inventory.Inventory, r Runner, ascii bool) *Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.CharLimit = 120

	m := &Model{
		inv:        inv,
		Runner:     r,
		st:         newStyles(),
		gl:         newGlyphs(ascii),
		marked:     map[string]bool{},
		search:     ti,
		width:      80,
		height:     24,
		openGroups: map[string]bool{},
		// The details pane is part of the layout rather than something to go
		// looking for; i takes it away when the window is wanted for the table.
		detail:  true,
		sortDir: 1,

		// The same setting the command layer reads to decide whether to ask
		// the terminal for mouse reporting at all.
		mouse:   inv.Store.Options.MouseOn(),
		mouseOn: inv.Store.Options.MouseOn(),
	}
	m.reload()
	return m
}

// Outcome returns what the caller should do once the interface has exited.
func (m *Model) Outcome() Outcome { return m.outcome }

func (m *Model) reload() {
	m.hosts = m.inv.Hosts()
	m.accounts = m.inv.Store.AccountList()
	m.rebuildGroups()
	m.applyFilter()
}

func (m *Model) applyFilter() {
	q := strings.TrimSpace(m.searchQuery)
	m.filtered = nil
	for _, h := range m.hosts {
		if h.Matches(q) && m.inSelectedGroup(h) {
			m.filtered = append(m.filtered, h)
		}
	}
	m.sortRows()
	if m.cursor >= m.rowCount() {
		m.cursor = max(0, m.rowCount()-1)
	}
}

// current returns the host under the cursor.
func (m *Model) current() (model.Host, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return model.Host{}, false
	}
	return m.filtered[m.cursor], true
}

// selection returns the marked hosts, or the one under the cursor when nothing
// is marked, which is what every action operates on.
func (m *Model) selection() []model.Host {
	if len(m.marked) == 0 {
		if h, ok := m.current(); ok {
			return []model.Host{h}
		}
		return nil
	}
	var out []model.Host
	for _, h := range m.filtered {
		if m.marked[h.Name] {
			out = append(out, h)
		}
	}
	return out
}

func (m *Model) Init() tea.Cmd { return tea.Batch(textinput.Blink, m.checkAgent()) }

// Update handles one message and then decides who should own the mouse.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	mm, cmd := m.update(msg)
	if hand := m.syncMouse(); hand != nil {
		return mm, tea.Batch(cmd, hand)
	}
	return mm, cmd
}

// syncMouse hands the mouse back to the terminal while a box is taking typing.
//
// A captured mouse is a mouse the terminal cannot use for its own pasting and
// selecting, and the one moment that matters most is while you are filling in
// an address you copied from somewhere else. So the pointer belongs to tram
// over a list, and to the terminal over a form.
func (m *Model) syncMouse() tea.Cmd {
	if !m.mouse {
		return nil
	}
	want := !m.typing()
	if want == m.mouseOn {
		return nil
	}
	m.mouseOn = want
	if want {
		return tea.EnableMouseCellMotion
	}
	return tea.DisableMouse
}

// typing reports whether something on screen is taking keystrokes as text.
func (m *Model) typing() bool {
	switch m.mode {
	case modeForm, modeSearch, modePicker, modePalette:
		return true
	}
	return false
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.result != nil {
			m.result.resize(m.width, m.listHeight())
		}
		return m, nil

	case reloadMsg:
		m.reload()
		m.status = string(msg)
		return m, nil

	case agentMsg:
		m.agent = string(msg)
		return m, nil

	case measuredMsg:
		// No note: the standing tally in the bar is the result of the sweep,
		// and a note saying the same thing in different words would sit on top
		// of it until the next keystroke.
		m.measuring = 0
		m.status = ""
		m.reload()
		return m, nil

	case errMsg:
		// A sweep that failed is a sweep that finished, or the bar goes on
		// claiming it is still measuring for the rest of the run.
		m.measuring = 0
		m.problem = msg.Error()
		return m, nil

	case filesOpenedMsg:
		m.status = ""
		if msg.err != nil {
			m.problem = msg.err.Error()
			return m, nil
		}
		m.files = &filesView{
			host: msg.host,
			sess: msg.sess,
			far: filePane{
				dir:     msg.dir,
				entries: withParent(msg.dir, msg.list, false),
				marked:  map[string]bool{},
			},
			local: filePane{marked: map[string]bool{}},
		}
		m.screen = screenFiles
		here, _ := os.Getwd()
		return m, m.localCmd(here)

	case filesListedMsg:
		if m.files == nil {
			return m, nil
		}
		m.files.busy = ""
		if msg.err != nil {
			m.files.problem = msg.err.Error()
			return m, nil
		}
		m.files.problem = ""
		side := &m.files.local
		if msg.far {
			side = &m.files.far
		}
		// A new directory is a new list: the marks go away, because they named
		// files that are no longer on screen.
		side.dir = msg.dir
		side.entries = withParent(msg.dir, msg.list, !msg.far)
		side.cursor, side.offset = 0, 0
		side.marked = map[string]bool{}
		if side.want != "" {
			for i, e := range side.entries {
				if e.Name == side.want {
					side.cursor = i
					break
				}
			}
			side.want = ""
			side.move(0, m.filesRows())
		}
		return m, nil

	case filesDoneMsg:
		if m.files == nil {
			return m, nil
		}
		m.files.busy = ""
		m.files.problem = ""
		m.files.note = msg.note
		if msg.err != nil {
			m.files.problem = msg.err.Error()
		}
		var cmds []tea.Cmd
		if msg.refreshLocal {
			cmds = append(cmds, m.localCmd(m.files.local.dir))
		}
		if msg.refreshFar {
			cmds = append(cmds, m.farCmd("."))
		}
		return m, tea.Batch(cmds...)

	case resultsMsg:
		m.result = newResultView(msg.title, msg.rows, m.st, m.gl)
		m.result.resize(m.width, m.listHeight())
		m.screen = screenResult
		m.mode = modeNormal
		m.running = ""
		return m, nil

	case tea.MouseMsg:
		return m.updateMouse(msg)

	case tea.KeyMsg:
		m.problem = ""
		// ctrl+k opens the palette from anywhere the list is showing, which is
		// what the design's ⌘K does.
		if msg.String() == "ctrl+k" && m.screen == screenList && (m.mode == modeNormal || m.mode == modePalette) {
			if m.mode == modePalette {
				m.closeOverlay()
				return m, nil
			}
			return m.openPalette()
		}
		switch m.mode {
		case modeSearch:
			return m.updateSearch(msg)
		case modeForm:
			return m.updateForm(msg)
		case modePicker:
			return m.updatePicker(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		case modePalette:
			return m.updatePalette(msg)
		case modeMenu:
			return m.updateMenu(msg)
		case modeHelp:
			m.mode = modeNormal
			return m, nil
		case modeBusy:
			return m, nil
		}
		if m.screen == screenResult {
			return m.updateResult(msg)
		}
		if m.screen == screenFiles {
			return m.updateFiles(msg)
		}
		return m.updateList(msg)
	}
	return m.forwardToFocused(msg)
}

// forwardToFocused hands a message the interface does not know about to
// whatever is currently taking typing.
//
// The text input turns ctrl+v into a command that reads the clipboard, and that
// command's answer comes back as a message of the widget's own. Handling only
// key events meant the answer was dropped on the floor and the paste silently
// did nothing. It is also what makes the cursor blink.
func (m *Model) forwardToFocused(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch {
	case m.mode == modeForm && m.form != nil:
		f := m.form
		if f.cursor < 0 || f.cursor >= len(f.fields) {
			return m, nil
		}
		var cmd tea.Cmd
		f.fields[f.cursor].input, cmd = f.fields[f.cursor].input.Update(msg)
		return m, cmd
	case m.mode == modeSearch:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.searchQuery = m.search.Value()
		m.applyFilter()
		return m, cmd
	}
	return m, nil
}

func (m *Model) View() string {
	if m.quit {
		return ""
	}
	// The hit map belongs to the frame about to be drawn, so it starts empty
	// and is filled in by whatever actually reaches the screen.
	m.clearHits()

	switch m.mode {
	case modeHelp:
		return m.viewHelp()
	case modeForm:
		return m.viewForm()
	case modePicker:
		return m.viewPicker()
	case modePalette:
		return m.viewPalette(m.viewList())
	case modeMenu:
		return m.viewMenu(m.viewList())
	}
	if m.screen == screenResult {
		return m.viewResult()
	}
	if m.screen == screenFiles {
		return m.viewFiles()
	}
	return m.viewList()
}

// ---- messages -------------------------------------------------------------

type reloadMsg string
type errMsg struct{ error }
type resultsMsg struct {
	title string
	rows  []Row
}

func fail(err error) tea.Cmd { return func() tea.Msg { return errMsg{err} } }
func note(s string) tea.Cmd  { return func() tea.Msg { return reloadMsg(s) } }

// results carries rows that already exist.
func results(title string, rows []Row) tea.Cmd {
	return func() tea.Msg { return resultsMsg{title, rows} }
}

// resultsFrom runs the work off the main loop and shows what it returns.
//
// The work is ssh to every host in the selection, which is seconds at best and
// a hung connection at worst. Running it inline froze the whole interface for
// the duration, which looks exactly like a program that has crashed.
func resultsFrom(title string, run func() []Row) tea.Cmd {
	return func() tea.Msg { return resultsMsg{title, run()} }
}

// ---- shared chrome --------------------------------------------------------

func (m *Model) quitWith(a Action, host string) (tea.Model, tea.Cmd) {
	m.outcome = Outcome{Action: a, Host: host}
	m.quit = true
	return m, tea.Quit
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
