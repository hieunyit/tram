package sshconf

import (
	"os"
	"strings"
)

// BlockType distinguishes the three kinds of region in an ssh_config.
type BlockType int

const (
	// BlockGlobal holds the lines before the first Host or Match, which apply
	// to every connection.
	BlockGlobal BlockType = iota
	// BlockHost is a Host stanza.
	BlockHost
	// BlockMatch is a Match stanza. tram reads these but never rewrites them.
	BlockMatch
)

// Block is a contiguous region of lines owned by one stanza.
//
// Head is the index of the Host or Match line itself. Lead is the index of the
// first line of the comment run that introduces the stanza, which equals Head
// when there is none. End is one past the last line the stanza owns, so
// [Lead, End) is everything that disappears when the stanza is deleted.
type Block struct {
	File *File
	Type BlockType

	Lead int
	Head int
	End  int

	Patterns []string // Host patterns, or Match tokens
}

// Names returns the Host patterns that name exactly one host, that is those
// without wildcards. A stanza like `Host web1 web1.internal` names two.
func (b *Block) Names() []string {
	if b.Type != BlockHost {
		return nil
	}
	var out []string
	for _, p := range b.Patterns {
		if !isPattern(p) {
			out = append(out, p)
		}
	}
	return out
}

// Matches reports whether the stanza applies to host, following ssh's pattern
// rules including negation.
func (b *Block) Matches(host string) bool {
	if b.Type == BlockGlobal {
		return true
	}
	if b.Type != BlockHost {
		return false
	}
	matched := false
	for _, p := range b.Patterns {
		neg := strings.HasPrefix(p, "!")
		pat := strings.TrimPrefix(p, "!")
		if matchPattern(pat, host) {
			if neg {
				return false
			}
			matched = true
		}
	}
	return matched
}

// Lines returns the stanza's own lines, excluding the introductory comment run.
func (b *Block) Lines() []Line { return b.File.Lines[b.Head:b.End] }

// Body returns the stanza's lines below the Host line.
func (b *Block) Body() []Line { return b.File.Lines[b.Head+1 : b.End] }

// LeadLines returns the comment run that introduces the stanza, above its Host
// line. It is empty when there is none.
func (b *Block) LeadLines() []Line { return b.File.Lines[b.Lead:b.Head] }

// Get returns the arguments of the first occurrence of keyword kw. ssh honours
// the first value it sees for most keywords, so this is the effective one.
func (b *Block) Get(kw string) ([]string, bool) {
	for _, l := range b.Body() {
		if l.Is(kw) {
			return l.Args, true
		}
	}
	return nil, false
}

// GetOne returns the first argument of the first occurrence of kw.
func (b *Block) GetOne(kw string) string {
	if a, ok := b.Get(kw); ok && len(a) > 0 {
		return a[0]
	}
	return ""
}

// GetAll returns the first argument of every occurrence of kw, which is what
// repeated keywords such as IdentityFile need.
func (b *Block) GetAll(kw string) []string {
	var out []string
	for _, l := range b.Body() {
		if l.Is(kw) && len(l.Args) > 0 {
			out = append(out, l.Args...)
		}
	}
	return out
}

// File is one parsed configuration file, held as its exact lines plus a
// structural view over them.
type File struct {
	Path   string
	Lines  []Line
	Blocks []*Block

	// Indent is the indentation this file uses for directives inside a stanza,
	// learned from the file itself so that new lines look like the old ones.
	Indent string
	// EOL is the line terminator new lines should use.
	EOL string
	// FinalNewline records whether the file ended with a terminator.
	FinalNewline bool

	// Dirty is set by the edit operations and cleared by Save.
	Dirty bool
	// Managed marks a file tram created and may write to without --force.
	Managed bool

	mode os.FileMode
}

// Bytes renders the file exactly as it should be written to disk.
func (f *File) Bytes() []byte {
	var b strings.Builder
	for _, l := range f.Lines {
		b.WriteString(l.Raw)
		b.WriteString(l.EOL)
	}
	return []byte(b.String())
}

// String renders the file as text.
func (f *File) String() string { return string(f.Bytes()) }

// ParseFile reads and parses the file at path.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := Parse(data, path)
	if st, err := os.Stat(path); err == nil {
		f.mode = st.Mode().Perm()
	}
	return f, nil
}

// Parse builds a File from raw bytes. path is recorded but not read.
func Parse(data []byte, path string) *File {
	f := &File{Path: path, EOL: "\n", Indent: "    ", FinalNewline: true, mode: 0o600}
	f.Lines = splitLines(data, f)
	f.reindex()
	f.learnStyle()
	return f
}

// splitLines cuts data into Lines, keeping each terminator as it was found so
// that files with mixed endings survive a round trip.
func splitLines(data []byte, f *File) []Line {
	s := string(data)
	if s == "" {
		f.FinalNewline = false
		return nil
	}
	var lines []Line
	start := 0
	crlf, lf := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		raw, eol := s[start:i], "\n"
		if strings.HasSuffix(raw, "\r") {
			raw, eol = raw[:len(raw)-1], "\r\n"
			crlf++
		} else {
			lf++
		}
		l := parseLine(raw)
		l.EOL = eol
		lines = append(lines, l)
		start = i + 1
	}
	if start < len(s) {
		l := parseLine(s[start:])
		l.EOL = ""
		lines = append(lines, l)
		f.FinalNewline = false
	}
	if crlf > lf {
		f.EOL = "\r\n"
	}
	return lines
}

// learnStyle infers the indentation of the file from its existing stanzas so
// that lines tram adds match the ones already there.
func (f *File) learnStyle() {
	counts := map[string]int{}
	for _, b := range f.Blocks {
		if b.Type == BlockGlobal {
			continue
		}
		for _, l := range b.Body() {
			if l.Kind == LineDirective && l.Indent != "" {
				counts[l.Indent]++
			}
		}
	}
	best, n := "", 0
	for ind, c := range counts {
		if c > n {
			best, n = ind, c
		}
	}
	if best != "" {
		f.Indent = best
	}
}

// reindex rebuilds the block view from the current lines. Every mutation calls
// it, so block boundaries are never stale.
func (f *File) reindex() {
	f.Blocks = nil
	var cur *Block

	closeAt := func(end int) {
		if cur != nil {
			cur.End = end
			f.Blocks = append(f.Blocks, cur)
			cur = nil
		}
	}

	// Start with the implicit global block covering the file preamble.
	cur = &Block{File: f, Type: BlockGlobal, Lead: 0, Head: 0}

	for i, l := range f.Lines {
		if l.Kind != LineDirective {
			continue
		}
		isHost := l.Is("Host")
		isMatch := l.Is("Match")
		if !isHost && !isMatch {
			continue
		}
		lead := leadStart(f.Lines, i)
		closeAt(lead)
		t := BlockHost
		if isMatch {
			t = BlockMatch
		}
		cur = &Block{File: f, Type: t, Lead: lead, Head: i, Patterns: append([]string(nil), l.Args...)}
	}
	closeAt(len(f.Lines))

	// A global block with no lines at all is not interesting, but keep it when
	// the file begins with content so that writers have somewhere to look.
	if len(f.Blocks) > 0 && f.Blocks[0].Type == BlockGlobal && f.Blocks[0].End == 0 {
		f.Blocks = f.Blocks[1:]
	}
}

// leadStart walks back from a Host line over an unbroken run of comments and
// returns where that run begins, but only when the run is separated from the
// stanza above by a blank line or the start of the file. A comment that sits
// directly under a directive belongs to the stanza above it, not below.
func leadStart(lines []Line, head int) int {
	i := head - 1
	for i >= 0 && lines[i].Kind == LineComment {
		i--
	}
	run := i + 1
	if run == head {
		return head // no comment run
	}
	if run == 0 || lines[run-1].Kind == LineBlank {
		return run
	}
	return head
}
