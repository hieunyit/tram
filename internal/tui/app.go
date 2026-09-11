package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
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
	// ActionWindow means open Host in a new terminal window.
	ActionWindow
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

	result  *resultView
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
	Doctor(hosts []model.Host) []Row
	Exec(hosts []model.Host, command string) []Row
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
		if !h.Matches(q) {
			continue
		}
		// The sessions tab is the hosts you have actually opened. The group
		// tree still narrows it, so sessions inside one group is a question you
		// can ask.
		if m.tab == tabSessions && h.LastUsed == 0 {
			continue
		}
		if m.inSelectedGroup(h) {
			m.filtered = append(m.filtered, h)
		}
	}
	if m.tab == tabSessions && m.sortKey == sortAlias {
		// Opening the tab on an alphabetical list would bury what you did a
		// minute ago somewhere in the middle of it.
		sortByRecent(m.filtered)
	} else {
		m.sortRows()
	}
	if m.cursor >= m.rowCount() {
		m.cursor = max(0, m.rowCount()-1)
	}
}

// sortByRecent puts the most recently opened host first.
func sortByRecent(hs []model.Host) {
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].LastUsed > hs[j].LastUsed })
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

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.measuring = 0
		m.reload()
		m.status = fmt.Sprintf("measured %d host(s), %d answered", msg.count, msg.up)
		return m, nil

	case errMsg:
		// A sweep that failed is a sweep that finished, or the bar goes on
		// claiming it is still measuring for the rest of the run.
		m.measuring = 0
		m.problem = msg.Error()
		return m, nil

	case resultsMsg:
		m.result = newResultView(msg.title, msg.rows, m.st, m.gl)
		m.result.resize(m.width, m.listHeight())
		m.screen = screenResult
		m.mode = modeNormal
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
func results(title string, rows []Row) tea.Cmd {
	return func() tea.Msg { return resultsMsg{title, rows} }
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
