package inventory

import (
	"fmt"

	"github.com/hieuny/tram/internal/account"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/sshconf"
)

// AccountPlans works out what applying an account would do to every host linked
// to it, without changing anything.
func (inv *Inventory) AccountPlans(name string, force bool) ([]account.Plan, error) {
	a, ok := inv.Store.Account(name)
	if !ok {
		return nil, fmt.Errorf("no account named %q", name)
	}
	var plans []account.Plan
	for _, h := range inv.hosts {
		if h.Account == "" || !equalFold(h.Account, name) {
			continue
		}
		p := account.PlanFor(a, h)
		if force && p.Action == "skip-readonly" {
			p = account.PlanFor(a, withWritable(h))
		}
		plans = append(plans, p)
	}
	return plans, nil
}

func withWritable(h model.Host) model.Host {
	h.ReadOnly = false
	return h
}

// ApplyAccount writes an account's identity into every host linked to it that
// has not been edited by hand.
func (inv *Inventory) ApplyAccount(name string, force bool) (*Change, []account.Plan, error) {
	plans, err := inv.AccountPlans(name, force)
	if err != nil {
		return nil, nil, err
	}
	a, _ := inv.Store.Account(name)
	user, keys := a.Materialized()

	ch := &Change{inv: inv}
	for _, p := range plans {
		if !p.Planned() {
			if p.Reason != "" {
				ch.Warnings = append(ch.Warnings, p.Host+": "+p.Reason)
			}
			continue
		}
		b := inv.Block(p.Host)
		if b == nil {
			continue
		}
		var set []sshconf.Directive
		var unset []string
		if user != "" {
			set = append(set, sshconf.D("User", user))
		} else {
			unset = append(unset, "User")
		}
		if len(keys) > 0 {
			for _, k := range keys {
				set = append(set, sshconf.D("IdentityFile", k))
			}
		}
		sshconf.SetDirectives(b, set, unset)
		ch.touch(b.File)
		ch.Summary = append(ch.Summary, "apply "+name+" to "+p.Host)
	}
	return ch, plans, nil
}

// RenameAccountLinks follows an account rename through every link.
func (inv *Inventory) RenameAccountLinks(oldName, newName string) error {
	for _, h := range inv.hosts {
		if equalFold(h.Account, oldName) {
			if err := inv.Store.SetLink(h.Name, newName); err != nil {
				return err
			}
		}
	}
	return nil
}

// UnlinkAccount clears the link on every host using an account, leaving the
// stanzas untouched. The User and IdentityFile the account wrote stay where
// they are, so nothing stops working.
func (inv *Inventory) UnlinkAccount(name string) (int, error) {
	n := 0
	for _, h := range inv.hosts {
		if equalFold(h.Account, name) {
			if err := inv.Store.SetLink(h.Name, ""); err != nil {
				return n, err
			}
			n++
		}
	}
	inv.rebuild()
	return n, nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
