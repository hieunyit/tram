package inventory

import (
	"os"
	"strings"
	"testing"
)

// TestTagsRoundTrip checks that a label written into ssh_config comes back out
// of it. Tags live in a comment, like the group and the description, so that a
// file carrying them is still a plain ssh_config and ssh ignores them.
func TestTagsRoundTrip(t *testing.T) {
	inv := setup(t, fixture)

	ch, err := inv.Edit("web1", Spec{Name: "web1", Tags: Str("gpu, nlp,, gpu")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Apply(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(inv.Config.Root.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "#tram-tags: gpu, nlp") {
		t.Fatalf("the comment is not in the file:\n%s", b)
	}
	if strings.Count(string(b), "#tram-tags:") != 1 {
		t.Errorf("the tags were written more than once:\n%s", b)
	}

	inv.Refresh()
	h, ok := inv.Host("web1")
	if !ok {
		t.Fatal("the host went missing")
	}
	if len(h.Tags) != 2 || h.Tags[0] != "gpu" || h.Tags[1] != "nlp" {
		t.Fatalf("tags = %v, want gpu and nlp with the duplicate dropped", h.Tags)
	}
	if h.Group != "prod/web" || h.Desc != "Primary web server" {
		t.Errorf("writing tags disturbed the other metadata: group %q, desc %q", h.Group, h.Desc)
	}
}

// TestEditingOneMarkerLeavesTheOthers is the failure the three comments share a
// rewrite: changing the group must not take the tags with it.
func TestEditingOneMarkerLeavesTheOthers(t *testing.T) {
	inv := setup(t, fixture)

	for _, s := range []Spec{
		{Name: "db1", Tags: Str("pg, primary")},
		{Name: "db1", Group: Str("prod/database")},
	} {
		ch, err := inv.Edit(s.Name, s, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.Apply(); err != nil {
			t.Fatal(err)
		}
		inv.Refresh()
	}

	h, _ := inv.Host("db1")
	if h.Group != "prod/database" {
		t.Errorf("group = %q", h.Group)
	}
	if len(h.Tags) != 2 {
		t.Errorf("the group edit lost the tags: %v", h.Tags)
	}
}

// TestCloneCarriesTheTags checks that a copy is a copy.
func TestCloneCarriesTheTags(t *testing.T) {
	inv := setup(t, fixture)

	ch, err := inv.Edit("web1", Spec{Name: "web1", Tags: Str("gpu")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Apply(); err != nil {
		t.Fatal(err)
	}
	inv.Refresh()

	ch, err = inv.Clone("web1", "web2", Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Apply(); err != nil {
		t.Fatal(err)
	}
	inv.Refresh()

	h, ok := inv.Host("web2")
	if !ok {
		t.Fatal("the clone was not created")
	}
	if len(h.Tags) != 1 || h.Tags[0] != "gpu" {
		t.Errorf("the clone has tags %v", h.Tags)
	}
}
