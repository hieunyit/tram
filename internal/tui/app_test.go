package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
)

type nullRunner struct{}

// Measure answers the way a small, healthy fleet would, so that the columns the
// design fills in have something to show in the tests.
func (nullRunner) Measure(hosts []model.Host) []Measurement {
	out := make([]Measurement, len(hosts))
	for i, h := range hosts {
		out[i] = Measurement{Host: h.Name, Class: "OK", OK: true, Millis: int64(3 + i),
			OS: "Linux 6.1", Load: "0.10", Uptime: "3 days", Disk: "34%", RAM: "51%"}
	}
	return out
}

func (nullRunner) Agent() string                   { return "agent 2 keys" }
func (nullRunner) Doctor(hosts []model.Host) []Row { return nil }
func (nullRunner) Exec(hosts []model.Host, command string) []Row {
	return []Row{{Host: hosts[0].Name, Status: "OK", OK: true, Summary: "exit 0", Body: command}}
}

func newModel(t *testing.T) *Model {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sshDir, "config")
	src := "Host bastion\n    #tram-group: prod\n    HostName b.example.com\n\n" +
		"Host web1\n    #tram-group: prod/web\n    HostName 10.0.0.1\n    ProxyJump bastion\n\n" +
		"Host laptop\n    HostName 192.168.1.5\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRAM_HOME", filepath.Join(home, "state"))
	t.Setenv("TRAM_SSH_DIR", sshDir)
	t.Setenv("TRAM_SSH_CONFIG", path)

	inv, err := inventory.Load(inventory.Options{ConfigPath: path, SSHDir: sshDir, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	m := New(inv, nullRunner{}, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// key builds the message a real terminal would send, so the tests exercise the
// same branches the interface takes in use rather than a convenient fiction.
var specialKeys = map[string]tea.KeyType{
	"enter":  tea.KeyEnter,
	"down":   tea.KeyDown,
	"up":     tea.KeyUp,
	"esc":    tea.KeyEsc,
	"space":  tea.KeySpace,
	"tab":    tea.KeyTab,
	"left":   tea.KeyLeft,
	"right":  tea.KeyRight,
	"ctrl+s": tea.KeyCtrlS,
	"ctrl+c": tea.KeyCtrlC,
}

func key(s string) tea.KeyMsg {
	if t, ok := specialKeys[s]; ok {
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// send delivers keys and drains the commands they produce.
//
// The draining is bounded on purpose. A focused text input schedules a cursor
// blink, whose message schedules the next one, so following the chain to its
// end never ends. A handful of rounds is enough to settle everything the tests
// care about.
func send(m *Model, keys ...string) {
	for _, k := range keys {
		_, cmd := m.Update(key(k))
		drain(m, cmd)
	}
}

func drain(m *Model, cmd tea.Cmd) {
	for i := 0; cmd != nil && i < 8; i++ {
		msg := cmd()
		if msg == nil {
			return
		}
		// A batch is a list of commands, and the runtime runs every one of
		// them. A harness that followed only the first would quietly drop the
		// half of the work that matters.
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				drain(m, c)
			}
			return
		}
		if _, ok := msg.(cursor.BlinkMsg); ok {
			return
		}
		_, cmd = m.Update(msg)
	}
}

// TestListRendersAndNavigates is a smoke test over the drawing code: the point
// is that no view panics and that the rows shown are the ones expected.
func TestListRendersAndNavigates(t *testing.T) {
	m := newModel(t)
	out := m.View()
	for _, want := range []string{"TRAM", "HOSTS", "bastion", "web1", "laptop", "ALIAS", "USER@HOST"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list does not show %q:\n%s", want, out)
		}
	}
	if got := len(m.filtered); got != 3 {
		t.Fatalf("%d hosts listed, want 3", got)
	}

	// The table is sorted by alias, as the design has it, so one step down is
	// laptop rather than whatever the file happened to list second.
	send(m, "down")
	if h, _ := m.current(); h.Name != "laptop" {
		t.Errorf("after one step down the cursor is on %q, want laptop", h.Name)
	}

	// The details pane is part of the layout rather than something to go
	// looking for, so it is showing before any key is pressed. It needs a wide
	// enough window to earn its room.
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	if !strings.Contains(m.View(), "CONNECTION") || !strings.Contains(m.View(), "192.168.1.5") {
		t.Errorf("the details pane does not show the host:\n%s", m.View())
	}
	send(m, "i")
	if m.detail {
		t.Error("i did not put the details pane away")
	}
	if strings.Contains(m.View(), "CONNECTION") {
		t.Error("the details pane is still drawn after i")
	}
}

// TestSearchFiltersAsYouType covers the search overlay, including the #group
// form that filters by group only.
func TestSearchFiltersAsYouType(t *testing.T) {
	m := newModel(t)
	send(m, "/", "w", "e", "b")
	if len(m.filtered) != 1 || m.filtered[0].Name != "web1" {
		t.Errorf("searching for web gave %v", model.Names(m.filtered))
	}
	send(m, "esc", "esc")
	if len(m.filtered) != 3 {
		t.Errorf("escape did not clear the search: %v", model.Names(m.filtered))
	}

	send(m, "/", "#", "p", "r", "o", "d")
	if len(m.filtered) != 2 {
		t.Errorf("a #group search gave %v, want the two prod hosts", model.Names(m.filtered))
	}
}

// TestConnectLeavesTheInterface is the handoff contract: the interface cannot
// open a session itself, so enter must quit and name the host.
func TestConnectLeavesTheInterface(t *testing.T) {
	m := newModel(t)
	send(m, "down", "down")
	mm, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter produced no command; it must quit so ssh can have the terminal")
	}
	_ = mm
	out := m.Outcome()
	if out.Action != ActionConnect || out.Host != "web1" {
		t.Errorf("outcome = %+v, want a connect to web1", out)
	}
}

// TestMarkingSelectsSeveralHosts checks that actions apply to every marked host
// rather than only to the one under the cursor.
func TestMarkingSelectsSeveralHosts(t *testing.T) {
	m := newModel(t)
	send(m, "space", "space")
	if got := len(m.selection()); got != 2 {
		t.Fatalf("%d hosts selected after two marks, want 2", got)
	}
	if !strings.Contains(m.View(), "marked 2") {
		t.Errorf("the top bar does not report the marks:\n%s", m.View())
	}
}

// TestFormWritesTheConfig drives the add form the way a user would and checks
// that the file on disk changed.
func TestFormWritesTheConfig(t *testing.T) {
	m := newModel(t)
	send(m, "a")
	if m.mode != modeForm {
		t.Fatal("pressing a did not open the form")
	}
	m.form.set(fName, "newbox")
	m.form.set(fAddr, "10.5.5.5")
	m.form.set(fGroup, "staging")
	send(m, "ctrl+s")

	if m.form != nil {
		t.Fatalf("the form stayed open: %v", m.problem)
	}
	if _, ok := m.inv.Host("newbox"); !ok {
		t.Fatal("the host was not created")
	}
	b, err := os.ReadFile(m.inv.Config.Root.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Host newbox") || !strings.Contains(string(b), "#tram-group: staging") {
		t.Errorf("the stanza is not in the file:\n%s", b)
	}
}

// TestJumpPickerBlocksLoops checks that a station which would form a ProxyJump
// loop is shown with the reason rather than silently offered.
func TestJumpPickerBlocksLoops(t *testing.T) {
	m := newModel(t)
	items := m.jumpChoices("bastion")
	var blocked, offered int
	for _, c := range items {
		if c.blocked != "" {
			blocked++
			continue
		}
		offered++
	}
	if blocked == 0 {
		t.Fatalf("no station was blocked; web1 jumps through bastion so it would loop: %+v", items)
	}
	for _, c := range items {
		if c.value == "web1" && c.blocked == "" {
			t.Error("web1 was offered as a jump station for bastion, which would loop")
		}
	}
}

// TestHelpScreenRenders guards the key table against a panic when the window is
// small.
func TestHelpScreenRenders(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	send(m, "?")
	if !strings.Contains(m.View(), "connect") {
		t.Error("the help screen does not describe enter")
	}
	send(m, "q")
	if m.mode != modeNormal {
		t.Error("a key did not close the help screen")
	}
}
