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

// TestImportUnderAGroup covers the second field of the form.
func TestImportUnderAGroup(t *testing.T) {
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
	if h.Group != "imported/prod/webservers" {
		t.Errorf("group = %q", h.Group)
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
