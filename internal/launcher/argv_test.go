package launcher

import (
	"strings"
	"testing"

	"github.com/hieuny/tram/internal/model"
)

// TestArgvShape covers what tram hands to ssh. Only the host name goes on the
// command line, because ssh reads the same configuration tram does and
// duplicating any of it is a way for the two to disagree.
func TestArgvShape(t *testing.T) {
	got := strings.Join(Request{Host: "web1"}.Argv(), " ")
	if got != "ssh web1" {
		t.Errorf("a plain connection is %q", got)
	}

	got = strings.Join(Request{Host: "web1", ConfigPath: "/tmp/c", ConnectTimeout: 9}.Argv(), " ")
	for _, want := range []string{"-F /tmp/c", "ConnectTimeout=9", "web1"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing from %q", want, got)
		}
	}

	// A command runs with a terminal so that anything interactive on the far
	// side behaves, and the command comes after the host.
	got = strings.Join(Request{Host: "web1", Command: []string{"uptime"}, ForceTTY: true}.Argv(), " ")
	if got != "ssh -t web1 uptime" {
		t.Errorf("a remote command is %q", got)
	}
}

// TestVerboseIsPassedThrough covers the flag that answers "why is this taking
// so long": ssh says nothing at all while it waits, and -v makes it narrate.
func TestVerboseIsPassedThrough(t *testing.T) {
	cases := map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 9: 3}
	for asked, want := range cases {
		argv := Request{Host: "web1", Verbose: asked}.Argv()
		got := 0
		for _, a := range argv {
			if a == "-v" {
				got++
			}
		}
		if got != want {
			t.Errorf("asking for %d gave %d -v flags: %v", asked, got, argv)
		}
	}
}

// TestJumpPreflightRefusesALoop is the check that has to happen before ssh is
// started at all: given a circular ProxyJump ssh does not fail, it hangs, so
// there is no error to report afterwards.
func TestJumpPreflightRefusesALoop(t *testing.T) {
	if why := JumpPreflight(chainWithCycle()); why == "" {
		t.Error("a loop was allowed through")
	} else if !strings.Contains(why, "hang") {
		t.Errorf("the reason does not say what would happen: %q", why)
	}
}

func chainWithCycle() model.JumpChain {
	return model.JumpChain{Cycle: []string{"a", "b", "a"}}
}
