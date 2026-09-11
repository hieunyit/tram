package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Overlays: the command palette and the context menu are drawn on top of the
// screen rather than instead of it, because both are about the row you were
// looking at and hiding it would be hiding the question.
//
// Compositing a box into already-coloured lines means cutting text that carries
// escape sequences. ansiSlice does that without losing the colour of the part it
// keeps: the sequence in force at the cut is re-opened on the far side, which is
// the one thing a naive cut gets wrong and the reason the right half of a row
// would otherwise come out grey.

// overlay draws box over screen with its top-left corner at (x, y).
func overlay(screen string, box []string, x, y int) string {
	lines := strings.Split(screen, "\n")
	boxW := 0
	for _, b := range box {
		if w := lipgloss.Width(b); w > boxW {
			boxW = w
		}
	}

	for i, b := range box {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		under := lines[row]
		left := ansiSlice(under, 0, x)
		if w := lipgloss.Width(left); w < x {
			left += strings.Repeat(" ", x-w)
		}
		right := ansiSlice(under, x+boxW, lipgloss.Width(under))
		lines[row] = left + ansiPad(b, boxW) + right
	}
	return strings.Join(lines, "\n")
}

// ansiSlice returns the visible columns [from, to) of a string that may carry
// colour, with the style in force at the cut carried over.
func ansiSlice(s string, from, to int) string {
	if to <= from {
		return ""
	}
	var b strings.Builder
	var open string // the last style opened and not yet reset
	col := 0
	started := false

	for i := 0; i < len(s); {
		if s[i] == 27 {
			j := i
			for j < len(s) && !isANSIFinal(s[j]) {
				j++
			}
			if j < len(s) {
				j++
			}
			seq := s[i:j]
			if seq == "\x1b[0m" || seq == "\x1b[m" {
				open = ""
			} else {
				open = seq
			}
			if started {
				b.WriteString(seq)
			}
			i = j
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		w := runewidth.RuneWidth(r)
		if col >= to {
			break
		}
		if col >= from {
			if !started {
				started = true
				// Re-open whatever was in force where the cut fell.
				if open != "" {
					b.WriteString(open)
				}
			}
			b.WriteString(s[i : i+size])
		}
		col += w
		i += size
	}
	if started && open != "" {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// box draws a bordered panel for an overlay to live in.
func (m *Model) box(title string, lines []string, w int) []string {
	fr := m.st.rule
	top := fr.Render("┌" + strings.Repeat("─", w-2) + "┐")
	if m.gl.hline == "-" {
		top = fr.Render("+" + strings.Repeat("-", w-2) + "+")
	}
	if title != "" {
		label := " " + title + " "
		if runewidth.StringWidth(label) < w-4 {
			rest := w - 3 - runewidth.StringWidth(label)
			top = fr.Render(corner(m, true)+m.gl.hline) + m.st.section.Render(label) +
				fr.Render(strings.Repeat(m.gl.hline, rest)+corner(m, false))
		}
	}

	out := []string{top}
	v := fr.Render(m.gl.vline)
	for _, l := range lines {
		out = append(out, v+" "+ansiPad(ansiTruncate(l, w-4), w-4)+" "+v)
	}
	bottom := "└" + strings.Repeat("─", w-2) + "┘"
	if m.gl.hline == "-" {
		bottom = "+" + strings.Repeat("-", w-2) + "+"
	}
	out = append(out, fr.Render(bottom))
	return out
}

func corner(m *Model, left bool) string {
	if m.gl.hline == "-" {
		return "+"
	}
	if left {
		return "┌"
	}
	return "┐"
}
