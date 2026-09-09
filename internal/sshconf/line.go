// Package sshconf parses and rewrites OpenSSH client configuration files.
//
// The package knows nothing about tram. It knows ssh_config, and it is built on
// one rule: anything it does not understand is preserved verbatim. A file that
// is read and written back without edits is byte-for-byte identical, including
// line endings, indentation, comments and a missing trailing newline.
package sshconf

import (
	"strings"
	"unicode"
)

// LineKind classifies a physical line of a configuration file.
type LineKind int

const (
	// LineBlank is empty or whitespace only.
	LineBlank LineKind = iota
	// LineComment starts with '#' after optional whitespace.
	LineComment
	// LineDirective is a keyword with zero or more arguments.
	LineDirective
)

// Line is one physical line. Raw excludes the line terminator; EOL holds it so
// that concatenating Raw+EOL over every line reproduces the original bytes.
type Line struct {
	Raw string
	EOL string // "\n", "\r\n" or "" for a final line with no terminator

	Kind    LineKind
	Indent  string   // leading whitespace, verbatim
	Keyword string   // as written, case preserved
	Sep     string   // separator between keyword and arguments, verbatim
	Args    []string // arguments with quoting removed
	ArgsRaw string   // everything after Sep, verbatim, trailing space kept
}

// Text returns the line including its terminator.
func (l Line) Text() string { return l.Raw + l.EOL }

// Is reports whether the line is a directive whose keyword equals kw, compared
// case-insensitively as ssh does.
func (l Line) Is(kw string) bool {
	return l.Kind == LineDirective && strings.EqualFold(l.Keyword, kw)
}

// Arg returns argument i, or "" when it does not exist.
func (l Line) Arg(i int) string {
	if i < 0 || i >= len(l.Args) {
		return ""
	}
	return l.Args[i]
}

// parseLine splits one raw line into its structural parts. It never fails: a
// line it cannot make sense of becomes a directive with no arguments, or a
// blank, and is reproduced verbatim either way.
func parseLine(raw string) Line {
	l := Line{Raw: raw}

	i := 0
	for i < len(raw) && isSpace(raw[i]) {
		i++
	}
	l.Indent = raw[:i]
	if i == len(raw) {
		l.Kind = LineBlank
		return l
	}
	if raw[i] == '#' {
		l.Kind = LineComment
		return l
	}

	l.Kind = LineDirective

	// The keyword runs until whitespace or '='. ssh accepts "Key value",
	// "Key=value" and "Key = value" interchangeably.
	start := i
	for i < len(raw) && !isSpace(raw[i]) && raw[i] != '=' {
		i++
	}
	l.Keyword = raw[start:i]

	sepStart := i
	for i < len(raw) && isSpace(raw[i]) {
		i++
	}
	if i < len(raw) && raw[i] == '=' {
		i++
		for i < len(raw) && isSpace(raw[i]) {
			i++
		}
	}
	l.Sep = raw[sepStart:i]
	l.ArgsRaw = raw[i:]
	l.Args = splitArgs(l.ArgsRaw)
	return l
}

// splitArgs tokenises an argument string the way ssh's argv parser does:
// whitespace separates, double quotes group, and a comment marker only counts
// when it begins a token outside quotes.
func splitArgs(s string) []string {
	var (
		args  []string
		cur   strings.Builder
		inTok bool
		inQ   bool
	)
	flush := func() {
		if inTok {
			args = append(args, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQ = !inQ
			inTok = true
		case inQ:
			cur.WriteByte(c)
			inTok = true
		case isSpace(c):
			flush()
		case c == '#' && !inTok:
			return args // rest of the line is a trailing comment
		default:
			cur.WriteByte(c)
			inTok = true
		}
	}
	flush()
	return args
}

// quoteArg wraps a value in double quotes when it contains characters that
// would otherwise split it into several arguments.
func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"#=") {
		return `"` + strings.ReplaceAll(s, `"`, ``) + `"`
	}
	return s
}

// joinArgs renders arguments back into a single argument string.
func joinArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = quoteArg(a)
	}
	return strings.Join(parts, " ")
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

// foldEqual compares two ssh keywords or host names case-insensitively.
func foldEqual(a, b string) bool { return strings.EqualFold(a, b) }

// isPattern reports whether a Host pattern contains wildcard characters, in
// which case it names a class of hosts rather than a single one.
func isPattern(s string) bool { return strings.ContainsAny(s, "*?!") }

// hasUpper is used when deciding whether to normalise a keyword's case.
func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
