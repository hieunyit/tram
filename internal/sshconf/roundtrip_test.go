package sshconf

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func corpus(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.conf"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("corpus not found: %v", err)
	}
	return paths
}

// TestRoundTripByteForByte is the parser's first obligation: reading a file and
// writing it back changes nothing at all.
func TestRoundTripByteForByte(t *testing.T) {
	for _, p := range corpus(t) {
		p := p
		t.Run(filepath.Base(p), func(t *testing.T) {
			want, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			got := Parse(want, p).Bytes()
			if !bytes.Equal(got, want) {
				t.Errorf("round trip changed the file\n--- want ---\n%q\n--- got ---\n%q", want, got)
			}
		})
	}
}

// TestNoOpEditPreservesBytes checks that touching a stanza without changing any
// value leaves the file alone, which is what makes repeated tram runs safe.
func TestNoOpEditPreservesBytes(t *testing.T) {
	for _, p := range corpus(t) {
		p := p
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, _ := os.ReadFile(p)
			f := Parse(raw, p)
			for _, b := range f.Blocks {
				if b.Type != BlockHost {
					continue
				}
				name := b.GetOne("HostName")
				if name == "" {
					continue
				}
				SetDirectives(b, []Directive{D("HostName", name)}, nil)
				break
			}
			if !bytes.Equal(f.Bytes(), raw) {
				t.Errorf("rewriting a value with the same value changed the file\n%s", UnifiedDiff(p, raw, f.Bytes()))
			}
		})
	}
}

// TestCommentsStayWithTheirStanza covers the rule that a comment inside a Host
// block dies with that block instead of drifting to the top of the file where
// it would document the wrong thing, and that a comment introducing a stanza
// goes with it too.
func TestCommentsStayWithTheirStanza(t *testing.T) {
	src := `# global note
ServerAliveInterval 30

# introduces a
Host a
    HostName 1.1.1.1
    # note inside a
    User x

# introduces b
Host b
    HostName 2.2.2.2
`
	f := Parse([]byte(src), "t.conf")
	b := findBlock(f, "a")
	if b == nil {
		t.Fatal("stanza a not found")
	}
	RemoveHost(b)
	got := f.String()

	for _, gone := range []string{"introduces a", "note inside a", "1.1.1.1"} {
		if strings.Contains(got, gone) {
			t.Errorf("removing host a left %q behind:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"global note", "ServerAliveInterval 30", "introduces b", "2.2.2.2"} {
		if !strings.Contains(got, kept) {
			t.Errorf("removing host a also removed %q:\n%s", kept, got)
		}
	}
}

// TestCommentBelowDirectiveStaysAbove guards the other half of the rule: a
// comment written directly under a directive belongs to the stanza above it,
// not to the stanza that follows.
func TestCommentBelowDirectiveStaysAbove(t *testing.T) {
	src := "Host a\n    HostName 1.1.1.1\n# trailing note for a\nHost b\n    HostName 2.2.2.2\n"
	f := Parse([]byte(src), "t.conf")
	RemoveHost(findBlock(f, "b"))
	if !strings.Contains(f.String(), "trailing note for a") {
		t.Errorf("comment attached to the wrong stanza:\n%s", f.String())
	}
}

// TestRepeatedKeywordsKeepOrder checks that ssh's first-value-wins keywords and
// its accumulating keywords both survive a rewrite.
func TestRepeatedKeywordsKeepOrder(t *testing.T) {
	src := "Host k\n    IdentityFile ~/.ssh/a\n    IdentityFile ~/.ssh/b\n    Port 22\n    Port 2222\n"
	f := Parse([]byte(src), "t.conf")
	b := findBlock(f, "k")
	if got := b.GetAll("IdentityFile"); len(got) != 2 || got[0] != "~/.ssh/a" || got[1] != "~/.ssh/b" {
		t.Errorf("IdentityFile list = %v", got)
	}
	if got := b.GetOne("Port"); got != "22" {
		t.Errorf("Port = %q, ssh uses the first value", got)
	}

	SetDirectives(b, []Directive{D("IdentityFile", "~/.ssh/x"), D("IdentityFile", "~/.ssh/y"), D("IdentityFile", "~/.ssh/z")}, nil)
	want := "Host k\n    IdentityFile ~/.ssh/x\n    IdentityFile ~/.ssh/y\n    IdentityFile ~/.ssh/z\n    Port 22\n    Port 2222\n"
	if f.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", f.String(), want)
	}
}

// TestUnsetRemovesEveryOccurrence checks that clearing a keyword clears all of
// its lines, not just the first.
func TestUnsetRemovesEveryOccurrence(t *testing.T) {
	src := "Host k\n    IdentityFile ~/.ssh/a\n    User x\n    IdentityFile ~/.ssh/b\n"
	f := Parse([]byte(src), "t.conf")
	SetDirectives(findBlock(f, "k"), nil, []string{"identityfile"})
	if strings.Contains(f.String(), "IdentityFile") {
		t.Errorf("unset left a line behind:\n%s", f.String())
	}
	if !strings.Contains(f.String(), "User x") {
		t.Errorf("unset removed an unrelated line:\n%s", f.String())
	}
}

// TestAddHostMatchesFileStyle checks that a stanza tram writes is indented the
// way the rest of the file is, including tabs and CRLF.
func TestAddHostMatchesFileStyle(t *testing.T) {
	src := "Host a\r\n\tHostName 1.1.1.1\r\n"
	f := Parse([]byte(src), "t.conf")
	if _, err := AddHost(f, []string{"new"}, []Directive{D("HostName", "9.9.9.9")}, ""); err != nil {
		t.Fatal(err)
	}
	got := f.String()
	if !strings.Contains(got, "\r\n\tHostName 9.9.9.9\r\n") {
		t.Errorf("new stanza did not follow the file's style:\n%q", got)
	}
}

// TestFuzzRoundTrip generates configurations out of the awkward fragments and
// checks that none of them can be lost or reordered by a read and write.
func TestFuzzRoundTrip(t *testing.T) {
	frags := []string{
		"Host a\n", "Host b c\n", "Host *.example.com\n", "Match host x\n",
		"    HostName 1.2.3.4\n", "\tUser deploy\n", "  Port=2222\n", "Port = 22\n",
		"# a comment\n", "\n", "   \n", "\t\n",
		"    IdentityFile ~/.ssh/id_ed25519\n", "    ProxyJump bastion\n",
		"    UnknownKeyword a b c\n", "    Quoted \"a b\" c\n",
		"Host d\r\n", "    HostName 5.5.5.5\r\n",
	}
	rng := rand.New(rand.NewSource(20260909))
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		for n := rng.Intn(20); n >= 0; n-- {
			b.WriteString(frags[rng.Intn(len(frags))])
		}
		src := b.String()
		if i%3 == 0 {
			src = strings.TrimSuffix(src, "\n")
		}
		f := Parse([]byte(src), "fuzz.conf")
		if got := f.String(); got != src {
			t.Fatalf("round trip lost data\nin:  %q\nout: %q", src, got)
		}
	}
}

func findBlock(f *File, name string) *Block {
	for _, b := range f.Blocks {
		for _, n := range b.Names() {
			if foldEqual(n, name) {
				return b
			}
		}
	}
	return nil
}
