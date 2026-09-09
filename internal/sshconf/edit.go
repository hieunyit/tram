package sshconf

import (
	"fmt"
	"strings"
)

// Directive is a keyword and its arguments, used to describe an edit.
type Directive struct {
	Keyword string
	Args    []string
}

// D is shorthand for a single-argument directive.
func D(kw string, args ...string) Directive { return Directive{Keyword: kw, Args: args} }

// mkLine builds a directive line in the style of the file it will live in.
func mkLine(f *File, indent, kw string, args []string) Line {
	raw := indent + kw
	if len(args) > 0 {
		raw += " " + joinArgs(args)
	}
	l := parseLine(raw)
	l.EOL = f.EOL
	return l
}

// rewrite produces a replacement for an existing directive line.
//
// When the new arguments are the ones already there the original line is
// returned untouched, so writing a value back over itself does not reformat
// separators or strip trailing whitespace. Otherwise the line keeps its
// indentation, its keyword casing and its separator style, and only the
// arguments change.
func rewrite(orig Line, args []string) Line {
	if sameArgs(orig.Args, args) {
		return orig
	}
	sep := orig.Sep
	if sep == "" {
		sep = " "
	}
	l := parseLine(orig.Indent + orig.Keyword + sep + joinArgs(args))
	l.EOL = orig.EOL
	return l
}

func sameArgs(a, b []string) bool {
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

// splice replaces the lines in [start, end) with repl and rebuilds the block
// index. Every mutation goes through here so the structural view is never
// out of step with the bytes.
func (f *File) splice(start, end int, repl []Line) {
	out := make([]Line, 0, len(f.Lines)-(end-start)+len(repl))
	out = append(out, f.Lines[:start]...)
	out = append(out, repl...)
	out = append(out, f.Lines[end:]...)
	f.Lines = out
	f.ensureFinalEOL()
	f.reindex()
	f.Dirty = true
}

// ensureFinalEOL keeps the file's last-line terminator consistent with how the
// file was found: a file that ended without a newline keeps ending without one.
func (f *File) ensureFinalEOL() {
	if len(f.Lines) == 0 {
		return
	}
	last := len(f.Lines) - 1
	for i := range f.Lines[:last] {
		if f.Lines[i].EOL == "" {
			f.Lines[i].EOL = f.EOL
		}
	}
	if f.FinalNewline && f.Lines[last].EOL == "" {
		f.Lines[last].EOL = f.EOL
	}
	if !f.FinalNewline {
		f.Lines[last].EOL = ""
	}
}

// bodyIndent returns the indentation used inside a stanza, taken from the
// stanza itself when it has directives and from the file's style otherwise.
func bodyIndent(b *Block) string {
	for _, l := range b.Body() {
		if l.Kind == LineDirective {
			return l.Indent
		}
	}
	return b.File.Indent
}

// contentEnd is the index one past the stanza's last non-blank line, so that
// appended directives land above any trailing blank separator.
func contentEnd(b *Block) int {
	e := b.End
	for e > b.Head+1 && b.File.Lines[e-1].Kind == LineBlank {
		e--
	}
	return e
}

// SetDirectives applies a batch of changes to one stanza.
//
// A keyword present in set replaces the stanza's existing occurrences of that
// keyword in place, keeping its position and indentation. A keyword given more
// than once in set produces that many lines, which is how repeated keywords
// such as IdentityFile are expressed. A keyword listed in unset is removed
// entirely. Everything else in the stanza, including comments and directives
// tram does not understand, is left untouched.
func SetDirectives(b *Block, set []Directive, unset []string) {
	f := b.File
	indent := bodyIndent(b)

	// Group the requested values by keyword, preserving the order they were
	// given so that repeated keywords keep their relative order.
	type group struct {
		kw   string
		args [][]string
	}
	var groups []*group
	byKw := map[string]*group{}
	for _, d := range set {
		k := strings.ToLower(d.Keyword)
		g, ok := byKw[k]
		if !ok {
			g = &group{kw: d.Keyword}
			byKw[k] = g
			groups = append(groups, g)
		}
		g.args = append(g.args, d.Args)
	}
	drop := map[string]bool{}
	for _, u := range unset {
		drop[strings.ToLower(u)] = true
	}

	end := contentEnd(b)
	body := f.Lines[b.Head+1 : end]
	out := make([]Line, 0, len(body)+len(groups))
	placed := map[string]bool{}

	for _, l := range body {
		if l.Kind != LineDirective {
			out = append(out, l)
			continue
		}
		k := strings.ToLower(l.Keyword)
		if drop[k] {
			continue
		}
		g, ok := byKw[k]
		if !ok {
			out = append(out, l)
			continue
		}
		if placed[k] {
			continue // a later occurrence of a keyword we already rewrote
		}
		placed[k] = true
		for i, args := range g.args {
			if i == 0 {
				out = append(out, rewrite(l, args))
				continue
			}
			out = append(out, mkLine(f, l.Indent, l.Keyword, args))
		}
	}

	for _, g := range groups {
		k := strings.ToLower(g.kw)
		if placed[k] {
			continue
		}
		for _, args := range g.args {
			out = append(out, mkLine(f, indent, g.kw, args))
		}
	}

	f.splice(b.Head+1, end, out)
}

// SetPatterns rewrites the Host line of a stanza, which is how a rename or an
// alias change is applied.
func SetPatterns(b *Block, patterns []string) {
	f := b.File
	f.splice(b.Head, b.Head+1, []Line{rewrite(f.Lines[b.Head], patterns)})
}

// AddHost appends a new stanza to the end of a file and returns it.
func AddHost(f *File, patterns []string, dirs []Directive, comment string) (*Block, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("a Host stanza needs at least one name")
	}
	var lines []Line

	// Separate the new stanza from whatever came before it.
	if n := len(f.Lines); n > 0 && f.Lines[n-1].Kind != LineBlank {
		lines = append(lines, Line{Raw: "", EOL: f.EOL, Kind: LineBlank})
	}
	if comment != "" {
		for _, c := range strings.Split(comment, "\n") {
			lines = append(lines, parseLineEOL("# "+c, f.EOL))
		}
	}
	lines = append(lines, mkLine(f, "", "Host", patterns))
	for _, d := range dirs {
		if len(d.Args) == 1 && d.Args[0] == "" {
			continue
		}
		lines = append(lines, mkLine(f, f.Indent, d.Keyword, d.Args))
	}

	at := len(f.Lines)
	f.splice(at, at, lines)

	for _, b := range f.Blocks {
		if b.Head >= at && b.Type == BlockHost {
			return b, nil
		}
	}
	return nil, fmt.Errorf("internal: new stanza not found after insert")
}

func parseLineEOL(raw, eol string) Line {
	l := parseLine(raw)
	l.EOL = eol
	return l
}

// RemoveHost deletes a stanza together with everything it owns: its body, its
// unknown directives, its comments and its introductory comment run. Nothing
// is left behind to float up the file and become global configuration.
func RemoveHost(b *Block) {
	f := b.File
	start, end := b.Lead, b.End

	// The stanza owns the blank lines that trail it. When it has none, take one
	// from above instead, so that deleting a stanza between two others leaves
	// exactly one blank line rather than none or two.
	trailingBlank := end > start && f.Lines[end-1].Kind == LineBlank
	if !trailingBlank && start > 0 && f.Lines[start-1].Kind == LineBlank && end < len(f.Lines) {
		start--
	}
	f.splice(start, end, nil)
}

// RemovePattern drops one name from a stanza's Host line. When it was the only
// name the whole stanza is removed instead.
func RemovePattern(b *Block, name string) {
	var keep []string
	for _, p := range b.Patterns {
		if !foldEqual(p, name) {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		RemoveHost(b)
		return
	}
	SetPatterns(b, keep)
}

// InsertLineAt puts a raw line at index i, used for Include lines that must go
// above everything else.
func InsertLineAt(f *File, i int, raw string) {
	f.splice(i, i, []Line{parseLineEOL(raw, f.EOL)})
}

// RawLine builds a line from verbatim text, in the file's line-ending style.
func RawLine(f *File, raw string) Line { return parseLineEOL(raw, f.EOL) }

// ReplaceBody swaps a stanza's body for the given lines, keeping the Host line
// and any trailing blank separator below the stanza.
func ReplaceBody(b *Block, lines []Line) {
	f := b.File
	for i := range lines {
		if lines[i].EOL == "" {
			lines[i].EOL = f.EOL
		}
	}
	f.splice(b.Head+1, contentEnd(b), lines)
}
