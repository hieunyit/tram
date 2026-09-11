package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The command palette and the context menu.
//
// Neither of them does any work of its own. Every entry replays the key that
// already performs it, so there is one implementation of connecting, deleting
// and importing, and a menu item cannot drift away from the key it claims to be.

// command is one line of the palette or the menu.
type command struct {
	label string
	// key is the keystroke this entry replays, and what is drawn on the right.
	key string
	// sep marks a divider rather than a command.
	sep bool
}

// keyMsg builds the message a terminal would send for a keystroke, so that a
// menu entry goes through exactly the code a key press goes through.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// commands is everything the palette offers, in the order it offers them: what
// you do to one host first, then to many, then to the file, then to the program.
func (m *Model) commands() []command {
	name := "the selection"
	if h, ok := m.current(); ok {
		name = h.Name
	}
	// What a fleet action is about to touch, said the way the status bar says
	// it: the marks when there are any, and the row under the cursor when there
	// are not.
	targets := name
	if n := len(m.marked); n > 0 {
		targets = fmt.Sprintf("%d marked host(s)", n)
	}

	return []command{
		{label: "Connect to " + name, key: "enter"},
		{label: "Open SFTP on " + name, key: "f"},
		{label: "Open " + name + " in a new window", key: "W"},
		{label: "Copy the ssh command for " + name, key: "y"},
		{label: "Edit " + name, key: "e"},
		{label: "Clone " + name, key: "c"},
		{label: "Delete " + name, key: "d"},
		{sep: true},
		{label: "Add a new host", key: "a"},
		{label: "Edit " + targets + " at once", key: "E"},
		{label: "Link the selection to an identity", key: "A"},
		{label: "Run a command on " + targets, key: "x"},
		{label: "Run a saved snippet", key: "r"},
		{sep: true},
		{label: "Measure the selection", key: "p"},
		{label: "Measure everything shown", key: "P"},
		{label: "Diagnose the route with doctor", key: "D"},
		{sep: true},
		{label: "Import an inventory or a CSV export", key: "I"},
		{label: "Re-read ssh_config from disk", key: "R"},
		{label: "Change the sort column", key: "s"},
		{label: "Pin or unpin " + name, key: "*"},
		{sep: true},
		{label: "Show the keys", key: "?"},
		{label: "Quit", key: "q"},
	}
}

// paletteVisible is the commands matching what has been typed.
func (m *Model) paletteVisible() []command {
	q := strings.ToLower(strings.TrimSpace(m.paletteQuery))
	var out []command
	for _, c := range m.commands() {
		if c.sep {
			// A divider only earns its line in an unfiltered list.
			if q == "" {
				out = append(out, c)
			}
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(c.label+" "+c.key), q) {
			out = append(out, c)
		}
	}
	return out
}

func (m *Model) openPalette() (tea.Model, tea.Cmd) {
	m.mode = modePalette
	m.paletteQuery = ""
	m.paletteCursor = 0
	return m, nil
}

func (m *Model) closeOverlay() {
	m.mode = modeNormal
	m.paletteQuery = ""
	m.menuItems = nil
}

func (m *Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.paletteVisible()
	switch msg.String() {
	case "esc", "ctrl+k", "ctrl+c":
		m.closeOverlay()
		return m, nil
	case "up", "ctrl+p":
		m.paletteCursor = m.paletteStep(items, -1)
	case "down", "ctrl+n":
		m.paletteCursor = m.paletteStep(items, 1)
	case "enter":
		return m.runPalette()
	case "backspace":
		if m.paletteQuery != "" {
			m.paletteQuery = m.paletteQuery[:len(m.paletteQuery)-1]
			m.paletteCursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.paletteQuery += string(msg.Runes)
			m.paletteCursor = 0
		} else if msg.Type == tea.KeySpace {
			m.paletteQuery += " "
			m.paletteCursor = 0
		}
	}
	return m, nil
}

// paletteStep moves the cursor and steps over dividers, which are not choices.
func (m *Model) paletteStep(items []command, d int) int {
	i := m.paletteCursor
	for n := 0; n < len(items); n++ {
		i = clamp(i+d, 0, len(items)-1)
		if !items[i].sep {
			return i
		}
		if i == 0 || i == len(items)-1 {
			break
		}
	}
	return m.paletteCursor
}

func (m *Model) runPalette() (tea.Model, tea.Cmd) {
	items := m.paletteVisible()
	if m.paletteCursor < 0 || m.paletteCursor >= len(items) {
		m.closeOverlay()
		return m, nil
	}
	c := items[m.paletteCursor]
	m.closeOverlay()
	if c.sep {
		return m, nil
	}
	return m.updateList(keyMsg(c.key))
}

// ---- the context menu -----------------------------------------------------

// openMenu puts the menu where the pointer is, nudged so that it never hangs
// off the bottom or the right of the window.
func (m *Model) openMenu(x, y int) (tea.Model, tea.Cmd) {
	if m.tab == tabKeys {
		m.menuItems = []command{
			{label: "Edit this key", key: "e"},
			{label: "New key", key: "a"},
		}
	} else {
		name := ""
		if h, ok := m.current(); ok {
			name = h.Name
		}
		if name == "" {
			return m, nil
		}
		m.menuItems = []command{
			{label: "Connect", key: "enter"},
			{label: "Open SFTP", key: "f"},
			{label: "New window", key: "W"},
			{label: "Copy ssh command", key: "y"},
			{sep: true},
			{label: "Edit", key: "e"},
			{label: "Clone", key: "c"},
			{label: "Mark", key: "space"},
			{label: "Delete", key: "d"},
		}
	}

	w, h := menuWidth, len(m.menuItems)+2
	m.menuX = min(x, max(0, m.width-w))
	m.menuY = min(y, max(0, m.height-h-1))
	m.menuCursor = 0
	m.mode = modeMenu
	return m, nil
}

const menuWidth = 26

func (m *Model) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.closeOverlay()
		return m, nil
	case "up", "k":
		m.menuCursor = max(0, m.menuCursor-1)
		if m.menuItems[m.menuCursor].sep {
			m.menuCursor = max(0, m.menuCursor-1)
		}
	case "down", "j":
		m.menuCursor = min(len(m.menuItems)-1, m.menuCursor+1)
		if m.menuItems[m.menuCursor].sep {
			m.menuCursor = min(len(m.menuItems)-1, m.menuCursor+1)
		}
	case "enter":
		return m.runMenu(m.menuCursor)
	}
	return m, nil
}

func (m *Model) runMenu(i int) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.menuItems) {
		m.closeOverlay()
		return m, nil
	}
	c := m.menuItems[i]
	m.closeOverlay()
	if c.sep {
		return m, nil
	}
	return m.updateList(keyMsg(c.key))
}

// ---- drawing --------------------------------------------------------------

// viewPalette composites the palette over whatever was showing.
func (m *Model) viewPalette(base string) string {
	items := m.paletteVisible()
	w := clamp(m.width-8, 30, 72)
	rows := clamp(m.height-8, 4, 16)

	// Keep the cursor in view without moving it.
	start := 0
	if m.paletteCursor >= rows {
		start = m.paletteCursor - rows + 1
	}
	end := min(start+rows, len(items))

	x := (m.width - w) / 2
	y := max(1, (m.height-rows-4)/2)

	var lines []string
	q := m.paletteQuery
	if q == "" {
		q = m.st.faint.Render("type to filter")
	} else {
		q = m.st.value.Render(q)
	}
	lines = append(lines, m.st.ok.Render("> ")+q, m.hrule(w-4))

	if len(items) == 0 {
		lines = append(lines, m.st.faint.Render("nothing matches"))
	}
	for i := start; i < end; i++ {
		c := items[i]
		if c.sep {
			lines = append(lines, m.hrule(w-4))
			continue
		}
		row := m.spreadIn(w-4, " "+m.st.value.Render(c.label), m.st.key.Render(" "+c.key+" "))
		if i == m.paletteCursor {
			row = m.st.selected.Render(ansiPad(" "+c.label, w-4-len(c.key)-3)) + m.st.key.Render(" "+c.key+" ")
		}
		lines = append(lines, row)

		// The line inside the box starts one column in from its border.
		idx := i
		m.hitAt(x+2, y+1+len(lines)-1, w-4, func() (tea.Model, tea.Cmd) {
			m.paletteCursor = idx
			return m.runPalette()
		})
	}

	box := m.box("COMMANDS", lines, w)
	return overlay(base, box, x, y)
}

// viewMenu composites the context menu over whatever was showing.
func (m *Model) viewMenu(base string) string {
	var lines []string
	for i, c := range m.menuItems {
		if c.sep {
			lines = append(lines, m.hrule(menuWidth-4))
			continue
		}
		row := m.spreadIn(menuWidth-4, m.st.value.Render(c.label), m.st.key.Render(" "+c.key+" "))
		if i == m.menuCursor {
			row = m.st.selected.Render(ansiPad(c.label, menuWidth-4-len(c.key)-3)) + m.st.key.Render(" "+c.key+" ")
		}
		lines = append(lines, row)

		idx := i
		m.hitAt(m.menuX+2, m.menuY+1+len(lines)-1, menuWidth-4, func() (tea.Model, tea.Cmd) {
			return m.runMenu(idx)
		})
	}
	return overlay(base, m.box("", lines, menuWidth), m.menuX, m.menuY)
}
