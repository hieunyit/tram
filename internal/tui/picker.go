package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
)

// choice is one entry in a picker.
type choice struct {
	value string
	label string
	note  string
	// blocked entries are shown but cannot be chosen, with the reason next to
	// them. A jump station that would form a loop is listed this way rather
	// than hidden, so the user sees why it is not an option.
	blocked string
	// create marks the entry that makes a new item instead of choosing one.
	create bool
}

type picker struct {
	title   string
	st      styles
	gl      glyphs
	items   []choice
	cursor  int
	offset  int
	onPick  func(string) tea.Cmd
	filter  string
	visible []choice
}

func (p *picker) refilter() {
	p.visible = nil
	q := strings.ToLower(p.filter)
	for _, c := range p.items {
		if q == "" || strings.Contains(strings.ToLower(c.label+" "+c.value+" "+c.note), q) {
			p.visible = append(p.visible, c)
		}
	}
	if p.cursor >= len(p.visible) {
		p.cursor = max(0, len(p.visible)-1)
	}
}

func (m *Model) showPicker(title string, items []choice, onPick func(string) tea.Cmd) {
	p := &picker{title: title, st: m.st, gl: m.gl, items: items, onPick: onPick}
	p.refilter()
	m.picker = p
	m.mode = modePicker
}

func (m *Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	switch msg.String() {
	case "esc":
		m.picker = nil
		if m.form != nil {
			m.mode = modeForm
			return m, nil
		}
		m.mode = modeNormal
		return m, nil
	case "up", "ctrl+p":
		p.cursor = max(0, p.cursor-1)
	case "down", "ctrl+n":
		p.cursor = min(len(p.visible)-1, p.cursor+1)
	case "backspace":
		if p.filter != "" {
			p.filter = p.filter[:len(p.filter)-1]
			p.refilter()
		}
	case "enter":
		if p.cursor < 0 || p.cursor >= len(p.visible) {
			return m, nil
		}
		c := p.visible[p.cursor]
		if c.blocked != "" {
			m.problem = c.blocked
			return m, nil
		}
		m.picker = nil
		if m.form != nil {
			m.mode = modeForm
		} else {
			m.mode = modeNormal
		}
		return m, p.onPick(c.value)
	default:
		if s := msg.String(); len(s) == 1 {
			p.filter += s
			p.refilter()
		}
	}
	return m, nil
}

func (p *picker) view(width, height int) string {
	var b strings.Builder
	b.WriteString(p.st.title.Render(p.title) + "\n")
	if p.filter != "" {
		b.WriteString(p.st.muted.Render("  filter "+p.filter) + "\n")
	}
	b.WriteString("\n")

	visibleRows := max(3, height-8)
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+visibleRows {
		p.offset = p.cursor - visibleRows + 1
	}
	end := min(p.offset+visibleRows, len(p.visible))

	if len(p.visible) == 0 {
		b.WriteString(p.st.muted.Render("  nothing matches") + "\n")
	}
	for i := p.offset; i < end; i++ {
		c := p.visible[i]
		marker := "  "
		if i == p.cursor {
			marker = p.st.selected.Render(p.gl.arrow[:1]) + " "
		}
		label := pad(c.label, 24)
		line := marker + label
		switch {
		case c.blocked != "":
			line = marker + p.st.muted.Render(label) + " " + p.st.bad.Render(c.blocked)
		case c.note != "":
			line += " " + p.st.muted.Render(c.note)
		}
		if c.create {
			line = marker + p.st.ok.Render(label)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + p.st.help.Render("  type to filter  enter choose  esc back"))
	return b.String()
}

// ---- the specific pickers -------------------------------------------------

func (m *Model) openFieldPicker(id fieldID) (tea.Model, tea.Cmd) {
	switch id {
	case fAccount:
		items := []choice{{value: "", label: "(none)", note: "leave User and IdentityFile to the stanza"}}
		for _, a := range m.inv.Store.AccountList() {
			items = append(items, choice{value: a.Name, label: a.Name, note: a.User + " " + string(a.Auth)})
		}
		m.showPicker("account", items, func(v string) tea.Cmd {
			m.form.set(fAccount, v)
			m.form.syncAccount()
			return nil
		})
	case fGroup:
		items := []choice{{value: "", label: "(none)"}}
		for _, g := range m.inv.Groups() {
			items = append(items, choice{value: g, label: g})
		}
		m.showPicker("group", items, func(v string) tea.Cmd {
			m.form.set(fGroup, v)
			return nil
		})
	case fJump:
		items := m.jumpChoices(m.form.get(fName))
		m.showPicker("jump station", items, func(v string) tea.Cmd {
			m.form.set(fJump, v)
			return nil
		})
	}
	return m, nil
}

// jumpChoices lists the hosts that could serve as a jump station, and shows the
// ones that cannot with the reason attached.
//
// A ProxyJump loop is not a configuration error ssh reports. ssh hangs. So the
// picker marks the hosts that would form one rather than letting them be picked
// and discovered later as a session that never opens.
func (m *Model) jumpChoices(self string) []choice {
	items := []choice{{value: "", label: "(direct)", note: "no jump station"}}
	for _, h := range m.inv.Hosts() {
		if self != "" && strings.EqualFold(h.Name, self) {
			continue
		}
		c := choice{value: h.Name, label: h.Name, note: h.Target()}
		if self != "" && model.WouldCycle(self, h.Name, m.inv.Lookup) {
			c.blocked = "would form a ProxyJump loop"
		}
		items = append(items, c)
	}
	return items
}

func (m *Model) openAccountPicker() (tea.Model, tea.Cmd) {
	sel := m.selection()
	if len(sel) == 0 {
		return m, nil
	}
	items := []choice{{value: "", label: "(unlink)", note: "keeps User and IdentityFile as they are"}}
	for _, a := range m.inv.Store.AccountList() {
		items = append(items, choice{value: a.Name, label: a.Name, note: a.User + " " + string(a.Auth)})
	}
	title := fmt.Sprintf("link %d host(s) to an account", len(sel))
	m.showPicker(title, items, func(v string) tea.Cmd {
		n := 0
		for _, h := range sel {
			spec := inventory.Spec{Name: h.Name, Account: inventory.Str(v)}
			ch, err := m.inv.Edit(h.Name, spec, false)
			if err != nil {
				return fail(err)
			}
			if err := ch.Apply(); err != nil {
				return fail(err)
			}
			n++
		}
		m.reload()
		if v == "" {
			return note(fmt.Sprintf("unlinked %d host(s)", n))
		}
		return note(fmt.Sprintf("linked %d host(s) to %s", n, v))
	})
	return m, nil
}

func (m *Model) openSnippetPicker() (tea.Model, tea.Cmd) {
	sel := m.selection()
	if len(sel) == 0 {
		return m, nil
	}
	_ = m.inv.Store.SeedSnippets()
	var items []choice
	for _, s := range m.inv.Store.SnippetList() {
		c := choice{value: s.Name, label: s.Name, note: s.Command}
		if s.Confirm {
			c.note = m.gl.cross + " " + s.Command
		}
		items = append(items, c)
	}
	if len(items) == 0 {
		return m, fail(fmt.Errorf("no snippets; add one with `tram snippet add`"))
	}
	m.showPicker(fmt.Sprintf("snippet on %d host(s)", len(sel)), items, func(v string) tea.Cmd {
		sn, ok := m.inv.Store.Snippet(v)
		if !ok {
			return fail(fmt.Errorf("snippet %q vanished", v))
		}
		if sn.Confirm {
			m.confirmText = fmt.Sprintf("%s is marked as needing confirmation. Run %q on %d host(s)?", sn.Name, sn.Command, len(sel))
			m.confirmFn = func() tea.Cmd { return results("snippet: "+sn.Name, m.Runner.Exec(sel, sn.Command)) }
			m.mode = modeConfirm
			return nil
		}
		return results("snippet: "+sn.Name, m.Runner.Exec(sel, sn.Command))
	})
	return m, nil
}

// ---- confirmation ---------------------------------------------------------

func (m *Model) confirmDelete() (tea.Model, tea.Cmd) {
	sel := m.selection()
	if len(sel) == 0 {
		return m, nil
	}
	var warnings []string
	for _, h := range sel {
		if deps := model.Dependents(h.Name, m.inv.Hosts()); len(deps) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s is a jump station for %s", h.Name, strings.Join(model.Names(deps), ", ")))
		}
	}
	text := fmt.Sprintf("delete %s?", strings.Join(model.Names(sel), ", "))
	if len(warnings) > 0 {
		text = strings.Join(warnings, "; ") + ". " + text
	}
	m.confirmText = text
	m.confirmFn = func() tea.Cmd {
		n := 0
		for _, h := range sel {
			ch, err := m.inv.Remove(h.Name, false)
			if err != nil {
				return fail(err)
			}
			if err := ch.Apply(); err != nil {
				return fail(err)
			}
			delete(m.marked, h.Name)
			n++
		}
		m.reload()
		return note(fmt.Sprintf("deleted %d host(s)", n))
	}
	m.mode = modeConfirm
	return m, nil
}

func (m *Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		fn := m.confirmFn
		m.mode = modeNormal
		m.confirmFn = nil
		m.confirmText = ""
		if fn != nil {
			return m, fn()
		}
	default:
		m.mode = modeNormal
		m.confirmFn = nil
		m.confirmText = ""
	}
	return m, nil
}
