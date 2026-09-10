package cmd

import (
	"strings"
	"testing"

	"github.com/hieuny/tram/internal/model"
)

// TestRouteLines covers the line printed before the terminal is handed over.
//
// It exists because of a real report: pressing enter on a host behind a jump
// station left a blank screen for minutes. ssh sets no connect timeout of its
// own and says nothing while it waits, so there was no way to tell a hung
// program from a station that was not answering.
func TestRouteLines(t *testing.T) {
	direct := routeLines(model.JumpChain{}, "web1")
	if len(direct) != 1 {
		t.Fatalf("a direct connection printed %d lines, want one: %v", len(direct), direct)
	}
	if !strings.Contains(direct[0], "web1") {
		t.Errorf("the line does not name the host: %q", direct[0])
	}

	chain := model.JumpChain{Hops: []model.Hop{{Spec: "bastion"}, {Spec: "inner"}}}
	routed := routeLines(chain, "vpb-fs17")
	if len(routed) != 2 {
		t.Fatalf("a routed connection printed %d lines, want two: %v", len(routed), routed)
	}
	for _, want := range []string{"bastion", "inner", "vpb-fs17"} {
		if !strings.Contains(routed[0], want) {
			t.Errorf("the route does not name %q: %q", want, routed[0])
		}
	}
	// The order has to be the order ssh dials, or it points at the wrong
	// machine when it is read in a hurry.
	if strings.Index(routed[0], "bastion") > strings.Index(routed[0], "vpb-fs17") {
		t.Errorf("the route is back to front: %q", routed[0])
	}
	if !strings.Contains(routed[1], "tram doctor vpb-fs17") {
		t.Errorf("the second line does not say what to run: %q", routed[1])
	}
}
