package inventory

import (
	"fmt"
	"strings"

	"github.com/hieuny/tram/internal/importer"
	"github.com/hieuny/tram/internal/model"
)

// ImportAction is what tram would do with one incoming record.
type ImportAction string

const (
	// ImportAdd creates a host that does not exist yet.
	ImportAdd ImportAction = "add"
	// ImportUpdate changes an existing host, and only happens with --overwrite.
	ImportUpdate ImportAction = "update"
	// ImportSkip leaves an existing host alone, which is the default.
	ImportSkip ImportAction = "skip"
	// ImportUnchanged means the host already says what the file says.
	ImportUnchanged ImportAction = "unchanged"
	// ImportReject means the record cannot be written.
	ImportReject ImportAction = "reject"
)

// ImportItem is one record with the decision tram made about it.
type ImportItem struct {
	Record importer.Record `json:"record"`
	Action ImportAction    `json:"action"`
	Reason string          `json:"reason"`
	// Changes describes what an update would alter, field by field.
	Changes []string `json:"changes"`
}

// ImportPlan is the whole of an import, decided but not yet written.
//
// Deciding and writing are separate so that the preview, the dry run and the
// real thing all look at the same answer. A preview that runs different code
// from the write is a preview of nothing.
type ImportPlan struct {
	Source   string       `json:"source"`
	Format   string       `json:"format"`
	Items    []ImportItem `json:"items"`
	Warnings []string     `json:"warnings"`
	Skipped  []string     `json:"skipped"`
}

// Counts totals the plan by action.
func (p *ImportPlan) Counts() map[ImportAction]int {
	out := map[ImportAction]int{}
	for _, it := range p.Items {
		out[it.Action]++
	}
	return out
}

// Writes reports how many hosts the plan would actually touch.
func (p *ImportPlan) Writes() int {
	c := p.Counts()
	return c[ImportAdd] + c[ImportUpdate]
}

// PlanImport reads a file and works out what importing it would do.
func (inv *Inventory) PlanImport(path string, opt importer.Options, overwrite bool) (*ImportPlan, error) {
	res, err := importer.Parse(path, opt)
	if err != nil {
		return nil, err
	}
	plan := &ImportPlan{
		Source:   res.Path,
		Format:   string(res.Format),
		Warnings: res.Warnings,
		Skipped:  res.Skipped,
	}

	// A file can name the same host twice; the first entry wins, and the rest
	// are reported rather than silently applied on top of each other.
	seen := map[string]bool{}

	for _, rec := range res.Records {
		item := ImportItem{Record: rec}
		key := strings.ToLower(rec.Name)

		switch {
		case seen[key]:
			item.Action, item.Reason = ImportReject, "named more than once in this file"
		case rec.HostName == "" && rec.ProxyJump == "":
			// Without an address ssh resolves the name itself, which is fine
			// when that name is real and useless when it came from a label in
			// a spreadsheet. Import it, but say so.
			item.Action = ImportAdd
			item.Reason = "no address; ssh will resolve the name itself"
		default:
			item.Action = ImportAdd
		}
		seen[key] = true

		if existing, found := inv.Host(rec.Name); found && item.Action != ImportReject {
			item.Changes = diffRecord(existing, rec)
			switch {
			case len(item.Changes) == 0:
				item.Action, item.Reason = ImportUnchanged, "already matches"
			case existing.ReadOnly:
				item.Action, item.Reason = ImportSkip, fmt.Sprintf("declared in %s, which tram does not manage", existing.File)
			case !overwrite:
				item.Action, item.Reason = ImportSkip, "already exists; pass --overwrite to update it"
			default:
				item.Action = ImportUpdate
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// diffRecord lists the fields an import would change on an existing host.
// Fields the file says nothing about are not changes: an inventory that omits
// a port is not asking for the port to be removed.
func diffRecord(cur model.Host, rec importer.Record) []string {
	var out []string
	cmp := func(field, from, to string) {
		if to != "" && from != to {
			out = append(out, fmt.Sprintf("%s %s -> %s", field, orDash(from), to))
		}
	}
	cmp("hostname", cur.HostName, rec.HostName)
	cmp("user", cur.User, rec.User)
	cmp("port", cur.Port, rec.Port)
	cmp("jump", cur.ProxyJump, rec.ProxyJump)
	cmp("group", cur.Group, rec.Group)
	cmp("desc", cur.Desc, rec.Desc)
	cmp("account", cur.Account, rec.Account)
	if rec.Key != "" {
		has := false
		for _, k := range cur.IdentityFiles {
			if samePath(k, rec.Key) {
				has = true
				break
			}
		}
		if !has {
			out = append(out, "key "+orDash(strings.Join(cur.IdentityFiles, ","))+" -> "+rec.Key)
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ApplyImport writes the adds and updates a plan decided on.
func (inv *Inventory) ApplyImport(plan *ImportPlan, force bool) (*Change, error) {
	ch := &Change{inv: inv}
	for _, item := range plan.Items {
		spec := specFrom(item.Record)
		var (
			sub *Change
			err error
		)
		switch item.Action {
		case ImportAdd:
			sub, err = inv.Add(spec)
		case ImportUpdate:
			sub, err = inv.Edit(spec.Name, spec, force)
		default:
			continue
		}
		if err != nil {
			ch.warn(item.Record.Name + ": " + err.Error())
			continue
		}
		for _, f := range sub.Files {
			ch.touch(f)
		}
		ch.warn(sub.Warnings...)
		ch.Summary = append(ch.Summary, sub.Summary...)
	}
	return ch, nil
}

// specFrom turns a record into an edit, setting only the fields the source
// actually carried.
func specFrom(rec importer.Record) Spec {
	s := Spec{Name: rec.Name}
	if rec.HostName != "" {
		s.HostName = Str(rec.HostName)
	}
	if rec.User != "" {
		s.User = Str(rec.User)
	}
	if rec.Port != "" {
		s.Port = Str(rec.Port)
	}
	if rec.ProxyJump != "" {
		s.ProxyJump = Str(rec.ProxyJump)
	}
	if rec.Group != "" {
		s.Group = Str(rec.Group)
	}
	if rec.Desc != "" {
		s.Desc = Str(rec.Desc)
	}
	if rec.Key != "" {
		s.SetIdentityFiles([]string{rec.Key})
	}
	if rec.Account != "" {
		s.Account = Str(rec.Account)
	}
	return s
}
