package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/importer"
	"github.com/hieuny/tram/internal/inventory"
)

// openImportForm asks for a file to read. The path is the only thing that has
// to be typed: the format is worked out from the file, and the preview does the
// rest of the explaining.
func (m *Model) openImportForm() {
	f := &form{
		kind:  formImport,
		st:    m.st,
		gl:    m.gl,
		title: "import an inventory",
		note:  "Ansible INI, Ansible YAML or a CSV export; nothing is written until you confirm",
	}
	f.fields = []*field{
		{id: fPath, label: "file", input: newInput("", "inventory.ini"), hint: "path to the file to read"},
		{id: fGroup, label: "under", input: newInput("", "keep the file's own groups"), hint: "optional group to file everything under"},
	}
	f.focus(0)
	m.form = f
	m.mode = modeForm
}

// submitImport reads the file and shows what importing it would do.
//
// It stops there. The plan is held and a second, explicit key applies it,
// because an import can create dozens of hosts at once and a form that writes
// on enter gives you no chance to look first.
func (m *Model) submitImport() (tea.Model, tea.Cmd) {
	f := m.form
	path := strings.Trim(f.get(fPath), `"'`)
	if path == "" {
		f.problem = "no file given"
		return m, nil
	}
	plan, err := m.inv.PlanImport(path, importer.Options{GroupPrefix: f.get(fGroup)}, false)
	if err != nil {
		f.problem = err.Error()
		return m, nil
	}

	m.mode = modeNormal
	m.form = nil
	m.pendingImport = plan
	return m, results(fmt.Sprintf("import %s (%s)", plan.Source, plan.Format), importRows(plan))
}

// importRows renders a plan for the result screen, one collapsible block per
// host, so that the summary line is readable and the detail is one key away.
func importRows(plan *inventory.ImportPlan) []Row {
	rows := make([]Row, 0, len(plan.Items)+1)
	for _, it := range plan.Items {
		r := it.Record

		target := r.HostName
		if r.User != "" && target != "" {
			target = r.User + "@" + target
		}
		if r.Port != "" && r.Port != "22" {
			target += ":" + r.Port
		}

		var body strings.Builder
		line := func(k, v string) {
			if v != "" {
				fmt.Fprintf(&body, "%-10s %s\n", k, v)
			}
		}
		line("address", r.HostName)
		line("user", r.User)
		line("port", r.Port)
		line("key", r.Key)
		line("jump", r.ProxyJump)
		line("group", r.Group)
		line("desc", r.Desc)
		line("from", r.Where)
		if len(r.Groups) > 1 {
			line("groups", strings.Join(r.Groups, ", "))
		}
		for _, c := range it.Changes {
			fmt.Fprintf(&body, "%-10s %s\n", "change", c)
		}

		summary := target
		if it.Reason != "" {
			summary = it.Reason
		}
		rows = append(rows, Row{
			Host:    r.Name,
			Status:  string(it.Action),
			OK:      it.Action == inventory.ImportAdd || it.Action == inventory.ImportUpdate,
			Summary: summary,
			Body:    body.String(),
		})
	}

	// Warnings and skipped hosts belong on the screen, not on a stderr stream
	// the interface has covered up.
	if len(plan.Warnings) > 0 || len(plan.Skipped) > 0 {
		var body strings.Builder
		for _, w := range plan.Warnings {
			fmt.Fprintf(&body, "%-9s %s\n", "warning", w)
		}
		for _, s := range plan.Skipped {
			fmt.Fprintf(&body, "%-9s %s\n", "skipped", s)
		}
		rows = append(rows, Row{
			Host:    "(notes)",
			Status:  "INFO",
			Summary: fmt.Sprintf("%d warning(s), %d not reachable over ssh", len(plan.Warnings), len(plan.Skipped)),
			Body:    body.String(),
		})
	}
	return rows
}

// applyImport writes the held plan and returns to the list.
func (m *Model) applyImport() tea.Cmd {
	plan := m.pendingImport
	m.pendingImport = nil
	if plan == nil {
		return nil
	}
	if plan.Writes() == 0 {
		m.screen = screenList
		m.result = nil
		return note("nothing to import from " + plan.Source)
	}
	ch, err := m.inv.ApplyImport(plan, false)
	if err != nil {
		return fail(err)
	}
	if err := ch.Apply(); err != nil {
		return fail(err)
	}
	m.reload()
	m.screen = screenList
	m.result = nil

	msg := fmt.Sprintf("imported %d host(s)", plan.Writes())
	if len(ch.Warnings) > 0 {
		msg += "  " + m.gl.dot + "  " + ch.Warnings[0]
	}
	return note(msg)
}
