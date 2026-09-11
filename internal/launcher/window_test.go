package launcher

import (
	"strings"
	"testing"
)

// TestWindowsTerminalAsksForTheRightWindow is the bug a real session found.
//
// -w 0 means "the window most recently used", which is the right answer only
// when that window is the one tram is drawing in. Run from any other terminal
// on a machine that merely has Windows Terminal installed, it puts the tab in
// an application the user is not looking at, and the report reads as nothing
// having happened at all.
func TestWindowsTerminalAsksForTheRightWindow(t *testing.T) {
	t.Setenv("WT_SESSION", "some-guid")
	inside, err := WindowCommand(TermWindowsTerminal, `C:\tram.exe`, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(inside, " "); !strings.Contains(got, "-w 0") {
		t.Errorf("inside Windows Terminal the tab should join this window: %v", inside)
	}

	t.Setenv("WT_SESSION", "")
	outside, err := WindowCommand(TermWindowsTerminal, `C:\tram.exe`, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(outside, " "); !strings.Contains(got, "-w new") {
		t.Errorf("outside Windows Terminal the tab should get a window of its own: %v", outside)
	}

	// Either way the tab runs tram again with one short name, and never an ssh
	// command line: Windows Terminal treats semicolons and quotes in its
	// arguments as its own syntax.
	for _, argv := range [][]string{inside, outside} {
		last := argv[len(argv)-2:]
		if last[0] != `C:\tram.exe` || last[1] != "web1" {
			t.Errorf("the tab runs %v", last)
		}
	}
}

// TestSplitNeedsToBeInTheWindowItSplits covers the other half: a pane is beside
// something, and there is nothing to be beside from another terminal.
func TestSplitNeedsToBeInTheWindowItSplits(t *testing.T) {
	t.Setenv("WT_SESSION", "some-guid")
	argv, split, err := SplitCommand(TermWindowsTerminal, `C:\tram.exe`, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !split {
		t.Error("inside Windows Terminal a split should be a split")
	}
	if !strings.Contains(strings.Join(argv, " "), "split-pane") {
		t.Errorf("the command does not split: %v", argv)
	}

	t.Setenv("WT_SESSION", "")
	argv, split, err = SplitCommand(TermWindowsTerminal, `C:\tram.exe`, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if split {
		t.Error("a split was claimed from outside the window it would split")
	}
	if strings.Contains(strings.Join(argv, " "), "split-pane") {
		t.Errorf("the fallback still tries to split: %v", argv)
	}
}

// TestAnOverrideWinsOverAllOfIt keeps the escape hatch working: whatever is in
// window_command is used exactly as written.
func TestAnOverrideWinsOverAllOfIt(t *testing.T) {
	argv, err := WindowCommand(TermWindowsTerminal, `C:\tram.exe`, "web1",
		[]string{"myterm", "--run", "{{tram}} {{host}}"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"myterm", "--run", `C:\tram.exe web1`}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("the override produced %v, want %v", argv, want)
		}
	}
}
