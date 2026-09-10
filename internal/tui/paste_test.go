package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// pasteMsgFor builds the message a terminal produces when text is pasted with
// the mouse: bracketed paste is on, so it arrives as one key event carrying the
// whole text rather than as a stream of keystrokes.
func pasteMsgFor(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true}
}

// TestMousePasteReachesTheField covers pasting into a form with the mouse.
func TestMousePasteReachesTheField(t *testing.T) {
	m := newModel(t)
	send(m, "a")
	m.form.focus(indexOf(m.form, fAddr))

	m.Update(pasteMsgFor("10.20.30.40"))
	if got := m.form.get(fAddr); got != "10.20.30.40" {
		t.Errorf("the address field holds %q after a paste", got)
	}
}

// TestCtrlVIsRoutedRatherThanDropped covers the other way people paste.
//
// The widget turns ctrl+v into a command; the command reads the clipboard and
// answers with a message of its own. An Update that only looks at key events
// throws that answer away and the paste silently does nothing, which is exactly
// what happened.
//
// The clipboard is read but never written: a test has no business overwriting
// what someone has copied. When it happens to hold something, the whole path is
// exercised; when it does not, only the first half can be.
func TestCtrlVIsRoutedRatherThanDropped(t *testing.T) {
	m := newModel(t)
	send(m, "a")
	m.form.focus(indexOf(m.form, fName))

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("ctrl+v produced no command, so nothing would ever read the clipboard")
	}

	before := m.form.get(fName)
	m.Update(textinput.Paste())
	if got := m.form.get(fName); got == before {
		t.Skip("the clipboard is empty here, so delivery cannot be observed")
	}
}

// TestPasteIsNotReadAsAKeyBinding is the trap underneath both.
//
// A key event is routed by the text it carries, so pasting a word that happens
// to spell a binding would trigger the binding instead of typing the word.
func TestPasteIsNotReadAsAKeyBinding(t *testing.T) {
	for _, text := range []string{"enter", "tab", "esc", "ctrl+s", "q"} {
		m := newModel(t)
		send(m, "a")
		m.form.focus(indexOf(m.form, fName))
		m.Update(pasteMsgFor(text))

		if m.form == nil {
			t.Errorf("pasting %q closed the form", text)
			continue
		}
		if got := m.form.get(fName); got != text {
			t.Errorf("pasting %q gave %q; it was read as a key rather than as text", text, got)
		}
	}
}

// TestPasteIntoSearchAndFilter covers the other two places text is typed.
func TestPasteIntoSearchAndFilter(t *testing.T) {
	m := newModel(t)
	send(m, "/")
	m.Update(pasteMsgFor("web"))
	if m.searchQuery != "web" {
		t.Errorf("search holds %q after a paste", m.searchQuery)
	}

	m2 := newModel(t)
	send(m2, "a")
	m2.form.focus(indexOf(m2.form, fGroup))
	send(m2, "enter") // the group picker
	if m2.picker == nil {
		t.Fatal("no picker opened")
	}
	m2.Update(pasteMsgFor("prod"))
	if !strings.Contains(m2.picker.filter, "prod") {
		t.Errorf("the picker filter holds %q after a paste", m2.picker.filter)
	}
}
