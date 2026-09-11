package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The mouse.
//
// tram used to refuse the mouse on purpose: capturing it takes selection and
// copying away from the terminal, and that is a bad trade for a list you can
// drive with one hand. The design this interface follows is a pointer design,
// though, so the mouse is on by default and `mouse = false` in config.toml puts
// it back. Most terminals still let you select text while it is captured by
// holding shift.
//
// Hit testing works the way an immediate-mode interface does it: the drawing
// code registers a rectangle and what to do with it, and a click looks up the
// point. Nothing is hit-tested that was not just drawn, so a click can never
// land on a row that scrolled away.

// hit is one clickable strip of one line.
type hit struct {
	x, y, w int
	run     func() (tea.Model, tea.Cmd)
}

// clear drops the hit map, which every frame rebuilds from nothing.
func (m *Model) clearHits() { m.hits = m.hits[:0] }

// hitAt registers a strip. Zero or negative widths are dropped rather than
// stored, so a column the layout squeezed out cannot be clicked.
func (m *Model) hitAt(x, y, w int, run func() (tea.Model, tea.Cmd)) {
	if w <= 0 || run == nil {
		return
	}
	m.hits = append(m.hits, hit{x: x, y: y, w: w, run: run})
}

// find returns the action registered at a point, latest first: an overlay
// registers after the screen under it, and the thing drawn last is the thing
// the eye sees.
func (m *Model) find(x, y int) func() (tea.Model, tea.Cmd) {
	for i := len(m.hits) - 1; i >= 0; i-- {
		h := m.hits[i]
		if y == h.y && x >= h.x && x < h.x+h.w {
			return h.run
		}
	}
	return nil
}

// doubleClickWindow is how close together two presses have to be to count as
// one double click. It is the interval most desktops use.
const doubleClickWindow = 400 * time.Millisecond

func (m *Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scroll(-3)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.scroll(3)
		return m, nil
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}

	// A click anywhere puts an overlay away, unless it lands inside one, and
	// the overlay's own strips are registered after the backdrop's.
	switch msg.Button {
	case tea.MouseButtonLeft:
		now := time.Now()
		double := m.lastClick.x == msg.X && m.lastClick.y == msg.Y &&
			now.Sub(m.lastClick.at) < doubleClickWindow
		m.lastClick = clickAt{x: msg.X, y: msg.Y, at: now}

		// Whether this is the second press is told to the strip rather than
		// acted on here: a double click means open on a row and means nothing
		// on a tab, and only the row knows which it is.
		m.doubleClick = double
		if run := m.find(msg.X, msg.Y); run != nil {
			return run()
		}
		if m.mode == modePalette || m.mode == modeMenu {
			m.closeOverlay()
		}
		return m, nil

	case tea.MouseButtonRight:
		// Right-clicking a row selects it first, so the menu is always about
		// the thing under the pointer.
		if run := m.find(msg.X, msg.Y); run != nil {
			run()
		}
		if m.screen == screenList && m.mode == modeNormal {
			return m.openMenu(msg.X, msg.Y)
		}
	}
	return m, nil
}

// clickAt remembers where and when the last press landed, for double clicks.
type clickAt struct {
	x, y int
	at   time.Time
}

// scroll moves the table without moving the selection, the way a wheel does.
func (m *Model) scroll(d int) {
	switch {
	case m.mode == modePalette:
		m.paletteCursor = clamp(m.paletteCursor+d, 0, max(0, len(m.paletteVisible())-1))
		return
	case m.mode != modeNormal, m.screen != screenList:
		return
	}
	n := m.rowCount()
	h := m.listHeight()
	if n <= h {
		return
	}
	m.offset = clamp(m.offset+d, 0, n-h)
	// The cursor follows the window rather than the other way round, so the
	// selection never leaves the screen.
	m.cursor = clamp(m.cursor, m.offset, m.offset+h-1)
}
