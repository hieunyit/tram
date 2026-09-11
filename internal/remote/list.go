package remote

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one name in a directory, on either side of the browser. The local
// pane fills the same struct from the filesystem, so one renderer draws both.
type Entry struct {
	Name  string
	Size  int64
	Mode  string
	Time  time.Time
	IsDir bool
	// Link is the target of a symbolic link, empty when the entry is not one.
	// A link to a directory is drawn and entered as a directory.
	Link string
}

// List changes to a directory and reads it.
//
// The change of directory is kept: the shell stays where it was put, so the
// next listing is relative to this one and the browser's idea of where it is
// and the shell's cannot drift apart.
func (s *Session) List(path string) (cwd string, entries []Entry, err error) {
	cd := "cd -- " + Quote(path)
	if path == "" {
		cd = "cd" // no argument means home, which is where a browser opens
	}
	// pwd first, so that the path comes back resolved even when ls says
	// nothing at all, and LC_ALL so that the month names are the ones the
	// parser knows.
	lines, status, err := s.Run(cd + " && pwd && LC_ALL=C ls -lA")
	if err != nil {
		return "", nil, err
	}
	if len(lines) == 0 {
		return "", nil, fmt.Errorf("%s said nothing about %s", s.Host, path)
	}
	// A failed cd leaves the shell where it was, and the first line is the
	// complaint rather than a path.
	cwd = strings.TrimSpace(lines[0])
	if status != 0 && !strings.HasPrefix(cwd, "/") {
		return "", nil, fmt.Errorf("%s", firstComplaint(lines))
	}

	for _, l := range lines[1:] {
		if e, ok := ParseLine(l); ok {
			entries = append(entries, e)
		}
	}
	SortEntries(entries)
	return cwd, entries, nil
}

// Home is where the browser opens, which is where ssh puts you.
func (s *Session) Home() (string, error) {
	lines, _, err := s.Run("pwd")
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%s did not say where it put us", s.Host)
	}
	return strings.TrimSpace(lines[0]), nil
}

// Mkdir makes a directory.
func (s *Session) Mkdir(path string) error { return s.do("mkdir -- " + Quote(path)) }

// Rename moves a name to another, which is also how a file is renamed.
func (s *Session) Rename(from, to string) error {
	return s.do("mv -- " + Quote(from) + " " + Quote(to))
}

// Remove deletes a name. A directory has to be named as one, so that a slip of
// the finger cannot turn into a recursive delete of something else.
func (s *Session) Remove(path string, dir bool) error {
	if dir {
		return s.do("rm -rf -- " + Quote(path))
	}
	return s.do("rm -f -- " + Quote(path))
}

// do runs a command that is expected to say nothing, and turns whatever it did
// say into the error.
func (s *Session) do(command string) error {
	lines, status, err := s.Run(command)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("%s", firstComplaint(lines))
	}
	return nil
}

// ParseLine reads one line of `ls -l` output.
//
// This is the one piece of guesswork in the package, and it is deliberately
// tolerant: `ls` has no machine-readable form, its columns move between
// systems, and a name can contain spaces. A line it cannot read is dropped
// rather than shown wrongly, and the fields it is least sure of are the ones it
// leaves empty.
func ParseLine(line string) (Entry, bool) {
	line = strings.TrimRight(line, "\r")
	if line == "" || strings.HasPrefix(line, "total ") {
		return Entry{}, false
	}
	// The mode string is the one anchor: ten or eleven characters, a type and
	// nine permissions, optionally an access-control mark.
	fields := strings.Fields(line)
	if len(fields) < 8 || len(fields[0]) < 10 {
		return Entry{}, false
	}
	mode := fields[0]
	switch mode[0] {
	case '-', 'd', 'l', 'b', 'c', 'p', 's':
	default:
		return Entry{}, false
	}

	// Size is the last field before the date, and the date is three fields.
	// Counting from the date backwards survives the two shapes of the owner
	// columns: some systems print a group, some do not.
	date, at := findDate(fields)
	if at < 1 {
		return Entry{}, false
	}
	size, _ := strconv.ParseInt(fields[at-1], 10, 64)

	// The name is everything after the three date fields, which is how a name
	// with spaces in it survives. Cutting the original line rather than joining
	// the fields keeps runs of spaces inside the name intact.
	name := nameAfter(line, fields, at+3)
	if name == "" || name == "." || name == ".." {
		return Entry{}, false
	}

	e := Entry{Name: name, Size: size, Mode: mode, Time: date, IsDir: mode[0] == 'd'}
	if mode[0] == 'l' {
		if i := strings.Index(name, " -> "); i >= 0 {
			e.Link = name[i+4:]
			e.Name = name[:i]
			// A link is entered as a directory when it points at one, which
			// only the far end can say. The trailing slash ls prints with -F
			// is not there, so the browser finds out by trying.
			e.IsDir = strings.HasSuffix(e.Link, "/")
		}
	}
	return e, true
}

// findDate locates the three fields ls writes the time in, and returns the
// index of the first of them.
//
// The two shapes are "Jan  2 15:04" for anything recent and "Jan  2  2006" for
// anything older than six months. Both start with a month name, which is what
// makes them findable without knowing how many columns came before.
func findDate(fields []string) (time.Time, int) {
	for i := 2; i+2 < len(fields); i++ {
		month, ok := months[fields[i]]
		if !ok {
			continue
		}
		day, err := strconv.Atoi(fields[i+1])
		if err != nil || day < 1 || day > 31 {
			continue
		}
		last := fields[i+2]
		year, now := time.Now().Year(), time.Now()
		if y, err := strconv.Atoi(last); err == nil && y > 1900 {
			return time.Date(y, month, day, 0, 0, 0, 0, time.Local), i
		}
		if h, m, ok := parseClock(last); ok {
			t := time.Date(year, month, day, h, m, 0, 0, time.Local)
			// ls prints a clock only for the recent past. A date in the future
			// is last year's, which is what ls means by it.
			if t.After(now.AddDate(0, 1, 0)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t, i
		}
	}
	return time.Time{}, -1
}

func parseClock(s string) (h, m int, ok bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// nameAfter returns the rest of the line after the nth field, with the spaces
// inside it left alone.
func nameAfter(line string, fields []string, n int) string {
	pos, count := 0, 0
	for pos < len(line) {
		for pos < len(line) && line[pos] == ' ' {
			pos++
		}
		if count == n {
			return line[pos:]
		}
		for pos < len(line) && line[pos] != ' ' {
			pos++
		}
		count++
	}
	if n < len(fields) {
		return fields[n]
	}
	return ""
}

var months = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March,
	"Apr": time.April, "May": time.May, "Jun": time.June,
	"Jul": time.July, "Aug": time.August, "Sep": time.September,
	"Oct": time.October, "Nov": time.November, "Dec": time.December,
}

// SortEntries puts directories first and then sorts by name, which is the order
// a file browser is read in.
func SortEntries(e []Entry) {
	sort.SliceStable(e, func(i, j int) bool {
		if e[i].IsDir != e[j].IsDir {
			return e[i].IsDir
		}
		return strings.ToLower(e[i].Name) < strings.ToLower(e[j].Name)
	})
}
