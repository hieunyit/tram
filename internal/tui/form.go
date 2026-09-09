package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
)

type formKind int

const (
	formAdd formKind = iota
	formEdit
	formClone
	formBatch
	formExec
	formImport
)

// fieldID names a form field so the code reads as something other than indexes.
type fieldID string

const (
	fName    fieldID = "name"
	fAddr    fieldID = "addr"
	fUser    fieldID = "user"
	fPort    fieldID = "port"
	fKey     fieldID = "key"
	fJump    fieldID = "jump"
	fGroup   fieldID = "group"
	fDesc    fieldID = "desc"
	fAccount fieldID = "account"
	fCommand fieldID = "command"
	fPath    fieldID = "path"
)

type field struct {
	id    fieldID
	label string
	hint  string
	input textinput.Model
	// pick opens a chooser instead of accepting free text, which is how
	// account, group and jump avoid being typed wrong.
	pick bool
	// hidden fields are not drawn. Choosing an account hides user and key,
	// because the account decides them and showing two sources of truth invites
	// the question of which one wins.
	hidden bool
}

type form struct {
	kind    formKind
	title   string
	st      styles
	gl      glyphs
	fields  []*field
	cursor  int
	targets []model.Host // batch edit
	origin  model.Host   // edit and clone
	note    string
	problem string
}

func newInput(value, placeholder string) textinput.Model {
	t := textinput.New()
	t.SetValue(value)
	t.Placeholder = placeholder
	t.Prompt = ""
	t.CharLimit = 200
	return t
}

func (m *Model) openForm(kind formKind, h model.Host) {
	f := &form{kind: kind, st: m.st, gl: m.gl, origin: h}
	switch kind {
	case formAdd:
		f.title = "add host"
	case formEdit:
		f.title = "edit " + h.Name
	case formClone:
		f.title = "clone " + h.Name
	}

	name := h.Name
	if kind == formClone {
		name = h.Name + "-copy"
	}
	if kind == formAdd {
		name = ""
	}

	f.fields = []*field{
		{id: fName, label: "name", input: newInput(name, "web1"), hint: "the name you type after tram"},
		{id: fAddr, label: "address", input: newInput(h.HostName, "10.0.0.1"), hint: "written as HostName"},
		{id: fAccount, label: "account", input: newInput(h.Account, "none"), pick: true, hint: "an identity; press enter to choose"},
		{id: fUser, label: "user", input: newInput(h.User, "")},
		{id: fKey, label: "key", input: newInput(strings.Join(h.IdentityFiles, ","), "~/.ssh/id_ed25519")},
		{id: fPort, label: "port", input: newInput(h.Port, "22")},
		{id: fJump, label: "jump", input: newInput(h.ProxyJump, "none"), pick: true, hint: "a station to go through; enter to choose"},
		{id: fGroup, label: "group", input: newInput(h.Group, "prod/web"), pick: true, hint: "slashes nest groups"},
		{id: fDesc, label: "desc", input: newInput(h.Desc, "")},
	}
	f.syncAccount()
	f.focus(0)
	m.form = f
	m.mode = modeForm
}

func (m *Model) openBatchForm(hosts []model.Host) {
	f := &form{
		kind:    formBatch,
		st:      m.st,
		gl:      m.gl,
		targets: hosts,
		title:   fmt.Sprintf("edit %d hosts", len(hosts)),
		note:    "empty fields are left alone on every host",
	}
	f.fields = []*field{
		{id: fAccount, label: "account", input: newInput("", "leave alone"), pick: true},
		{id: fUser, label: "user", input: newInput("", "leave alone")},
		{id: fPort, label: "port", input: newInput("", "leave alone")},
		{id: fJump, label: "jump", input: newInput("", "leave alone"), pick: true},
		{id: fGroup, label: "group", input: newInput("", "leave alone"), pick: true},
		{id: fKey, label: "key", input: newInput("", "leave alone")},
	}
	f.focus(0)
	m.form = f
	m.mode = modeForm
}

func (m *Model) openExecForm() {
	sel := m.selection()
	f := &form{
		kind:    formExec,
		st:      m.st,
		gl:      m.gl,
		targets: sel,
		title:   fmt.Sprintf("run a command on %d host(s)", len(sel)),
		note:    "the command runs without a terminal; anything interactive belongs in a session",
	}
	f.fields = []*field{{id: fCommand, label: "command", input: newInput("", "uptime")}}
	f.focus(0)
	m.form = f
	m.mode = modeForm
}

func (f *form) get(id fieldID) string {
	for _, fl := range f.fields {
		if fl.id == id {
			return strings.TrimSpace(fl.input.Value())
		}
	}
	return ""
}

func (f *form) set(id fieldID, v string) {
	for _, fl := range f.fields {
		if fl.id == id {
			fl.input.SetValue(v)
		}
	}
}

// syncAccount hides the user and key fields once an account is chosen, and
// shows a line saying what that account will write instead. Two editable
// sources for the same value is the fastest way to make a form untrustworthy.
func (f *form) syncAccount() {
	acct := f.get(fAccount)
	for _, fl := range f.fields {
		if fl.id == fUser || fl.id == fKey {
			fl.hidden = acct != ""
		}
	}
}

func (f *form) focus(i int) {
	for _, fl := range f.fields {
		fl.input.Blur()
	}
	f.cursor = i
	if i >= 0 && i < len(f.fields) {
		f.fields[i].input.Focus()
	}
}

func (f *form) move(d int) {
	n := len(f.fields)
	for k := 0; k < n; k++ {
		f.cursor = (f.cursor + d + n) % n
		if !f.fields[f.cursor].hidden {
			break
		}
	}
	f.focus(f.cursor)
}

func (m *Model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.form = nil
		return m, nil
	case "tab", "down":
		f.move(1)
		return m, nil
	case "shift+tab", "up":
		f.move(-1)
		return m, nil
	case "ctrl+s":
		return m.submitForm()
	case "enter":
		cur := f.fields[f.cursor]
		if cur.pick {
			return m.openFieldPicker(cur.id)
		}
		if f.cursor == len(f.fields)-1 {
			return m.submitForm()
		}
		f.move(1)
		return m, nil
	}
	var cmd tea.Cmd
	f.fields[f.cursor].input, cmd = f.fields[f.cursor].input.Update(msg)
	if f.fields[f.cursor].id == fAccount {
		f.syncAccount()
	}
	return m, cmd
}

func (f *form) view(width, height int) string {
	var b strings.Builder
	b.WriteString(f.st.title.Render(f.title) + "\n\n")
	if f.note != "" {
		b.WriteString(f.st.muted.Render("  "+f.note) + "\n\n")
	}

	labelW := 10
	for i, fl := range f.fields {
		if fl.hidden {
			continue
		}
		marker := "  "
		if i == f.cursor {
			marker = f.st.selected.Render(f.gl.arrow[:1]) + " "
		}
		label := f.st.label.Render(pad(fl.label, labelW))
		b.WriteString(marker + label + " " + fl.input.View() + "\n")
		if i == f.cursor && fl.hint != "" {
			b.WriteString("  " + strings.Repeat(" ", labelW+1) + f.st.muted.Render(fl.hint) + "\n")
		}
	}

	if acct := f.get(fAccount); acct != "" {
		b.WriteString("\n" + f.st.muted.Render(
			"  account "+acct+" will write User and IdentityFile into this stanza") + "\n")
	}
	if f.problem != "" {
		b.WriteString("\n  " + f.st.bad.Render(f.problem) + "\n")
	}
	b.WriteString("\n" + f.st.help.Render("  tab move  enter next or choose  ctrl+s save  esc cancel"))
	return b.String()
}

// submitForm turns the form into a change and applies it.
func (m *Model) submitForm() (tea.Model, tea.Cmd) {
	f := m.form
	switch f.kind {
	case formImport:
		return m.submitImport()

	case formExec:
		cmdText := f.get(fCommand)
		if cmdText == "" {
			f.problem = "no command given"
			return m, nil
		}
		hosts := f.targets
		m.mode = modeNormal
		m.form = nil
		return m, results("exec: "+cmdText, m.Runner.Exec(hosts, cmdText))

	case formBatch:
		var applied int
		for _, h := range f.targets {
			spec := inventory.Spec{Name: h.Name}
			if v := f.get(fAccount); v != "" {
				spec.Account = inventory.Str(v)
			}
			if v := f.get(fUser); v != "" {
				spec.User = inventory.Str(v)
			}
			if v := f.get(fPort); v != "" {
				spec.Port = inventory.Str(v)
			}
			if v := f.get(fJump); v != "" {
				spec.ProxyJump = inventory.Str(v)
			}
			if v := f.get(fGroup); v != "" {
				spec.Group = inventory.Str(v)
			}
			if v := f.get(fKey); v != "" {
				spec.SetIdentityFiles(splitList(v))
			}
			ch, err := m.inv.Edit(h.Name, spec, false)
			if err != nil {
				f.problem = err.Error()
				return m, nil
			}
			if err := ch.Apply(); err != nil {
				f.problem = err.Error()
				return m, nil
			}
			applied++
		}
		m.mode = modeNormal
		m.form = nil
		m.reload()
		return m, note(fmt.Sprintf("updated %d hosts", applied))
	}

	spec := inventory.Spec{Name: f.get(fName)}
	spec.HostName = inventory.Str(f.get(fAddr))
	spec.Port = inventory.Str(f.get(fPort))
	spec.ProxyJump = inventory.Str(f.get(fJump))
	spec.Group = inventory.Str(f.get(fGroup))
	spec.Desc = inventory.Str(f.get(fDesc))
	spec.Account = inventory.Str(f.get(fAccount))
	if f.get(fAccount) == "" {
		spec.User = inventory.Str(f.get(fUser))
		spec.SetIdentityFiles(splitList(f.get(fKey)))
	}

	var (
		ch  *inventory.Change
		err error
	)
	switch f.kind {
	case formAdd:
		ch, err = m.inv.Add(spec)
	case formClone:
		ch, err = m.inv.Clone(f.origin.Name, spec.Name, spec)
	case formEdit:
		if spec.Name != f.origin.Name {
			rn, rerr := m.inv.Rename(f.origin.Name, spec.Name, false)
			if rerr != nil {
				f.problem = rerr.Error()
				return m, nil
			}
			if aerr := rn.Apply(); aerr != nil {
				f.problem = aerr.Error()
				return m, nil
			}
		}
		ch, err = m.inv.Edit(spec.Name, spec, false)
	}
	if err != nil {
		f.problem = err.Error()
		return m, nil
	}
	if err := ch.Apply(); err != nil {
		f.problem = err.Error()
		return m, nil
	}

	m.mode = modeNormal
	m.form = nil
	m.reload()
	msg := spec.Name + " saved"
	if len(ch.Warnings) > 0 {
		msg += "  " + m.gl.dot + "  " + ch.Warnings[0]
	}
	return m, note(msg)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
