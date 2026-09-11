package tui

import (
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
