package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inventoryINI = `[webservers]
imp-web1 ansible_host=10.7.0.1
imp-web2 ansible_host=10.7.0.2

[webservers:vars]
ansible_user=deploy

[prod:children]
webservers
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestImportPreviewsBeforeWriting is the whole point of the import screen:
// reading a file shows what would happen and changes nothing, and a second key
// is what commits it.
func TestImportPreviewsBeforeWriting(t *testing.T) {
	m := newModel(t)
	before := len(m.hosts)
	configBefore, err := os.ReadFile(m.inv.Config.Root.Path)
	if err != nil {
		t.Fatal(err)
	}

	send(m, "I")
	if m.mode != modeForm {
		t.Fatal("pressing I did not open the import form")
	}
	m.form.set(fPath, writeTemp(t, "inventory.ini", inventoryINI))
	send(m, "ctrl+s")

	if m.form != nil {
		t.Fatalf("the form stayed open: %v", m.form.problem)
	}
	if m.screen != screenResult {
		t.Fatal("the preview screen did not open")
	}
	if m.pendingImport == nil {
		t.Fatal("no plan is being held")
	}
	if got := m.pendingImport.Writes(); got != 2 {
		t.Errorf("the plan would write %d hosts, want 2", got)
	}

	// Nothing may have been written yet.
	if len(m.hosts) != before {
		t.Errorf("the host list changed during a preview: %d then %d", before, len(m.hosts))
	}
	after, _ := os.ReadFile(m.inv.Config.Root.Path)
	if string(after) != string(configBefore) {
		t.Error("the preview wrote to ssh_config")
	}

	out := m.View()
	for _, want := range []string{"imp-web1", "imp-web2", "2 to add", "write 2 host(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview does not show %q:\n%s", want, out)
		}
	}

	send(m, "w")
	if m.pendingImport != nil {
		t.Error("the plan is still pending after writing")
	}
	if m.screen != screenList {
		t.Error("writing did not return to the list")
	}
	if _, ok := m.inv.Host("imp-web1"); !ok {
		t.Fatal("imp-web1 was not created")
	}
	h, _ := m.inv.Host("imp-web2")
	if h.User != "deploy" {
		t.Errorf("group vars were not applied: user = %q", h.User)
	}
	if h.Group != "prod/webservers" {
		t.Errorf("group = %q, want prod/webservers", h.Group)
	}
}

// TestImportCanBeCancelled checks that escaping the preview leaves the
// configuration exactly as it was.
func TestImportCanBeCancelled(t *testing.T) {
	m := newModel(t)
	before, _ := os.ReadFile(m.inv.Config.Root.Path)

	send(m, "I")
	m.form.set(fPath, writeTemp(t, "inventory.ini", inventoryINI))
	send(m, "ctrl+s")
	send(m, "esc")

	if m.pendingImport != nil {
		t.Error("escaping the preview left a plan pending")
	}
	if m.screen != screenList {
		t.Error("escape did not return to the list")
	}
	after, _ := os.ReadFile(m.inv.Config.Root.Path)
	if string(after) != string(before) {
		t.Error("cancelling still wrote to ssh_config")
	}
	if _, ok := m.inv.Host("imp-web1"); ok {
		t.Error("a cancelled import created a host")
	}
}

// TestImportOfAMissingFileStaysInTheForm checks that a bad path is reported
// where it was typed rather than dropping the user somewhere else.
func TestImportOfAMissingFileStaysInTheForm(t *testing.T) {
	m := newModel(t)
	send(m, "I")
	m.form.set(fPath, filepath.Join(t.TempDir(), "nope.ini"))
	send(m, "ctrl+s")

	if m.form == nil {
		t.Fatal("the form closed on an unreadable file")
	}
	if m.form.problem == "" {
		t.Error("no problem was shown")
	}
	if m.pendingImport != nil {
		t.Error("a plan was held for a file that could not be read")
	}
}

// TestImportGroupReplacesTheFilesOwn covers the group field, and the bug that
// made it worth a test of its own.
//
// It used to prefix: asking for "imported" produced "imported/prod/webservers".
// Naming a group is a plain instruction, and inventing a deeper one from the
// file's sections is not what anyone means by it.
func TestImportGroupReplacesTheFilesOwn(t *testing.T) {
	m := newModel(t)
	send(m, "I")
	m.form.set(fPath, writeTemp(t, "inventory.ini", inventoryINI))
	m.form.set(fGroup, "imported")
	send(m, "ctrl+s")
	send(m, "w")

	h, ok := m.inv.Host("imp-web1")
	if !ok {
		t.Fatal("imp-web1 was not created")
	}
	if h.Group != "imported" {
		t.Errorf("group = %q, want imported exactly", h.Group)
	}

	// With the field left empty the file's own structure is what survives.
	m2 := newModel(t)
	send(m2, "I")
	m2.form.set(fPath, writeTemp(t, "inventory.ini", inventoryINI))
	send(m2, "ctrl+s")
	send(m2, "w")
	if h, _ := m2.inv.Host("imp-web1"); h.Group != "prod/webservers" {
		t.Errorf("with no group given the file's own was %q, want prod/webservers", h.Group)
	}
}

// TestImportOfAnAlreadyPresentHostLeavesItAlone checks that re-importing a file
// is safe: the second run has nothing to write.
func TestImportOfAnAlreadyPresentHostLeavesItAlone(t *testing.T) {
	m := newModel(t)
	path := writeTemp(t, "inventory.ini", inventoryINI)

	send(m, "I")
	m.form.set(fPath, path)
	send(m, "ctrl+s")
	send(m, "w")

	send(m, "I")
	m.form.set(fPath, path)
	send(m, "ctrl+s")
	if m.pendingImport == nil {
		t.Fatal("no plan on the second run")
	}
	if got := m.pendingImport.Writes(); got != 0 {
		t.Errorf("re-importing would write %d host(s), want 0", got)
	}
}

// TestFileBrowserWalksToAFile covers the reason the browser exists: a path
// typed from memory is the one thing in this form that can be wrong in a way
// the form cannot report until you submit it.
func TestFileBrowserWalksToAFile(t *testing.T) {
	m := newModel(t)

	root := t.TempDir()
	sub := filepath.Join(root, "inventories")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hosts", "notes.txt", "servers.csv"} {
		if err := os.WriteFile(filepath.Join(sub, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	send(m, "I")
	m.form.set(fPath, root)
	send(m, "enter") // the file field opens the browser

	if m.mode != modePicker || m.picker == nil {
		t.Fatal("enter on the file field did not open the browser")
	}
	if !strings.Contains(m.picker.title, filepath.Base(root)) {
		t.Errorf("the browser does not say where it is: %q", m.picker.title)
	}

	// Walk into the directory.
	pick(t, m, "inventories/")
	if m.picker == nil {
		t.Fatal("choosing a directory closed the browser instead of opening it")
	}

	labels := pickerLabels(m)
	for _, want := range []string{"../", "hosts", "notes.txt", "servers.csv"} {
		if !contains(labels, want) {
			t.Errorf("the listing is missing %q: %v", want, labels)
		}
	}
	// The files tram can actually read are offered first.
	if idx(labels, "notes.txt") < idx(labels, "hosts") {
		t.Errorf("an unreadable file was listed above an inventory: %v", labels)
	}

	pick(t, m, "hosts")
	if m.picker != nil {
		t.Fatal("choosing a file left the browser open")
	}
	if m.mode != modeForm {
		t.Fatalf("choosing a file did not return to the form, mode = %v", m.mode)
	}
	if got := m.form.get(fPath); got != filepath.Join(sub, "hosts") {
		t.Errorf("the field holds %q", got)
	}
}

// TestFileBrowserGoesUp checks the parent entry, without which a wrong turn is
// a dead end.
func TestFileBrowserGoesUp(t *testing.T) {
	m := newModel(t)
	root := t.TempDir()
	sub := filepath.Join(root, "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	send(m, "I")
	m.form.set(fPath, sub)
	send(m, "enter")
	pick(t, m, "../")

	if m.picker == nil {
		t.Fatal("going up closed the browser")
	}
	if !strings.Contains(m.picker.title, filepath.Base(root)) {
		t.Errorf("did not go up: %q", m.picker.title)
	}
}

// TestFileBrowserStartsSomewhereSensible covers what the field can hold when
// the browser opens: nothing, a directory, a file, or a path half typed.
func TestFileBrowserStartsSomewhereSensible(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hosts")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := expandDir(dir); got != dir {
		t.Errorf("a directory gave %q", got)
	}
	if got := expandDir(file); got != dir {
		t.Errorf("a file did not give the directory holding it: %q", got)
	}
	if got := expandDir(filepath.Join(dir, "half-typed-na")); got != dir {
		t.Errorf("a half-typed path did not fall back to the deepest real part: %q", got)
	}
	if expandDir("") == "" {
		t.Error("an empty field gave nowhere to start")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if got := expandDir("~"); got != home {
			t.Errorf("~ gave %q, want %q", got, home)
		}
	}
}

func pick(t *testing.T, m *Model, label string) {
	t.Helper()
	p := m.picker
	if p == nil {
		t.Fatal("no browser is open")
	}
	for i, c := range p.visible {
		if c.label == label {
			p.cursor = i
			send(m, "enter")
			return
		}
	}
	t.Fatalf("%q is not in the listing: %v", label, pickerLabels(m))
}

func pickerLabels(m *Model) []string {
	if m.picker == nil {
		return nil
	}
	out := make([]string, 0, len(m.picker.visible))
	for _, c := range m.picker.visible {
		out = append(out, c.label)
	}
	return out
}

func contains(ss []string, want string) bool { return idx(ss, want) >= 0 }

func idx(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// TestBrowserShowsWholeFileNames guards the one thing a file browser must not
// do. The picker used a fixed label column, which is fine for host names and
// wrong for paths: a truncated file name is unusable for choosing between
// inventory-2024.ini and inventory-2025.ini.
func TestBrowserShowsWholeFileNames(t *testing.T) {
	m := newModel(t)
	dir := t.TempDir()
	long := "a-rather-long-inventory-file-name-2025.ini"
	if err := os.WriteFile(filepath.Join(dir, long), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	send(m, "I")
	m.form.set(fPath, dir)
	send(m, "enter")

	out := m.View()
	if !strings.Contains(out, long) {
		t.Errorf("the browser truncated the file name:\n%s", out)
	}
	if !strings.Contains(out, "enter opens a folder") {
		t.Errorf("the browser does not explain what enter does:\n%s", out)
	}
}

// TestImportFormSetsEveryHost covers the fields that fill in what an inventory
// does not say: a login, a key, a jump station.
func TestImportFormSetsEveryHost(t *testing.T) {
	m := newModel(t)
	send(m, "I")
	m.form.set(fPath, writeTemp(t, "inventory.ini", inventoryINI))
	m.form.set(fGroup, "vpb-prod")
	m.form.set(fUser, "root")
	m.form.set(fKey, "~/.ssh/id_ed25519")
	m.form.set(fJump, "bastion")
	send(m, "ctrl+s")
	send(m, "w")

	for _, name := range []string{"imp-web1", "imp-web2"} {
		h, ok := m.inv.Host(name)
		if !ok {
			t.Fatalf("%s was not created", name)
		}
		// The group replaces the file's own rather than nesting under it.
		if h.Group != "vpb-prod" {
			t.Errorf("%s group = %q, want vpb-prod exactly", name, h.Group)
		}
		if h.User != "root" {
			t.Errorf("%s user = %q; the file said deploy and the form said root", name, h.User)
		}
		if len(h.IdentityFiles) != 1 || h.IdentityFiles[0] != "~/.ssh/id_ed25519" {
			t.Errorf("%s keys = %v", name, h.IdentityFiles)
		}
		if h.ProxyJump != "bastion" {
			t.Errorf("%s jump = %q", name, h.ProxyJump)
		}
	}
}
