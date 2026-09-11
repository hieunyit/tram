package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// The mouse tests drive the interface the way a pointer does: draw a frame,
// find something on it, and press where it is. Nothing is hit-tested that was
// not just drawn, so looking the target up in the rendered screen is not a
// convenience here, it is the same lookup the program does.

// find returns where a piece of text is on screen, in the columns and rows the
// terminal counts in.
func findOnScreen(t *testing.T, m *Model, needle string) (x, y int) {
	t.Helper()
	screen := ansi.ReplaceAllString(m.View(), "")
	for row, line := range strings.Split(screen, "\n") {
		i := strings.Index(line, needle)
		if i < 0 {
			continue
		}
		return runewidth.StringWidth(line[:i]), row
	}
	t.Fatalf("%q is not on screen:\n%s", needle, screen)
	return 0, 0
}

func press(m *Model, x, y int, b tea.MouseButton) {
	m.View() // the frame the click lands on
	_, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress})
	drain(m, cmd)
}

func click(m *Model, x, y int) { press(m, x, y, tea.MouseButtonLeft) }

func wide(t *testing.T) *Model {
	t.Helper()
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 26})
	return m
}

// TestClickSelectsARow is the first thing a pointer is for.
func TestClickSelectsARow(t *testing.T) {
	m := wide(t)
	if h, _ := m.current(); h.Name != "bastion" {
		t.Fatalf("the cursor starts on %q", h.Name)
	}

	x, y := findOnScreen(t, m, "web1")
	click(m, x, y)
	if h, _ := m.current(); h.Name != "web1" {
		t.Errorf("clicking the web1 row selected %q", h.Name)
	}

	// The box two columns left of the name marks the row instead of selecting it.
	x, y = findOnScreen(t, m, "laptop")
	click(m, x-2, y)
	if !m.marked["laptop"] {
		t.Error("clicking the box did not mark the row")
	}
	click(m, x-2, y)
	if m.marked["laptop"] {
		t.Error("clicking the box again did not clear the mark")
	}
}

// TestClickingATabSwitchesView covers the tab strip along the top.
func TestClickingATabSwitchesView(t *testing.T) {
	m := wide(t)
	x, y := findOnScreen(t, m, "KEYS")
	click(m, x, y)
	if m.tab != tabKeys {
		t.Fatalf("clicking KEYS left the interface on %v", m.tab)
	}
	x, y = findOnScreen(t, m, "HOSTS")
	click(m, x, y)
	if m.tab != tabHosts {
		t.Error("clicking HOSTS did not come back")
	}
}

// TestClickingAHeadingSorts covers the sortable columns, which the design marks
// with an arrow and expects to be clickable.
func TestClickingAHeadingSorts(t *testing.T) {
	m := wide(t)
	x, y := findOnScreen(t, m, "USER@HOST")
	click(m, x, y)
	if m.sortKey != sortHost {
		t.Fatalf("clicking USER@HOST sorted by %v", m.sortKey)
	}
	if m.sortDir != 1 {
		t.Errorf("the first click should sort upwards, not %d", m.sortDir)
	}
	click(m, x, y)
	if m.sortDir != -1 {
		t.Error("clicking the same heading again did not reverse it")
	}
}

// TestRightClickOpensTheMenuOnThatRow checks that the menu is about the row
// under the pointer rather than the row that happened to be selected.
func TestRightClickOpensTheMenuOnThatRow(t *testing.T) {
	m := wide(t)
	x, y := findOnScreen(t, m, "web1")
	press(m, x, y, tea.MouseButtonRight)

	if m.mode != modeMenu {
		t.Fatal("the right button opened no menu")
	}
	if h, _ := m.current(); h.Name != "web1" {
		t.Errorf("the menu is about %q rather than the row that was clicked", h.Name)
	}
	out := m.View()
	for _, want := range []string{"Connect", "Copy ssh command", "Delete"} {
		if !strings.Contains(out, want) {
			t.Errorf("the menu has no %q:\n%s", want, out)
		}
	}

	// Escape puts it away and leaves the row alone.
	send(m, "esc")
	if m.mode != modeNormal {
		t.Error("escape did not close the menu")
	}
	if h, _ := m.current(); h.Name != "web1" {
		t.Error("closing the menu moved the cursor")
	}
}

// TestMenuItemsRunTheKeyTheyName is the contract that keeps a menu honest: every
// entry replays a keystroke, so it cannot do something the key does not.
func TestMenuItemsRunTheKeyTheyName(t *testing.T) {
	m := wide(t)
	x, y := findOnScreen(t, m, "laptop")
	press(m, x, y, tea.MouseButtonRight)

	mx, my := findOnScreen(t, m, "Mark")
	click(m, mx, my)
	if m.mode != modeNormal {
		t.Fatal("choosing an entry left the menu open")
	}
	if !m.marked["laptop"] {
		t.Error("the Mark entry did not mark the host")
	}
}

// TestPaletteRunsACommand covers the palette end to end: open it, type, choose.
func TestPaletteRunsACommand(t *testing.T) {
	m := wide(t)
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.mode != modePalette {
		t.Fatal("ctrl+k opened no palette")
	}
	if !strings.Contains(m.View(), "COMMANDS") {
		t.Error("the palette is not drawn")
	}

	send(m, "a", "d", "d")
	items := m.paletteVisible()
	if len(items) == 0 {
		t.Fatal("typing add matched nothing")
	}
	for _, c := range items {
		if strings.Contains(strings.ToLower(c.label), "add a new host") {
			goto found
		}
	}
	t.Fatalf("the add command is not among %d matches", len(items))

found:
	// Walk to it rather than assuming it is first, then run it.
	for i, c := range items {
		if strings.Contains(strings.ToLower(c.label), "add a new host") {
			m.paletteCursor = i
		}
	}
	send(m, "enter")
	if m.mode != modeForm {
		t.Fatalf("the palette did not open the add form; mode is %v", m.mode)
	}
}

// TestWheelScrollsWithoutLosingTheSelection checks that the wheel moves the
// window and the cursor stays on screen with it.
func TestWheelScrollsWithoutLosingTheSelection(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 12})
	for i := 0; i < 30; i++ {
		m.filtered = append(m.filtered, m.hosts[0])
	}

	m.View()
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.offset == 0 {
		t.Fatal("the wheel did not scroll")
	}
	if m.cursor < m.offset || m.cursor >= m.offset+m.listHeight() {
		t.Errorf("the cursor at %d is outside the window %d..%d", m.cursor, m.offset, m.offset+m.listHeight())
	}
}

// TestOverlaysKeepTheScreenWidth guards the compositing: a box spliced into
// coloured lines must not make them wider, or every pane to its right moves.
func TestOverlaysKeepTheScreenWidth(t *testing.T) {
	m := wide(t)
	plain := strings.Split(ansi.ReplaceAllString(m.View(), ""), "\n")

	for _, open := range []func(){
		func() { m.Update(tea.KeyMsg{Type: tea.KeyCtrlK}) },
		func() { press(m, 40, 8, tea.MouseButtonRight) },
	} {
		m.closeOverlay()
		open()
		lines := strings.Split(ansi.ReplaceAllString(m.View(), ""), "\n")
		if len(lines) != len(plain) {
			t.Fatalf("an overlay changed the screen from %d lines to %d", len(plain), len(lines))
		}
		for i, l := range lines {
			if got, want := runewidth.StringWidth(l), runewidth.StringWidth(plain[i]); got != want {
				t.Fatalf("an overlay made line %d %d columns wide, it was %d:\n%s", i, got, want, l)
			}
		}
	}
}

// TestOverlayKeepsTheColourOfWhatItCutsThrough is the subtle half of
// compositing: the text to the right of the box has to keep the style it was
// drawn with, not the style the cut left behind.
func TestOverlayKeepsTheColourOfWhatItCutsThrough(t *testing.T) {
	const red = "\x1b[31m"
	line := red + "left-of-box" + strings.Repeat(" ", 10) + "right-of-box\x1b[0m"

	got := overlay(line, []string{"BOX"}, 12, 0)
	if !strings.Contains(got, "BOX") {
		t.Fatalf("the box was not composited: %q", got)
	}
	after := got[strings.Index(got, "BOX")+3:]
	if !strings.Contains(after, red) {
		t.Errorf("the text right of the box lost its colour: %q", after)
	}
	if w, want := runewidth.StringWidth(ansi.ReplaceAllString(got, "")), runewidth.StringWidth(ansi.ReplaceAllString(line, "")); w != want {
		t.Errorf("compositing changed the width from %d to %d", want, w)
	}
}
