package remote

import (
	"testing"
	"time"
)

// TestParseLineReadsRealListings is the whole risk of this package in one test.
//
// `ls -l` has no machine-readable form. Its columns differ between systems, a
// name may contain spaces, and a line read wrongly would be a file drawn with
// somebody else's size. Every shape below came off a real system.
func TestParseLineReadsRealListings(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		want  Entry
		valid bool
	}{
		{
			name:  "gnu, a recent file",
			line:  "-rw-r--r--  1 hieuny hieuny     4096 Sep 11 09:32 syslog",
			want:  Entry{Name: "syslog", Size: 4096, Mode: "-rw-r--r--"},
			valid: true,
		},
		{
			name:  "gnu, a directory older than six months",
			line:  "drwxr-xr-x  4 root   root       4096 Jan  3  2024 nginx",
			want:  Entry{Name: "nginx", Size: 4096, Mode: "drwxr-xr-x", IsDir: true},
			valid: true,
		},
		{
			name:  "a name with spaces in it",
			line:  "-rw-r--r--  1 root root  17 Aug  1 11:02 a file with spaces.txt",
			want:  Entry{Name: "a file with spaces.txt", Size: 17, Mode: "-rw-r--r--"},
			valid: true,
		},
		{
			name:  "a symbolic link",
			line:  "lrwxrwxrwx  1 root root  7 Feb 14  2025 latest -> current",
			want:  Entry{Name: "latest", Size: 7, Mode: "lrwxrwxrwx", Link: "current"},
			valid: true,
		},
		{
			name:  "an access control mark after the mode",
			line:  "-rw-rw-r--+ 1 hieuny hieuny 128 Mar  9 14:00 shared.conf",
			want:  Entry{Name: "shared.conf", Size: 128, Mode: "-rw-rw-r--+"},
			valid: true,
		},
		{
			name:  "busybox, which prints no group",
			line:  "-rw-r--r--    1 root          641 Sep  2 08:15 inittab",
			want:  Entry{Name: "inittab", Size: 641, Mode: "-rw-r--r--"},
			valid: true,
		},
		{name: "the total line", line: "total 48"},
		{name: "an error from ls", line: "ls: cannot open directory '/root': Permission denied"},
		{name: "nothing at all", line: ""},
		{name: "the current directory", line: "drwxr-xr-x  2 root root 4096 Sep 11 09:32 ."},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseLine(c.line)
			if ok != c.valid {
				t.Fatalf("readable = %v, want %v (got %+v)", ok, c.valid, got)
			}
			if !c.valid {
				return
			}
			if got.Name != c.want.Name {
				t.Errorf("name = %q, want %q", got.Name, c.want.Name)
			}
			if got.Size != c.want.Size {
				t.Errorf("size = %d, want %d", got.Size, c.want.Size)
			}
			if got.Mode != c.want.Mode {
				t.Errorf("mode = %q, want %q", got.Mode, c.want.Mode)
			}
			if got.IsDir != c.want.IsDir {
				t.Errorf("directory = %v, want %v", got.IsDir, c.want.IsDir)
			}
			if got.Link != c.want.Link {
				t.Errorf("link = %q, want %q", got.Link, c.want.Link)
			}
			if got.Time.IsZero() {
				t.Error("no time was read")
			}
		})
	}
}

// TestAClockInTheFutureIsLastYear covers the one thing ls leaves out: a listing
// with a clock in it carries no year, and a date that has not happened yet is
// the same date a year ago.
func TestAClockInTheFutureIsLastYear(t *testing.T) {
	ahead := time.Now().AddDate(0, 3, 0)
	line := "-rw-r--r-- 1 root root 10 " + ahead.Format("Jan  2 15:04") + " later.txt"

	got, ok := ParseLine(line)
	if !ok {
		t.Fatalf("the line was not read: %q", line)
	}
	if got.Time.After(time.Now()) {
		t.Errorf("time = %s, which has not happened yet", got.Time)
	}
	if got.Time.Year() != ahead.Year()-1 {
		t.Errorf("year = %d, want %d", got.Time.Year(), ahead.Year()-1)
	}
}

// TestQuoteSurvivesAwkwardNames guards the one place where a file name reaches
// a shell.
func TestQuoteSurvivesAwkwardNames(t *testing.T) {
	cases := map[string]string{
		"plain.txt":           `'plain.txt'`,
		"with space.txt":      `'with space.txt'`,
		"it's here":           `'it'\''s here'`,
		"; rm -rf /":          `'; rm -rf /'`,
		"$(whoami)":           `'$(whoami)'`,
		"`id`":                "'`id`'",
		"back\\slash":         `'back\slash'`,
		"--looks-like-a-flag": `'--looks-like-a-flag'`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestSortPutsDirectoriesFirst is the order a file browser is read in.
func TestSortPutsDirectoriesFirst(t *testing.T) {
	e := []Entry{
		{Name: "zebra.txt"},
		{Name: "Alpha", IsDir: true},
		{Name: "apple.txt"},
		{Name: "beta", IsDir: true},
	}
	SortEntries(e)

	want := []string{"Alpha", "beta", "apple.txt", "zebra.txt"}
	for i, w := range want {
		if e[i].Name != w {
			t.Fatalf("order = %v, want %v", names(e), want)
		}
	}
}

func names(e []Entry) []string {
	out := make([]string, len(e))
	for i, x := range e {
		out[i] = x.Name
	}
	return out
}
