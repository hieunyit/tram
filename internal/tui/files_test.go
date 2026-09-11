package tui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/remote"
)

// fakeFS is a machine with three directories on it. The browser is driven
// against this rather than against ssh, which is the only way any of it can be
// tested at all.
type fakeFS struct {
	cwd    string
	tree   map[string][]remote.Entry
	closed bool
	// removed and made record what the browser asked for.
	removed []string
	made    []string
	renamed [2]string
}

func newFakeFS() *fakeFS {
	old := time.Date(2025, 3, 4, 9, 0, 0, 0, time.Local)
	return &fakeFS{
		cwd: "/home/hieuny",
		tree: map[string][]remote.Entry{
			"/home/hieuny": {
				{Name: "logs", IsDir: true, Time: old},
				{Name: "notes.txt", Size: 1200, Time: old},
			},
			"/home/hieuny/logs": {
				{Name: "syslog", Size: 4096, Time: old},
			},
		},
	}
}

func (f *fakeFS) List(p string) (string, []remote.Entry, error) {
	switch {
	case p == "" || p == ".":
	case p == "..":
		f.cwd = path.Dir(f.cwd)
	case strings.HasPrefix(p, "/"):
		f.cwd = p
	default:
		f.cwd = path.Join(f.cwd, p)
	}
	list, ok := f.tree[f.cwd]
	if !ok {
		return "", nil, fmt.Errorf("cd: %s: No such file or directory", f.cwd)
	}
	return f.cwd, list, nil
}

func (f *fakeFS) Mkdir(name string) error { f.made = append(f.made, name); return nil }
func (f *fakeFS) Rename(a, b string) error {
	f.renamed = [2]string{a, b}
	return nil
}
func (f *fakeFS) Remove(name string, dir bool) error {
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeFS) Close() error { f.closed = true; return nil }

// filesRunner lends the browser a machine and records every copy asked of it.
type filesRunner struct {
	nullRunner
	fs     *fakeFS
	copies []remote.Copy
	fail   error
}

func (r *filesRunner) Files(h model.Host) (FileSystem, error) {
	if r.fail != nil {
		return nil, r.fail
	}
	return r.fs, nil
}

func (r *filesRunner) Copy(job remote.Copy) error {
	r.copies = append(r.copies, job)
	return nil
}

// browser opens the two-pane screen on a temporary directory of real files, so
// that the near side is as real as the far side is fake.
func browser(t *testing.T) (*Model, *filesRunner, string) {
	t.Helper()
	m := wide(t)
	r := &filesRunner{fs: newFakeFS()}
	m.Runner = r

	dir := t.TempDir()
	for _, name := range []string{"report.md", "data.csv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, cmd := m.Update(key("f"))
	drain(m, cmd)
	if m.screen != screenFiles {
		t.Fatalf("f did not open the browser: %v", m.problem)
	}
	// The near side opens wherever tram was started; the test points it at its
	// own directory instead.
	drain(m, m.localCmd(dir))
	return m, r, dir
}

// TestBrowserShowsBothSides is the shape of the screen: this machine on the
// left, the host on the right, each with its own path and its own list.
func TestBrowserShowsBothSides(t *testing.T) {
	m, _, dir := browser(t)

	out := m.View()
	for _, want := range []string{
		"THIS MACHINE", strings.ToUpper("bastion"),
		"report.md", "data.csv", "out/", // the near side
		"logs/", "notes.txt", // the far side
		"/home/hieuny",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the browser does not show %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, filepath.Base(dir)) {
		t.Errorf("the near side does not show where it is:\n%s", out)
	}
}

// TestBrowserWalksTheFarSide covers entering a directory and coming back, and
// that the cursor and the marks do not survive the move.
func TestBrowserWalksTheFarSide(t *testing.T) {
	m, _, _ := browser(t)
	send(m, "tab") // to the host side
	if !m.files.onFar {
		t.Fatal("tab did not move to the host")
	}

	// The way up is first, then the directories, then the files. It cannot be
	// marked, because it is a door rather than a file.
	send(m, " ")
	if len(m.files.far.marked) != 0 {
		t.Fatal("the way up was marked")
	}
	send(m, "down", " ")
	if len(m.files.far.marked) != 1 {
		t.Fatal("space did not mark")
	}
	m.files.far.cursor = 1 // logs

	_, cmd := m.Update(key("enter"))
	drain(m, cmd)
	if m.files.far.dir != "/home/hieuny/logs" {
		t.Fatalf("enter went to %q", m.files.far.dir)
	}
	if len(m.files.far.marked) != 0 {
		t.Error("the marks followed us into another directory")
	}
	if !strings.Contains(m.View(), "syslog") {
		t.Errorf("the new directory is not drawn:\n%s", m.View())
	}

	_, cmd = m.Update(key("u"))
	drain(m, cmd)
	if m.files.far.dir != "/home/hieuny" {
		t.Errorf("u went to %q", m.files.far.dir)
	}
}

// TestCopyAsksAndThenCopies is the point of the screen. Nothing is transferred
// until the question in the bar is answered.
func TestCopyAsksAndThenCopies(t *testing.T) {
	m, r, dir := browser(t)

	// Two files on the near side, sent to the host.
	m.files.local.marked["report.md"] = true
	m.files.local.marked["data.csv"] = true

	_, cmd := m.Update(key("c"))
	drain(m, cmd)
	if m.mode != modeConfirm {
		t.Fatal("copying did not ask first")
	}
	if len(r.copies) != 0 {
		t.Fatal("something was copied before the question was answered")
	}
	if !strings.Contains(m.View(), "copy 2 item(s) to bastion") {
		t.Errorf("the question does not say what it will do:\n%s", m.View())
	}

	send(m, "y")
	if len(r.copies) != 2 {
		t.Fatalf("%d copies were made, want 2", len(r.copies))
	}
	for _, c := range r.copies {
		if !c.Up {
			t.Error("a copy from this machine went the wrong way")
		}
		if !strings.HasPrefix(c.Local, dir) {
			t.Errorf("the near path is %q, which is not in the directory shown", c.Local)
		}
		if !strings.HasPrefix(c.Remote, "/home/hieuny/") {
			t.Errorf("the far path is %q, which is not the directory shown", c.Remote)
		}
	}
}

// TestCopyFromTheHostGoesTheOtherWay checks the direction is taken from the
// side the cursor is on.
func TestCopyFromTheHostGoesTheOtherWay(t *testing.T) {
	m, r, _ := browser(t)
	send(m, "tab")
	m.files.far.cursor = 2 // notes.txt, after the way up and the logs directory

	_, cmd := m.Update(key("c"))
	drain(m, cmd)
	send(m, "y")

	if len(r.copies) != 1 {
		t.Fatalf("%d copies, want 1", len(r.copies))
	}
	if c := r.copies[0]; c.Up {
		t.Error("a copy from the host was sent to the host")
	} else if c.Remote != "/home/hieuny/notes.txt" {
		t.Errorf("the far path is %q", c.Remote)
	}
}

// TestDeleteAsksOnBothSides guards the one action that cannot be undone.
func TestDeleteAsksOnBothSides(t *testing.T) {
	m, _, dir := browser(t)
	send(m, "down") // past the way up, onto the first real name

	_, cmd := m.Update(key("d"))
	drain(m, cmd)
	if m.mode != modeConfirm {
		t.Fatal("delete did not ask")
	}
	if !strings.Contains(m.View(), "this machine") {
		t.Errorf("the question does not say where it will delete:\n%s", m.View())
	}
	send(m, "n")
	if _, err := os.Stat(filepath.Join(dir, "data.csv")); err != nil {
		t.Errorf("answering no deleted the file anyway: %v", err)
	}

	// And on the far side it goes through the connection rather than the disk.
	send(m, "tab", "down")
	_, cmd = m.Update(key("d"))
	drain(m, cmd)
	send(m, "y")
	fs := m.files.sess.(*fakeFS)
	if len(fs.removed) != 1 || fs.removed[0] != "logs" {
		t.Errorf("the host was asked to remove %v", fs.removed)
	}
}

// TestLeavingTheBrowserClosesTheConnection is the housekeeping that matters:
// the browser holds an ssh connection open, and it must not outlive the screen.
func TestLeavingTheBrowserClosesTheConnection(t *testing.T) {
	m, r, _ := browser(t)
	send(m, "esc")

	if m.screen != screenList {
		t.Error("escape did not go back to the hosts")
	}
	if m.files != nil {
		t.Error("the browser is still open")
	}
	if !r.fs.closed {
		t.Error("the connection was left open")
	}
}

// TestTheBrowserSaysWhyItCouldNotOpen covers the common case of a host that is
// not answering: the failure belongs in the bar, not in a blank screen.
func TestTheBrowserSaysWhyItCouldNotOpen(t *testing.T) {
	m := wide(t)
	m.Runner = &filesRunner{fs: newFakeFS(), fail: fmt.Errorf("bastion: Connection refused")}

	_, cmd := m.Update(key("f"))
	drain(m, cmd)

	if m.screen == screenFiles {
		t.Fatal("the browser opened on a host that refused")
	}
	if !strings.Contains(m.View(), "Connection refused") {
		t.Errorf("the failure is not on screen:\n%s", m.View())
	}
}

// TestBrowserFitsTheTerminal is the same guard the other screens have.
func TestBrowserFitsTheTerminal(t *testing.T) {
	m, _, _ := browser(t)
	for _, size := range [][2]int{{150, 26}, {100, 30}, {80, 20}, {60, 12}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(ansi.ReplaceAllString(m.View(), ""), "\n")
		if len(lines) > size[1] {
			t.Errorf("the browser is %d lines at %dx%d", len(lines), size[0], size[1])
		}
	}
}

// TestTheWayUpIsOnScreen is what a real session found missing: u and backspace
// walk out of a directory, but nothing said so and a pointer had nowhere to
// click.
func TestTheWayUpIsOnScreen(t *testing.T) {
	m, _, _ := browser(t)

	if got := m.files.far.entries[0].Name; got != ".." {
		t.Errorf("the host side opens on %q, want the way up first", got)
	}
	if got := m.files.local.entries[0].Name; got != ".." {
		t.Errorf("this side opens on %q, want the way up first", got)
	}
	if !strings.Contains(m.View(), "../") {
		t.Errorf("the way up is not drawn:\n%s", m.View())
	}

	// Walking out of a directory leaves the cursor standing on it.
	send(m, "tab", "down") // onto logs
	_, cmd := m.Update(key("enter"))
	drain(m, cmd)
	if m.files.far.dir != "/home/hieuny/logs" {
		t.Fatalf("enter went to %q", m.files.far.dir)
	}

	_, cmd = m.Update(key("enter")) // the way up is under the cursor again
	drain(m, cmd)
	if m.files.far.dir != "/home/hieuny" {
		t.Fatalf("the way up went to %q", m.files.far.dir)
	}
	if e, _ := m.files.far.at(); e.Name != "logs" {
		t.Errorf("coming back left the cursor on %q, want the directory just left", e.Name)
	}
}

// TestARootHasNoWayUp checks that the row is not drawn where it would lead
// nowhere.
func TestARootHasNoWayUp(t *testing.T) {
	list := []remote.Entry{{Name: "etc", IsDir: true}}
	if got := withParent("/", list, false); len(got) != 1 {
		t.Errorf("a root was given a way up: %v", got[0].Name)
	}
	if got := withParent("/home", list, false); got[0].Name != ".." {
		t.Errorf("a directory below the root has no way up: %v", got[0].Name)
	}
}
