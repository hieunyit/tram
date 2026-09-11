package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/model"
)

// TestMeasureFillsTheColumns is the contract behind the design's LATENCY column
// and its RTT, LOAD and SYSTEM tiles: every one of them is a measurement, and
// none of them shows anything until something has been measured.
func TestMeasureFillsTheColumns(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	before := m.View()
	if !strings.Contains(before, "LATENCY") {
		t.Fatal("the table has no latency column")
	}
	if strings.Contains(before, " ms") {
		t.Errorf("a latency is shown before anything was measured:\n%s", before)
	}
	if !strings.Contains(before, "press p to measure this host") {
		t.Errorf("the details pane does not say how to fill itself in:\n%s", before)
	}

	// P measures everything shown. The command runs off the main loop, so the
	// test drains it the way the program's own loop would.
	_, cmd := m.Update(key("P"))
	if cmd == nil {
		t.Fatal("P started no measurement")
	}
	drain(m, cmd)

	after := m.View()
	if !strings.Contains(after, "3 ms") {
		t.Errorf("the latency column is still empty after a sweep:\n%s", after)
	}
	for _, want := range []string{"RTT", "LOAD", "SYSTEM", "Linux 6.1", "0.10"} {
		if !strings.Contains(after, want) {
			t.Errorf("the details pane does not show %q after a sweep:\n%s", want, after)
		}
	}
	if !strings.Contains(after, "3 up") {
		t.Errorf("the fleet health block was not updated:\n%s", after)
	}
}

// TestMeasurementsSurviveARestart checks that what was measured is written down
// rather than held in memory, which is what makes the reachability sparkline
// mean anything on the second run.
func TestMeasurementsSurviveARestart(t *testing.T) {
	m := newModel(t)
	_, cmd := m.Update(key("P"))
	drain(m, cmd)

	// A second interface over the same state, as a later run of tram would be.
	again := New(m.inv, nullRunner{}, false)
	again.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	if f := again.fact("web1"); !f.Measured() || f.Millis == 0 {
		t.Fatalf("the measurement did not survive: %+v", f)
	}
	if !strings.Contains(again.View(), " ms") {
		t.Error("the latency column is empty in a fresh interface")
	}
}

// TestSortingChangesTheOrder covers the sortable columns the design marks with
// an arrow.
func TestSortingChangesTheOrder(t *testing.T) {
	m := newModel(t)
	if got := model.Names(m.filtered); got[0] != "bastion" {
		t.Fatalf("the table does not open sorted by alias: %v", got)
	}

	send(m, "S") // reverse
	if got := model.Names(m.filtered); got[0] != "web1" {
		t.Errorf("reversing the sort gave %v", got)
	}
	if !strings.Contains(m.View(), "ALIAS "+m.gl.down) {
		t.Error("the heading does not show which way the column is sorted")
	}

	send(m, "S", "s") // back to ascending, then sort by user@host
	if m.sortKey != sortHost {
		t.Fatalf("s moved the sort to %v", m.sortKey)
	}
	if got := model.Names(m.filtered); got[0] != "web1" {
		// 10.0.0.1 sorts before 192.168.1.5 and b.example.com as text.
		t.Errorf("sorting by address gave %v", got)
	}
}

// TestTabsSwitchTheTable checks the three views in the design's tab strip.
func TestTabsSwitchTheTable(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	send(m, "2") // sessions
	if len(m.filtered) != 0 {
		t.Errorf("sessions lists %d hosts before anything was connected to", len(m.filtered))
	}
	if !strings.Contains(m.View(), "no sessions yet") {
		t.Errorf("the empty sessions tab does not explain itself:\n%s", m.View())
	}

	if err := m.inv.Store.Touch("web1"); err != nil {
		t.Fatal(err)
	}
	m.inv.Refresh()
	m.reload()
	if got := model.Names(m.filtered); len(got) != 1 || got[0] != "web1" {
		t.Errorf("sessions lists %v after connecting to web1", got)
	}

	send(m, "3") // keys
	if !strings.Contains(m.View(), "no keys yet") {
		t.Errorf("the empty keys tab does not explain itself:\n%s", m.View())
	}
	if err := m.inv.Store.PutAccount(model.Account{Name: "deploy", User: "deploy", Auth: model.AuthAgent}); err != nil {
		t.Fatal(err)
	}
	m.reload()
	out := m.View()
	for _, want := range []string{"deploy", "IDENTITY", "agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("the keys tab does not show %q:\n%s", want, out)
		}
	}

	send(m, "1")
	if m.tab != tabHosts {
		t.Error("1 did not go back to the hosts")
	}
}

// TestTagsAreShownAndSearchable covers the design's TAGS column end to end: the
// label is written into ssh_config, listed in the table, and found by a search.
func TestTagsAreShownAndSearchable(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	send(m, "e") // edit the first host, which is bastion
	if m.mode != modeForm {
		t.Fatal("e did not open the form")
	}
	m.form.set(fTags, "edge, gpu")
	send(m, "ctrl+s")
	if m.form != nil {
		t.Fatalf("the form stayed open: %v", m.problem)
	}

	h, ok := m.inv.Host("bastion")
	if !ok {
		t.Fatal("the host went missing")
	}
	if len(h.Tags) != 2 || h.Tags[0] != "edge" || h.Tags[1] != "gpu" {
		t.Fatalf("tags = %v, want edge and gpu", h.Tags)
	}
	if !strings.Contains(m.View(), "edge gpu") {
		t.Errorf("the tags column does not show them:\n%s", m.View())
	}

	send(m, "/", "#", "g", "p", "u")
	if got := model.Names(m.filtered); len(got) != 1 || got[0] != "bastion" {
		t.Errorf("a #tag search gave %v", got)
	}
}

// downRunner reports a fleet where the jump station is refusing connections and
// everything behind it times out, which is the case the diagnosis exists for.
type downRunner struct{ nullRunner }

func (downRunner) Measure(hosts []model.Host) []Measurement {
	out := make([]Measurement, len(hosts))
	for i, h := range hosts {
		switch h.Name {
		case "bastion":
			out[i] = Measurement{Host: h.Name, Class: "REFUSED", Millis: 8,
				Detail:  "ssh: connect to host b.example.com port 22: Connection refused",
				Explain: "the port answered and refused; sshd is probably not running"}
		case "web1":
			out[i] = Measurement{Host: h.Name, Class: "JUMP", Millis: 9,
				Detail:  "ssh: connect to host b.example.com port 22: Connection refused",
				Explain: "a jump station on the way failed"}
		default:
			out[i] = Measurement{Host: h.Name, Class: "OK", OK: true, Millis: 4, OS: "Linux 6.1"}
		}
	}
	return out
}

// TestDiagnosisSaysWhatWentWrong is what replaced the reachability chart: a
// chart of two measurements said nothing, and this says where to go and look.
func TestDiagnosisSaysWhatWentWrong(t *testing.T) {
	m := newModel(t)
	m.Runner = downRunner{}
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	_, cmd := m.Update(key("P"))
	drain(m, cmd)

	// The cursor opens on bastion, the station that refused.
	out := m.View()
	// The explanation and the line ssh wrote are both wrapped to the pane, so
	// the test looks for the parts that survive a line break.
	for _, want := range []string{
		"DIAGNOSIS",
		"refused",
		"the port answered and refused",
		"ssh: connect to host b.example.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the diagnosis does not say %q:\n%s", want, out)
		}
	}

	// web1 routes through it, so its own pane blames the station rather than
	// the destination, without probing anything again.
	send(m, "down", "down")
	if h, _ := m.current(); h.Name != "web1" {
		t.Fatalf("the cursor is on %q", h.Name)
	}
	out = m.View()
	if !strings.Contains(out, "route") {
		t.Fatalf("the diagnosis does not draw the route:\n%s", out)
	}
	if !strings.Contains(out, "bastion "+m.gl.cross) {
		t.Errorf("the route does not mark the station that is down:\n%s", out)
	}
	if !strings.Contains(out, "press D to walk it") {
		t.Errorf("the diagnosis does not offer the doctor:\n%s", out)
	}
}

// TestUsedByWarnsAboutJumpStations covers the line that replaced the recent
// list: what breaks if this host does.
func TestUsedByWarnsAboutJumpStations(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	out := m.View()
	if !strings.Contains(out, "used by") || !strings.Contains(out, "web1") {
		t.Errorf("bastion's pane does not say that web1 routes through it:\n%s", out)
	}

	// A host nothing routes through does not carry the line at all.
	send(m, "down")
	if h, _ := m.current(); h.Name != "laptop" {
		t.Fatalf("the cursor is on %q", h.Name)
	}
	if strings.Contains(m.View(), "used by") {
		t.Error("a host nothing depends on is still drawn as a jump station")
	}
}

// TestExecShowsItsOutput is the bug a real session found: running a command on
// one host put the answer behind a keystroke nobody knew to press.
func TestExecShowsItsOutput(t *testing.T) {
	m := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	send(m, "x")
	if m.mode != modeForm {
		t.Fatal("x opened no form")
	}
	m.form.set(fCommand, "uptime")
	send(m, "ctrl+s")

	if m.screen != screenResult {
		t.Fatalf("the result screen did not open; screen is %v", m.screen)
	}
	out := m.View()
	if !strings.Contains(strings.ToLower(out), "exec: uptime") {
		t.Errorf("the result screen does not name the command:\n%s", out)
	}
	// nullRunner echoes the command back as the body, and the heading above is
	// upper case, so a lower case "uptime" on screen can only be the output
	// itself, drawn with nothing pressed.
	if !strings.Contains(out, "exit 0") {
		t.Errorf("the result screen does not show the status:\n%s", out)
	}
	if !strings.Contains(out, "uptime") {
		t.Errorf("the output is still collapsed behind a keystroke:\n%s", out)
	}
}

// TestFleetCommandsRunOffTheMainLoop checks that the interface hands the work
// to a command rather than doing it inside Update, where a hung ssh would
// freeze the whole screen.
func TestFleetCommandsRunOffTheMainLoop(t *testing.T) {
	m := newModel(t)
	_, cmd := m.Update(key("D"))
	if cmd == nil {
		t.Fatal("doctor produced no command")
	}
	if m.screen == screenResult {
		t.Error("the result screen opened before the work had run")
	}
	if m.running == "" {
		t.Error("the bar does not say what the interface is waiting for")
	}
	drain(m, cmd)
	if m.screen != screenResult {
		t.Error("the results never arrived")
	}
	if m.running != "" {
		t.Error("the bar still claims something is running")
	}
}

// TestTheTerminalGetsTheMouseBackWhileTyping is the other bug a real session
// found: with the pointer captured, the terminal's own paste stopped working in
// the one place it is needed most.
func TestTheTerminalGetsTheMouseBackWhileTyping(t *testing.T) {
	m := wide(t)
	if !m.mouseOn {
		t.Fatal("the interface did not take the mouse to begin with")
	}

	_, cmd := m.Update(key("a"))
	if m.mouseOn {
		t.Error("the form kept the mouse, so the terminal cannot paste into it")
	}
	if got := msgTypes(cmd); !strings.Contains(got, "disableMouse") {
		t.Errorf("opening the form sent %s, which does not hand the mouse back", got)
	}

	send(m, "esc")
	if !m.mouseOn {
		t.Error("closing the form did not take the mouse back")
	}
	send(m, "/")
	if m.mouseOn {
		t.Error("the search box kept the mouse")
	}
}

// msgTypes names what a command produces, so that a test can assert on the
// messages bubbletea keeps to itself.
func msgTypes(cmd tea.Cmd) string {
	if cmd == nil {
		return "nothing"
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var all []string
		for _, c := range batch {
			all = append(all, msgTypes(c))
		}
		return strings.Join(all, ", ")
	}
	return fmt.Sprintf("%T", msg)
}
