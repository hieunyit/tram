package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/secret"
)

// newAccountEntry is the value the picker returns for "make one". It starts
// with a NUL so that it can never collide with a real account name.
var newAccountEntry = string(rune(0)) + "new-account"

// openAccountForm asks for a new identity, on top of whatever form was already
// open.
//
// It stacks rather than replaces, because it is nearly always reached from the
// middle of another job: you are filling in a host, you reach the account
// field, and the account you want does not exist yet. Losing the half-filled
// host to create it would be its own small insult.
func (m *Model) openAccountForm(afterwards func(name string) tea.Cmd) {
	f := &form{
		kind:  formAccount,
		st:    m.st,
		gl:    m.gl,
		title: "new account",
		note:  "an identity: who you log in as and how you prove it, with no address of its own",
		after: afterwards,
	}
	f.fields = []*field{
		{id: fName, label: "name", input: newInput("", "deploy"), hint: "what you will call it, not the login"},
		{id: fUser, label: "user", input: newInput("", "root"), hint: "the login name this identity uses"},
		{id: fAuth, label: "auth", input: newInput(string(model.AuthKey), "key"), pick: true,
			hint: "key, agent or password"},
		{id: fKey, label: "key", input: newInput("", "press enter to browse"), pick: true,
			hint: "enter opens the folder; the private key, not the .pub"},
		{id: fDesc, label: "desc", input: newInput("", "")},
	}
	f.syncAuth()
	f.focus(0)
	m.pushForm(f)
}

// openAccountFormFor opens an identity that already exists, so that a key can
// be corrected rather than only replaced.
func (m *Model) openAccountFormFor(a model.Account) {
	m.openAccountForm(nil)
	f := m.form
	f.replaces = a.Name
	f.title = "edit " + a.Name
	f.note = "changing the name moves every host that uses this identity to the new one"
	f.set(fName, a.Name)
	f.set(fUser, a.User)
	f.set(fAuth, string(a.Auth))
	f.set(fKey, a.KeyPath)
	f.set(fDesc, a.Desc)
	f.syncAuth()
	f.focus(0)
}

// syncAuth hides the key field for the methods that have no key file, so the
// form never shows a box that cannot mean anything.
func (f *form) syncAuth() {
	byKey := f.get(fAuth) == string(model.AuthKey)
	for _, fl := range f.fields {
		if fl.id == fKey {
			fl.hidden = !byKey
		}
	}
}

// submitAccount validates and stores the identity, then hands the name back to
// whatever asked for it.
func (m *Model) submitAccount() (tea.Model, tea.Cmd) {
	f := m.form
	a := model.Account{
		Name:    f.get(fName),
		User:    f.get(fUser),
		Auth:    model.AuthMethod(f.get(fAuth)),
		KeyPath: f.get(fKey),
		Desc:    f.get(fDesc),
	}
	if a.Auth != model.AuthKey {
		a.KeyPath = ""
	}
	if err := a.Valid(); err != nil {
		f.problem = err.Error()
		return m, nil
	}
	if _, exists := m.inv.Store.Account(a.Name); exists && !strings.EqualFold(a.Name, f.replaces) {
		f.problem = fmt.Sprintf("an account called %q already exists", a.Name)
		return m, nil
	}
	// Say so now rather than at connection time, where a missing key file looks
	// like the far end refusing you.
	var warn string
	if a.Auth == model.AuthKey {
		info, err := secret.InspectKey(a.KeyPath)
		switch {
		case err != nil && !info.Exists:
			f.problem = fmt.Sprintf("no key file at %s", a.KeyPath)
			return m, nil
		case err != nil:
			warn = err.Error()
		case info.Encrypted:
			warn = "that key is passphrase protected; tram will ask once per run"
		}
	}
	if err := m.inv.Store.PutAccount(a); err != nil {
		f.problem = err.Error()
		return m, nil
	}
	// A rename has to take the hosts with it, or they end up pointing at an
	// identity that no longer exists.
	if f.replaces != "" && !strings.EqualFold(f.replaces, a.Name) {
		for _, host := range m.inv.Store.LinkedHosts(f.replaces) {
			_ = m.inv.Store.SetLink(host, a.Name)
		}
		if err := m.inv.Store.DeleteAccount(f.replaces); err != nil {
			f.problem = err.Error()
			return m, nil
		}
	}

	after := f.after
	m.popForm()
	m.reload()

	msg := "created account " + a.Name
	if f.replaces != "" {
		msg = "saved account " + a.Name
	}
	if warn != "" {
		msg += "  " + m.gl.dot + "  " + warn
	}
	if after != nil {
		if cmd := after(a.Name); cmd != nil {
			return m, tea.Batch(cmd, note(msg))
		}
	}
	return m, note(msg)
}

// accountChoices lists the identities, with the entry that makes a new one.
func (m *Model) accountChoices(noneLabel, noneNote string) []choice {
	items := []choice{{value: "", label: noneLabel, note: noneNote}}
	for _, a := range m.inv.Store.AccountList() {
		note := a.User + "  " + string(a.Auth)
		if a.KeyPath != "" {
			note += "  " + a.KeyPath
		}
		items = append(items, choice{value: a.Name, label: a.Name, note: note})
	}
	return append(items, choice{value: newAccountEntry, label: m.gl.plus + " new account", create: true})
}

// authChoices are the three ways an identity can prove itself.
func (m *Model) authChoices() []choice {
	return []choice{
		{value: string(model.AuthKey), label: "key", note: "a private key file, written as IdentityFile"},
		{value: string(model.AuthAgent), label: "agent", note: "whatever the ssh agent already holds"},
		{value: string(model.AuthPassword), label: "password", note: "ssh asks; tram stores nothing"},
	}
}
