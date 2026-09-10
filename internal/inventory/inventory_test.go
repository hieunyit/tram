package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/sshconf"
)

const fixture = `# personal notes at the top
Host *
    ServerAliveInterval 60

# the production bastion
Host bastion
    #tram-group: prod
    HostName bastion.example.com
    User jump
    Port 2222

Host web1
    #tram-group: prod/web
    #tram-desc: Primary web server
    HostName 10.0.0.1
    User deploy
    ProxyJump bastion
    IdentityFile ~/.ssh/id_ed25519
    # a note that belongs to web1
    ForwardAgent yes

Host db1
    #tram-group: prod/db
    HostName 10.0.1.1
    ProxyJump bastion
`

// setup builds an isolated ssh directory and state directory so that a test
// never reads or writes the machine's real configuration.
func setup(t *testing.T, content string) *Inventory {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sshDir, "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRAM_HOME", filepath.Join(home, "state"))
	t.Setenv("TRAM_SSH_DIR", sshDir)
	t.Setenv("TRAM_SSH_CONFIG", path)

	inv, err := Load(Options{ConfigPath: path, SSHDir: sshDir, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func read(t *testing.T, inv *Inventory) string {
	t.Helper()
	b, err := os.ReadFile(inv.Config.Root.Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// applier returns a helper that takes a change straight from one of the edit
// methods, which return two values, and applies it.
func applier(t *testing.T) func(*Change, error) *Change {
	return func(ch *Change, err error) *Change {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.Apply(); err != nil {
			t.Fatal(err)
		}
		return ch
	}
}

// TestGroupsComeFromTheConfigFile is the check behind the promise that deleting
// tram's state directory costs you Recent, Favorites and account links, and
// nothing else. Groups have to survive that, so they live in ssh_config as
// comments rather than in tram's own store.
func TestGroupsComeFromTheConfigFile(t *testing.T) {
	inv := setup(t, fixture)
	h, ok := inv.Host("web1")
	if !ok {
		t.Fatal("web1 not found")
	}
	if h.Group != "prod/web" {
		t.Errorf("group = %q, want prod/web", h.Group)
	}
	if h.Desc != "Primary web server" {
		t.Errorf("desc = %q", h.Desc)
	}

	// Wiping the state directory must not change the host list.
	if err := os.RemoveAll(os.Getenv("TRAM_HOME")); err != nil {
		t.Fatal(err)
	}
	again, err := Load(Options{ConfigPath: inv.Config.Root.Path, SSHDir: filepath.Dir(inv.Config.Root.Path)})
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := again.Host("web1")
	if h2.Group != "prod/web" || len(again.Hosts()) != len(inv.Hosts()) {
		t.Errorf("deleting tram's state changed the host list: %d hosts, group %q", len(again.Hosts()), h2.Group)
	}
}

func TestNestedGroupFilterIncludesChildren(t *testing.T) {
	inv := setup(t, fixture)
	got := model.Names(inv.Select(Filter{Group: "prod"}))
	want := []string{"bastion", "db1", "web1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("group prod selected %v, want %v", got, want)
	}
	if got := model.Names(inv.Select(Filter{Group: "prod/db"})); len(got) != 1 || got[0] != "db1" {
		t.Errorf("group prod/db selected %v", got)
	}
}

// TestEditLeavesEverythingElseAlone is the golden-file check for an edit: only
// the line asked for may move.
func TestEditLeavesEverythingElseAlone(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	apply(inv.Edit("web1", Spec{Name: "web1", Port: Str("2200")}, false))

	got := read(t, inv)
	want := strings.Replace(fixture,
		"    # a note that belongs to web1\n    ForwardAgent yes\n",
		"    # a note that belongs to web1\n    ForwardAgent yes\n    Port 2200\n", 1)
	if got != want {
		t.Errorf("edit changed more than the port:\n%s", sshconf.UnifiedDiff("config", []byte(want), []byte(got)))
	}
}

// TestRenameCascadesThroughProxyJump covers the operation most likely to break
// a configuration: every reference has to move with the name.
func TestRenameCascadesThroughProxyJump(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	ch := apply(inv.Rename("bastion", "jump1", false))
	if len(ch.Summary) != 3 {
		t.Errorf("rename touched %d stanzas, want 3 (the host and its two dependents): %v", len(ch.Summary), ch.Summary)
	}

	got := read(t, inv)
	if strings.Contains(got, "ProxyJump bastion") {
		t.Errorf("a ProxyJump still points at the old name:\n%s", got)
	}
	if strings.Count(got, "ProxyJump jump1") != 2 {
		t.Errorf("expected two updated ProxyJump lines:\n%s", got)
	}
	if !strings.Contains(got, "# the production bastion") {
		t.Error("the comment introducing the stanza was lost")
	}
}

// TestRenameKeepsUserAndPortInAJumpSpec checks that only the name part of a
// ProxyJump token changes.
func TestRenameKeepsUserAndPortInAJumpSpec(t *testing.T) {
	src := "Host b\n    HostName 1.1.1.1\n\nHost x\n    ProxyJump ops@b:2022\n"
	inv := setup(t, src)
	apply := applier(t)
	apply(inv.Rename("b", "bastion", false))
	if got := read(t, inv); !strings.Contains(got, "ProxyJump ops@bastion:2022") {
		t.Errorf("rename mangled the jump spec:\n%s", got)
	}
}

// TestRemoveWarnsAboutDependents checks that the hosts a deletion breaks are
// named before anything is written.
func TestRemoveWarnsAboutDependents(t *testing.T) {
	inv := setup(t, fixture)
	ch, err := inv.Remove("bastion", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Warnings) == 0 {
		t.Fatal("removing a jump station raised no warning")
	}
	w := ch.Warnings[0]
	for _, name := range []string{"db1", "web1"} {
		if !strings.Contains(w, name) {
			t.Errorf("warning does not name %s: %s", name, w)
		}
	}
	ch.Discard()
	if read(t, inv) != fixture {
		t.Error("discarding a change still wrote to the file")
	}
}

func TestRemoveTakesTheStanzasCommentsWithIt(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	apply(inv.Remove("web1", false))
	got := read(t, inv)
	for _, gone := range []string{"a note that belongs to web1", "Primary web server", "10.0.0.1", "ForwardAgent"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived the deletion:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "personal notes at the top") {
		t.Error("the file's own header comment was removed")
	}
}

// TestCloneCopiesUnknownDirectives checks that a clone is a real copy: the
// directives tram does not model come along.
func TestCloneCopiesUnknownDirectives(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	apply(inv.Clone("web1", "web2", Spec{Name: "web2", HostName: Str("10.0.0.2")}))

	h, ok := inv.Host("web2")
	if !ok {
		t.Fatal("web2 was not created")
	}
	if h.HostName != "10.0.0.2" {
		t.Errorf("hostname = %q, the override should win", h.HostName)
	}
	if h.Group != "prod/web" || h.User != "deploy" || h.ProxyJump != "bastion" {
		t.Errorf("clone lost fields: %+v", h)
	}
	if got := h.Other["ForwardAgent"]; len(got) != 1 || got[0] != "yes" {
		t.Errorf("clone lost the unknown directive: %v", h.Other)
	}
}

// TestReadOnlyLockAfterInit covers the fourth design constraint: once tram has
// its own file, everything else is read-only unless forced.
func TestReadOnlyLockAfterInit(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	sshDir := filepath.Dir(inv.Config.Root.Path)

	res, err := inv.Init(sshDir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Change == nil {
		t.Fatalf("init did not create a managed file and an Include: %+v", res)
	}
	if err := res.Change.Apply(); err != nil {
		t.Fatal(err)
	}

	root := read(t, inv)
	if !strings.HasPrefix(root, "# Added by tram") || !strings.Contains(root, "Include config.d/tram.conf") {
		t.Errorf("the Include is not at the top of the file:\n%s", root)
	}
	// An absolute Include here would be silently ignored by ssh on Windows.
	if strings.Contains(root, ":\\") || strings.Contains(root, ":/") {
		t.Errorf("init wrote an absolute Include path:\n%s", root)
	}

	fresh, err := Load(Options{ConfigPath: inv.Config.Root.Path, SSHDir: sshDir})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Managed == nil {
		t.Fatal("the managed file was not picked up on reload")
	}
	if h, _ := fresh.Host("web1"); !h.ReadOnly {
		t.Error("a host outside the managed file is still writable after init")
	}
	if _, err := fresh.Edit("web1", Spec{Name: "web1", Port: Str("2200")}, false); err == nil {
		t.Error("editing a read-only host was allowed without --force")
	}
	if ch, err := fresh.Edit("web1", Spec{Name: "web1", Port: Str("2200")}, true); err != nil {
		t.Errorf("--force did not unlock the host: %v", err)
	} else {
		ch.Discard()
	}

	// A new host must land in the managed file, not in the root config.
	before := read(t, fresh)
	apply(fresh.Add(Spec{Name: "newhost", HostName: Str("10.9.9.9")}))
	if read(t, fresh) != before {
		t.Error("add wrote to ssh_config even though a managed file exists")
	}
	managed, _ := os.ReadFile(res.ManagedPath)
	if !strings.Contains(string(managed), "Host newhost") {
		t.Errorf("the new host is not in the managed file:\n%s", managed)
	}
}

// TestAccountMaterializesOnceAndDetectsDrift covers the identity model: an
// account writes into the stanza, and a hand edit afterwards is noticed rather
// than reverted.
func TestAccountMaterializesOnceAndDetectsDrift(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	if err := inv.Store.PutAccount(model.Account{
		Name: "deploy-key", User: "ops", Auth: model.AuthKey, KeyPath: "~/.ssh/deploy",
	}); err != nil {
		t.Fatal(err)
	}

	apply(inv.Edit("db1", Spec{Name: "db1", Account: Str("deploy-key")}, false))
	h, _ := inv.Host("db1")
	if h.User != "ops" {
		t.Errorf("the account did not write User: %q", h.User)
	}
	if len(h.IdentityFiles) != 1 || h.IdentityFiles[0] != "~/.ssh/deploy" {
		t.Errorf("the account did not write IdentityFile: %v", h.IdentityFiles)
	}
	if h.Drift {
		t.Error("a freshly applied account should not read as drifted")
	}

	// A hand edit makes the host drift, and apply must leave it alone.
	apply(inv.Edit("db1", Spec{Name: "db1", User: Str("someone-else")}, false))
	if h, _ := inv.Host("db1"); !h.Drift {
		t.Fatal("editing User by hand did not register as drift")
	}
	ch, plans, err := inv.ApplyAccount("deploy-key", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Action != "skip-drift" {
		t.Fatalf("apply did not skip the drifted host: %+v", plans)
	}
	if !ch.Empty() {
		t.Errorf("apply would still have written:\n%s", ch.Diff())
	}
	if h, _ := inv.Host("db1"); h.User != "someone-else" {
		t.Errorf("the hand edit was reverted: %q", h.User)
	}
}

// TestUnlinkingKeepsTheStanza checks that removing an account link changes only
// the label, so the host still connects exactly as before.
func TestUnlinkingKeepsTheStanza(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	if err := inv.Store.PutAccount(model.Account{Name: "a", User: "ops", Auth: model.AuthAgent}); err != nil {
		t.Fatal(err)
	}
	apply(inv.Edit("db1", Spec{Name: "db1", Account: Str("a")}, false))
	before := read(t, inv)

	apply(inv.Edit("db1", Spec{Name: "db1", Account: Str("")}, false))
	if got := read(t, inv); got != before {
		t.Errorf("unlinking changed the stanza:\n%s", sshconf.UnifiedDiff("config", []byte(before), []byte(got)))
	}
	if h, _ := inv.Host("db1"); h.Account != "" {
		t.Errorf("the link survived: %q", h.Account)
	}
}

// TestExactMatchBeatsFuzzy checks the rule that keeps tram off the wrong
// database: db-prod and db-prod-replica are different machines.
func TestExactMatchBeatsFuzzy(t *testing.T) {
	inv := setup(t, "Host db-prod\n    HostName 1.1.1.1\n\nHost db-prod-replica\n    HostName 2.2.2.2\n")
	if got := model.Names(inv.Resolve("db-prod")); len(got) != 1 || got[0] != "db-prod" {
		t.Errorf("an exact name did not win outright: %v", got)
	}
	if got := inv.Resolve("db-pro"); len(got) != 2 {
		t.Errorf("an ambiguous prefix returned %d hosts, it must return both", len(got))
	}
	if got := inv.Resolve("nothing-like-this"); len(got) != 0 {
		t.Errorf("a name that matches nothing returned %v", got)
	}
}

// TestJumpLoopIsDetected covers the failure mode that ssh does not report: a
// circular ProxyJump makes it hang rather than fail.
func TestJumpLoopIsDetected(t *testing.T) {
	inv := setup(t, "Host a\n    ProxyJump b\n\nHost b\n    ProxyJump a\n")
	chain := inv.Chain("a")
	if !chain.Broken() {
		t.Fatal("the loop was not detected")
	}
	if len(chain.Cycle) < 2 {
		t.Errorf("the cycle does not name its stations: %v", chain.Cycle)
	}
	if !model.WouldCycle("a", "b", inv.Lookup) {
		t.Error("WouldCycle missed a loop the picker has to grey out")
	}
	if model.WouldCycle("a", "a", inv.Lookup) != true {
		t.Error("a host jumping through itself is a loop")
	}
}

func TestAddRefusesNamesSSHWouldMisread(t *testing.T) {
	inv := setup(t, fixture)
	for _, bad := range []string{"", "two words", "wild*card", `quo"te`} {
		if _, err := inv.Add(Spec{Name: bad}); err == nil {
			t.Errorf("adding a host named %q was allowed", bad)
		}
	}
	if _, err := inv.Add(Spec{Name: "web1"}); err == nil {
		t.Error("adding a duplicate name was allowed")
	}
}

// TestPortTwentyTwoIsNotWritten checks that tram does not add lines that only
// restate ssh's defaults.
func TestPortTwentyTwoIsNotWritten(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	apply(inv.Add(Spec{Name: "plain", HostName: Str("1.1.1.1"), Port: Str("22")}))
	if strings.Contains(read(t, inv), "Port 22\n") {
		t.Errorf("tram wrote the default port:\n%s", read(t, inv))
	}
}

// legacyFixture is a configuration written by the tool tram replaces. Two
// things about it matter: the markers use the old spelling, and they sit above
// the Host line rather than inside the stanza.
const legacyFixture = `#sshfleet:group=giavang/web-chat
#sshfleet:desc=web chat gia vang
Host chat-gia-vang
    HostName 45.76.202.178
    User root
    Port 22
`

// TestLegacyMarkersAreRead is the difference between a first run that shows
// your groups and one that silently shows none.
//
// tram shares no code with the tool it replaces, but the people most likely to
// run it are the ones whose ssh_config already carries its markers.
func TestLegacyMarkersAreRead(t *testing.T) {
	inv := setup(t, legacyFixture)
	h, ok := inv.Host("chat-gia-vang")
	if !ok {
		t.Fatal("host not found")
	}
	if h.Group != "giavang/web-chat" {
		t.Errorf("group = %q; a marker above the Host line was not read", h.Group)
	}
	if h.Desc != "web chat gia vang" {
		t.Errorf("desc = %q", h.Desc)
	}
	if got := model.Names(inv.Select(Filter{Group: "giavang"})); len(got) != 1 {
		t.Errorf("filtering by the inherited group found %v", got)
	}
}

// TestMarkerAboveTheHostLineIsRead covers tram's own spelling in the same
// place, since a person editing by hand may well put it there.
func TestMarkerAboveTheHostLineIsRead(t *testing.T) {
	inv := setup(t, "#tram-group: prod/web\n#tram-desc: the web one\nHost w\n    HostName 1.1.1.1\n")
	h, _ := inv.Host("w")
	if h.Group != "prod/web" || h.Desc != "the web one" {
		t.Errorf("group %q desc %q", h.Group, h.Desc)
	}
}

// TestWritingReplacesLegacyMarkers checks that an edit migrates the old
// spelling instead of leaving a second copy above the new one, where the reader
// would take whichever it saw last.
func TestWritingReplacesLegacyMarkers(t *testing.T) {
	inv := setup(t, legacyFixture)
	apply := applier(t)
	apply(inv.Edit("chat-gia-vang", Spec{Name: "chat-gia-vang", Group: Str("giavang/web")}, false))

	got := read(t, inv)
	if strings.Contains(got, "sshfleet") {
		t.Errorf("the old marker was left behind:\n%s", got)
	}
	if strings.Count(got, groupMarker) != 1 {
		t.Errorf("expected exactly one group marker:\n%s", got)
	}

	h, _ := inv.Host("chat-gia-vang")
	if h.Group != "giavang/web" {
		t.Errorf("group = %q", h.Group)
	}
	// Changing the group must not take the description with it. The two share
	// one rewrite, and a field the caller said nothing about is one to leave.
	if h.Desc != "web chat gia vang" {
		t.Errorf("the description was lost by an edit that never mentioned it: %q", h.Desc)
	}
}

// TestEditingDescriptionKeepsTheGroup is the same rule the other way round.
func TestEditingDescriptionKeepsTheGroup(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	apply(inv.Edit("web1", Spec{Name: "web1", Desc: Str("changed")}, false))

	h, _ := inv.Host("web1")
	if h.Group != "prod/web" {
		t.Errorf("the group was lost: %q", h.Group)
	}
	if h.Desc != "changed" {
		t.Errorf("desc = %q", h.Desc)
	}
}

// TestClearingAMarkerStillWorks checks that an explicit empty value removes it,
// which is different from saying nothing about it.
func TestClearingAMarkerStillWorks(t *testing.T) {
	inv := setup(t, fixture)
	apply := applier(t)
	apply(inv.Edit("web1", Spec{Name: "web1", Group: Str("")}, false))

	h, _ := inv.Host("web1")
	if h.Group != "" {
		t.Errorf("an explicit empty group did not clear it: %q", h.Group)
	}
	if h.Desc != "Primary web server" {
		t.Errorf("clearing the group took the description: %q", h.Desc)
	}
}
