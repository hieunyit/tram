package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hieuny/tram/internal/model"
)

// TestCreateAnAccountFromTheHostForm is the path that was missing: you are
// filling in a host, you reach the account field, and the account you want does
// not exist yet.
//
// The half-filled host has to survive that, which is why the account form
// stacks on top rather than replacing it.
func TestCreateAnAccountFromTheHostForm(t *testing.T) {
	m := newModel(t)
	key := writeKey(t)

	send(m, "a") // add a host
	m.form.set(fName, "newbox")
	m.form.set(fAddr, "10.5.5.5")

	// Open the account picker on the account field and choose to make one.
	m.form.focus(indexOf(m.form, fAccount))
	send(m, "enter")
	if m.mode != modePicker {
		t.Fatal("the account field did not open a picker")
	}
	if !strings.Contains(m.View(), "new account") {
		t.Fatalf("the picker offers no way to create one:\n%s", m.View())
	}
	pick(t, m, m.gl.plus+" new account")

	if m.form == nil || m.form.kind != formAccount {
		t.Fatal("the account form did not open")
	}
	m.form.set(fName, "deploy")
	m.form.set(fUser, "root")
	m.form.set(fAuth, string(model.AuthKey))
	m.form.set(fKey, key)
	send(m, "ctrl+s")

	// Back on the host, with the new account filled in and nothing lost.
	if m.form == nil || m.form.kind != formAdd {
		t.Fatalf("did not return to the host form: %#v", m.form)
	}
	if got := m.form.get(fName); got != "newbox" {
		t.Errorf("the half-filled host was lost: name = %q", got)
	}
	if got := m.form.get(fAddr); got != "10.5.5.5" {
		t.Errorf("the address was lost: %q", got)
	}
	if got := m.form.get(fAccount); got != "deploy" {
		t.Errorf("the new account was not filled in: %q", got)
	}
	if _, ok := m.inv.Store.Account("deploy"); !ok {
		t.Fatal("the account was not stored")
	}

	// Finishing the host writes it linked.
	send(m, "ctrl+s")
	h, ok := m.inv.Host("newbox")
	if !ok {
		t.Fatalf("the host was not created: %v", m.problem)
	}
	if h.Account != "deploy" {
		t.Errorf("account = %q", h.Account)
	}
	if h.User != "root" {
		t.Errorf("the account did not write its user: %q", h.User)
	}
}

// TestAccountFormRefusesWhatWillNotWork checks the three ways to get it wrong,
// each reported where it was typed.
func TestAccountFormRefusesWhatWillNotWork(t *testing.T) {
	m := newModel(t)
	if err := m.inv.Store.PutAccount(model.Account{Name: "taken", User: "u", Auth: model.AuthAgent}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, user, auth, key, want string
	}{
		{"", "root", string(model.AuthAgent), "", "name"},
		{"x", "", string(model.AuthAgent), "", "user"},
		{"taken", "root", string(model.AuthAgent), "", "already exists"},
		{"y", "root", string(model.AuthKey), filepath.Join(t.TempDir(), "nope"), "no key file"},
	}
	for _, c := range cases {
		openNewAccountForm(t, m)
		m.form.set(fName, c.name)
		m.form.set(fUser, c.user)
		m.form.set(fAuth, c.auth)
		m.form.set(fKey, c.key)
		send(m, "ctrl+s")

		if m.form == nil {
			t.Fatalf("%q was accepted", c.name)
		}
		if !strings.Contains(m.form.problem, c.want) {
			t.Errorf("for %q the message was %q, want something about %q", c.name, m.form.problem, c.want)
		}
		send(m, "esc")
	}
}

// TestAccountFormHidesTheKeyForKeylessMethods checks the form does not show a
// box that cannot mean anything.
func TestAccountFormHidesTheKeyForKeylessMethods(t *testing.T) {
	m := newModel(t)
	openNewAccountForm(t, m)

	m.form.set(fAuth, string(model.AuthKey))
	m.form.syncAuth()
	if fieldHidden(m.form, fKey) {
		t.Error("key authentication hid the key field")
	}

	m.form.set(fAuth, string(model.AuthAgent))
	m.form.syncAuth()
	if !fieldHidden(m.form, fKey) {
		t.Error("agent authentication still shows a key field")
	}
	if strings.Contains(m.form.view(80, 24), "press enter to browse") {
		t.Error("the hidden key field is still drawn")
	}
}

// TestEscapeFromAStackedFormGoesBack checks that abandoning the account keeps
// the host underneath.
func TestEscapeFromAStackedFormGoesBack(t *testing.T) {
	m := newModel(t)
	send(m, "a")
	m.form.set(fName, "keepme")
	m.form.focus(indexOf(m.form, fAccount))
	send(m, "enter")
	pick(t, m, m.gl.plus+" new account")

	send(m, "esc")
	if m.form == nil || m.form.kind != formAdd {
		t.Fatal("escaping the account form did not return to the host form")
	}
	if got := m.form.get(fName); got != "keepme" {
		t.Errorf("the host form was lost: %q", got)
	}

	send(m, "esc")
	if m.form != nil {
		t.Error("escaping the last form did not close it")
	}
}

// openNewAccountForm walks the way a person does: the account picker, then the
// entry that makes one.
func openNewAccountForm(t *testing.T, m *Model) {
	t.Helper()
	send(m, "A")
	if m.mode != modePicker {
		t.Fatalf("A did not open the account picker, mode = %v", m.mode)
	}
	pick(t, m, m.gl.plus+" new account")
	if m.form == nil || m.form.kind != formAccount {
		t.Fatal("the create entry did not open the account form")
	}
}

func writeKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(p, []byte("not really a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func indexOf(f *form, id fieldID) int {
	for i, fl := range f.fields {
		if fl.id == id {
			return i
		}
	}
	return 0
}

func fieldHidden(f *form, id fieldID) bool {
	for _, fl := range f.fields {
		if fl.id == id {
			return fl.hidden
		}
	}
	return false
}
