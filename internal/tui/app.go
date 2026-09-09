package tui

import (
	"fmt"
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

	search      textinput.Model
	searchQuery string

	form   *form
	picker *picker

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
type Runner interface {
	Ping(hosts []model.Host) []Row
	Doctor(hosts []model.Host) []Row
	Exec(hosts []model.Host, command string) []Row
}

// New builds the interface over an inventory.
func New(inv *inventory.Inventory, r Runner, ascii bool) *Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.CharLimit = 120

	m := &Model{
		inv:    inv,
		Runner: r,
		st:     newStyles(),
		gl:     newGlyphs(ascii),
		marked: map[string]bool{},
		search: ti,
		width:  80,
		height: 24,
	}
	m.reload()
	return m
}

// Outcome returns what the caller should do once the interface has exited.
func (m *Model) Outcome() Outcome { return m.outcome }

func (m *Model) reload() {
	m.hosts = m.inv.Hosts()
	m.applyFilter()
}

func (m *Model) applyFilter() {
	q := strings.TrimSpace(m.searchQuery)
	m.filtered = nil
	for _, h := range m.hosts {
		if h.Matches(q) {
			m.filtered = append(m.filtered, h)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
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

func (m *Model) Init() tea.Cmd { return textinput.Blink }

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

	case errMsg:
		m.problem = msg.Error()
		return m, nil

	case resultsMsg:
		m.result = newResultView(msg.title, msg.rows, m.st, m.gl)
		m.result.resize(m.width, m.listHeight())
		m.screen = screenResult
		m.mode = modeNormal
		return m, nil

	case tea.KeyMsg:
		m.problem = ""
		switch m.mode {
		case modeSearch:
			return m.updateSearch(msg)
		case modeForm:
			return m.updateForm(msg)
		case modePicker:
			return m.updatePicker(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
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
	return m, nil
}

func (m *Model) View() string {
	if m.quit {
		return ""
	}
	switch m.mode {
	case modeHelp:
		return m.viewHelp()
	case modeForm:
		return m.form.view(m.width, m.height)
	case modePicker:
		return m.picker.view(m.width, m.height)
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

func (m *Model) listHeight() int {
	h := m.height - 4 // title, header, status, help
	if m.detail {
		h -= 8
	}
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) statusLine() string {
	if m.problem != "" {
		return m.st.bad.Render(m.problem)
	}
	if m.status != "" {
		return m.st.muted.Render(m.status)
	}
	parts := []string{fmt.Sprintf("%d/%d hosts", len(m.filtered), len(m.hosts))}
	if len(m.marked) > 0 {
		parts = append(parts, fmt.Sprintf("%d marked", len(m.marked)))
	}
	if m.searchQuery != "" {
		parts = append(parts, "filter "+m.searchQuery)
	}
	if m.inv.Managed == nil {
		parts = append(parts, "no managed file; run tram init")
	}
	return m.st.muted.Render(strings.Join(parts, "  "+m.gl.dot+"  "))
}

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
