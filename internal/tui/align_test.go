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

// TestPanesStayInStep is the bug from the first screenshot, made into a test.
//
// The panes used to be padded by two different pieces of code, one for a row
// with a group in it and one for a blank row, and they came out different
// widths: every host below the last group sat two columns to the right of the
// ones above it. The panes are divided by a hairline now, so the test is that
// the hairlines fall in the same columns on every row of the body.
func TestPanesStayInStep(t *testing.T) {
	m := newModel(t)
	// Wide enough that all three panes are drawn, which is the case where two
	// panes can disagree with each other.
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 24})

	lines := strings.Split(ansi.ReplaceAllString(m.View(), ""), "\n")
	if len(lines) != 24 {
		t.Fatalf("the screen is %d lines tall, the terminal is 24", len(lines))
	}

	// The body is everything between the two full-width rules.
	var body []string
	for _, l := range lines[2 : len(lines)-2] {
		body = append(body, l)
	}
	if len(body) < 5 {
		t.Fatalf("only %d lines of body, the panes are not being drawn", len(body))
	}

	want := ruleColumns(body[0])
	if len(want) != 2 {
		t.Fatalf("the first body line has %d hairlines, want 2 (groups, table, details):\n%s", len(want), body[0])
	}
	for i, l := range body {
		if got := ruleColumns(l); !sameInts(got, want) {
			t.Fatalf("body line %d puts its hairlines at %v, the first puts them at %v:\n%s",
				i, got, want, strings.Join(body, "\n"))
		}
	}
}

// TestEveryScreenFitsTheTerminal guards the one mistake that makes a terminal
// scroll and leave a torn copy of the top bar behind: drawing one line too many,
// or one column too wide.
func TestEveryScreenFitsTheTerminal(t *testing.T) {
	screens := map[string][]string{
		"list":    nil,
		"help":    {"?"},
		"form":    {"a"},
		"picker":  {"A"},
		"results": {"D"},
		"keys":    {"3"},
		"search":  {"/", "w"},
	}

	for _, size := range [][2]int{{150, 26}, {100, 30}, {80, 20}, {60, 12}} {
		for name, keys := range screens {
			m := newModel(t)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			send(m, keys...)

			lines := strings.Split(ansi.ReplaceAllString(m.View(), ""), "\n")
			if len(lines) > size[1] {
				t.Errorf("the %s screen is %d lines at %dx%d, which will scroll",
					name, len(lines), size[0], size[1])
			}
			for i, l := range lines {
				if w := runewidth.StringWidth(l); w > size[0] {
					t.Errorf("the %s screen's line %d is %d columns at width %d:\n%s",
						name, i, w, size[0], l)
					break
				}
			}
		}
	}
}

// ruleColumns is where the hairlines between panes fall, counted in columns an
// eye sees rather than bytes: a box glyph is three bytes wide and one column
// wide, and counting the wrong one is how a misalignment hides from its own
// test.
func ruleColumns(line string) []int {
	var out []int
	col := 0
	for _, r := range line {
		if r == '│' {
			out = append(out, col)
		}
		col += runewidth.RuneWidth(r)
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
