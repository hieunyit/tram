package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hieuny/tram/internal/model"
)

// TestGroupPaneListsTheTree covers the sidebar's shape: the three headings
// people actually navigate by, then the hierarchy with counts.
func TestGroupPaneListsTheTree(t *testing.T) {
	m := newModel(t)
	labels := groupLabels(m)

	if len(labels) == 0 || !strings.Contains(labels[0], "All") {
		t.Fatalf("the first row is not All: %v", labels)
	}
	if !containsSub(labels, "prod") {
		t.Errorf("the tree has no prod: %v", labels)
	}
	// web is nested under prod, so it is not shown until prod is opened.
	if containsSub(labels, "web") {
		t.Errorf("a closed branch is showing its children: %v", labels)
	}

	out := m.View()
	if !strings.Contains(out, "GROUPS") {
		t.Errorf("the pane has no heading:\n%s", out)
	}
	if !strings.Contains(out, "All") {
		t.Errorf("the pane is not drawn:\n%s", out)
	}
}

// TestOpeningABranchRevealsItsChildren checks the disclosure, which is the only
// way to reach a nested group.
func TestOpeningABranchRevealsItsChildren(t *testing.T) {
	m := newModel(t)
	send(m, "tab") // move to the group pane
	if m.focus != focusGroups {
		t.Fatal("tab did not move to the group pane")
	}
	selectGroup(t, m, "prod")
	send(m, "enter")

	if !containsSub(groupLabels(m), "web") {
		t.Errorf("opening prod did not reveal web: %v", groupLabels(m))
	}
	send(m, "enter") // close it again
	if containsSub(groupLabels(m), "web") {
		t.Errorf("closing prod left its children showing: %v", groupLabels(m))
	}
}

// TestChoosingAGroupFiltersTheHosts is what the pane is for.
func TestChoosingAGroupFiltersTheHosts(t *testing.T) {
	m := newModel(t)
	if len(m.filtered) != 3 {
		t.Fatalf("All shows %v", model.Names(m.filtered))
	}

	send(m, "tab")
	selectGroup(t, m, "prod")
	// prod holds bastion and web1; laptop has no group at all.
	if got := model.Names(m.filtered); len(got) != 2 {
		t.Errorf("prod shows %v, want the two prod hosts", got)
	}

	selectGroup(t, m, "(no group)")
	if got := model.Names(m.filtered); len(got) != 1 || got[0] != "laptop" {
		t.Errorf("the ungrouped row shows %v, want laptop", got)
	}

	selectGroup(t, m, "All")
	if len(m.filtered) != 3 {
		t.Errorf("going back to All shows %v", model.Names(m.filtered))
	}
}

// TestFavouritesAndRecentAppearOnlyWhenTheyHaveSomething keeps the pane from
// carrying two rows that are always empty.
func TestFavouritesAndRecentAppearOnlyWhenTheyHaveSomething(t *testing.T) {
	m := newModel(t)
	if containsSub(groupLabels(m), "Favorites") {
		t.Errorf("Favorites is listed with nothing pinned: %v", groupLabels(m))
	}

	send(m, "*") // pin the host under the cursor
	if !containsSub(groupLabels(m), "Favorites") {
		t.Errorf("pinning a host did not add Favorites: %v", groupLabels(m))
	}

	send(m, "tab")
	selectGroup(t, m, "Favorites")
	if got := model.Names(m.filtered); len(got) != 1 {
		t.Errorf("Favorites shows %v, want the one pinned host", got)
	}
}

// TestSearchAndGroupNarrowTogether checks the two filters compose rather than
// one replacing the other.
func TestSearchAndGroupNarrowTogether(t *testing.T) {
	m := newModel(t)
	send(m, "tab")
	selectGroup(t, m, "prod")
	send(m, "right") // back to the hosts
	send(m, "/", "w", "e", "b")

	if got := model.Names(m.filtered); len(got) != 1 || got[0] != "web1" {
		t.Errorf("prod plus a search for web gave %v", got)
	}
}

// TestGroupPaneStepsAsideOnANarrowWindow checks the host list wins the space
// when there is not enough of it.
func TestGroupPaneStepsAsideOnANarrowWindow(t *testing.T) {
	m := newModel(t)
	if m.sidebarWidth() == 0 {
		t.Fatal("the pane is missing at a normal width")
	}

	m.Update(windowSize(60, 30))
	if m.sidebarWidth() != 0 {
		t.Error("the pane still takes space on a narrow window")
	}
	if strings.Contains(m.View(), "GROUPS") {
		t.Error("the pane is still drawn on a narrow window")
	}

	m.Update(windowSize(100, 30))
	send(m, "g")
	if m.sidebarWidth() != 0 {
		t.Error("g did not hide the pane")
	}
	if m.focus != focusHosts {
		t.Error("hiding the pane left the keyboard in it")
	}
}

func groupLabels(m *Model) []string {
	out := make([]string, 0, len(m.groupRows))
	for _, r := range m.groupRows {
		out = append(out, r.label)
	}
	return out
}

func containsSub(ss []string, want string) bool {
	for _, s := range ss {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func selectGroup(t *testing.T, m *Model, label string) {
	t.Helper()
	for i, r := range m.groupRows {
		if strings.Contains(r.label, label) {
			m.groupCursor = i
			m.cursor, m.offset = 0, 0
			m.applyFilter()
			return
		}
	}
	t.Fatalf("%q is not in the group pane: %v", label, groupLabels(m))
}

func windowSize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }
