package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// ansi strips the colour codes, so a test counts columns the way an eye does.
var ansi = regexp.MustCompile(string(rune(27)) + `\[[0-9;]*[a-zA-Z]`)

// TestPanesStayInStep is the bug from the screenshot, made into a test.
//
// The group pane had fewer rows than the host list, and the blank ones were
// padded by different code from the rows with a group in them. They came out
// two columns wider, so every host below the last group sat two columns to the
// right of the ones above it.
func TestPanesStayInStep(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})

	lines := strings.Split(ansi.ReplaceAllString(m.View(), ""), "\n")
	var cols []int
	for _, l := range lines {
		if i := strings.Index(l, "│"); i >= 0 {
			// The column an eye sees, not the byte offset: a tree glyph is three
			// bytes wide and one column wide, and counting the wrong one is how
			// a misalignment hides from its own test.
			cols = append(cols, runewidth.StringWidth(l[:i]))
		}
	}
	if len(cols) < 5 {
		t.Fatalf("only %d lines carry the divider, the panes are not being drawn", len(cols))
	}
	for i, c := range cols {
		if c != cols[0] {
			t.Fatalf("line %d puts the divider at column %d, the first puts it at %d:\n%s",
				i, c, cols[0], strings.Join(lines, "\n"))
		}
	}
}
