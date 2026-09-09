// Package render turns results into the shape the caller asked for. Every
// command that reads something supports the same set of formats, and the
// machine-readable ones are not an afterthought bolted onto the table: the
// table and the JSON are rendered from the same value.
package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"
	"gopkg.in/yaml.v3"
)

// Format is an output shape.
type Format string

const (
	// Table is the default human-readable output.
	Table Format = "table"
	// JSON has stable ASCII keys, and an absent value is an empty string rather
	// than a missing field, so a consumer never has to test for both.
	JSON Format = "json"
	// YAML is the same structure as JSON.
	YAML Format = "yaml"
	// CSV writes the table's columns with a header row.
	CSV Format = "csv"
	// Value writes the table's cells separated by tabs and no header, for
	// piping into cut, awk or a shell loop.
	Value Format = "value"
)

// Formats lists every accepted value, for flag help and completion.
func Formats() []string { return []string{"table", "json", "yaml", "csv", "value"} }

// Parse validates a format name.
func Parse(s string) (Format, error) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case "", Table:
		return Table, nil
	case JSON:
		return JSON, nil
	case YAML:
		return YAML, nil
	case CSV:
		return CSV, nil
	case Value:
		return Value, nil
	}
	return "", fmt.Errorf("unknown format %q; use one of %s", s, strings.Join(Formats(), ", "))
}

// Machine reports whether a format is meant for a program rather than a person,
// which is how commands know to suppress progress output and colour.
func (f Format) Machine() bool { return f != Table }

// Grid is a table of strings with a header row.
type Grid struct {
	Columns []string
	Rows    [][]string
	// RightAlign marks columns whose values are numbers.
	RightAlign map[int]bool
	// Empty is printed when the whole grid has no rows.
	Empty string
}

// Add appends a row, padding or trimming it to the column count so that a
// caller cannot produce a ragged table.
func (g *Grid) Add(cells ...string) {
	row := make([]string, len(g.Columns))
	copy(row, cells)
	g.Rows = append(g.Rows, row)
}

// Out renders a result. data is used for JSON and YAML, grid for everything
// else; a command supplies both and does not care which one is wanted.
func Out(w io.Writer, f Format, data any, grid *Grid) error {
	switch f {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	case YAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(data); err != nil {
			return err
		}
		return enc.Close()
	case CSV:
		if grid == nil {
			return fmt.Errorf("this command has no tabular form; use -f json")
		}
		cw := csv.NewWriter(w)
		if err := cw.Write(grid.Columns); err != nil {
			return err
		}
		for _, r := range grid.Rows {
			if err := cw.Write(r); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()
	case Value:
		if grid == nil {
			return fmt.Errorf("this command has no tabular form; use -f json")
		}
		for _, r := range grid.Rows {
			if _, err := fmt.Fprintln(w, strings.Join(r, "\t")); err != nil {
				return err
			}
		}
		return nil
	default:
		return WriteGrid(w, grid)
	}
}

// WriteGrid prints an aligned table. Column widths are measured in display
// cells rather than bytes, so a host name in Vietnamese, Chinese or with an
// emoji does not push the rest of the row out of line.
func WriteGrid(w io.Writer, g *Grid) error {
	if g == nil {
		return nil
	}
	if len(g.Rows) == 0 {
		if g.Empty != "" {
			_, err := fmt.Fprintln(w, g.Empty)
			return err
		}
		return nil
	}

	widths := make([]int, len(g.Columns))
	for i, c := range g.Columns {
		widths[i] = runewidth.StringWidth(c)
	}
	for _, r := range g.Rows {
		for i, c := range r {
			if n := runewidth.StringWidth(c); n > widths[i] {
				widths[i] = n
			}
		}
	}

	var b strings.Builder
	writeRow := func(cells []string) {
		b.Reset()
		for i, c := range cells {
			right := g.RightAlign != nil && g.RightAlign[i]
			pad := widths[i] - runewidth.StringWidth(c)
			if right {
				b.WriteString(strings.Repeat(" ", pad))
				b.WriteString(c)
			} else {
				b.WriteString(c)
				if i < len(cells)-1 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
			if i < len(cells)-1 {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}

	upper := make([]string, len(g.Columns))
	for i, c := range g.Columns {
		upper[i] = strings.ToUpper(c)
	}
	writeRow(upper)
	for _, r := range g.Rows {
		writeRow(r)
	}
	return nil
}

// Tree prints a hierarchy of groups with their hosts.
type Tree struct {
	Label    string
	Children []*Tree
	Leaves   []string
}

// Child returns the named child, creating it when absent.
func (t *Tree) Child(label string) *Tree {
	for _, c := range t.Children {
		if c.Label == label {
			return c
		}
	}
	c := &Tree{Label: label}
	t.Children = append(t.Children, c)
	return c
}

// Sort orders children and leaves alphabetically so output is stable.
func (t *Tree) Sort() {
	sort.Slice(t.Children, func(i, j int) bool { return t.Children[i].Label < t.Children[j].Label })
	sort.Strings(t.Leaves)
	for _, c := range t.Children {
		c.Sort()
	}
}

// WriteTree prints the tree with box-drawing characters, or plain ASCII when
// the console cannot show them.
func WriteTree(w io.Writer, t *Tree, ascii bool) {
	branch, last, vert, gap := "├─ ", "└─ ", "│  ", "   "
	if ascii {
		branch, last, vert, gap = "|- ", "`- ", "|  ", "   "
	}
	var walk func(n *Tree, prefix string)
	walk = func(n *Tree, prefix string) {
		items := make([]func(string), 0, len(n.Children)+len(n.Leaves))
		for _, c := range n.Children {
			c := c
			items = append(items, func(p string) { walk(c, p) })
		}
		for _, l := range n.Leaves {
			l := l
			items = append(items, func(p string) { fmt.Fprintln(w, p+l) })
		}
		for i := range items {
			isLast := i == len(items)-1
			connector, childPrefix := branch, prefix+vert
			if isLast {
				connector, childPrefix = last, prefix+gap
			}
			if i < len(n.Children) {
				fmt.Fprintln(w, prefix+connector+n.Children[i].Label)
				walk(n.Children[i], childPrefix)
				continue
			}
			fmt.Fprintln(w, prefix+connector+n.Leaves[i-len(n.Children)])
		}
	}
	if t.Label != "" {
		fmt.Fprintln(w, t.Label)
	}
	walk(t, "")
}
