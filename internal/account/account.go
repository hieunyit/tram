// Package account materializes an identity into host stanzas and reports when
// a stanza has drifted away from the identity it was written from.
//
// The relationship is one way. An account writes User and IdentityFile into a
// stanza once; it never stands between ssh_config and ssh at read time. That is
// what keeps "delete tram and ssh web1 still works" true.
package account

import (
	"fmt"

	"github.com/hieuny/tram/internal/model"
)

// Plan is what applying an account would do to one host.
type Plan struct {
	Host string `json:"host"`
	// Action is "write", "skip-drift", "skip-readonly" or "unchanged".
	Action string `json:"action"`
	// Reason explains a skip in words a user can act on.
	Reason string `json:"reason"`

	FromUser string `json:"from_user"`
	ToUser   string `json:"to_user"`
	FromKeys string `json:"from_keys"`
	ToKeys   string `json:"to_keys"`
}

// Planned reports whether the plan would change anything.
func (p Plan) Planned() bool { return p.Action == "write" }

// PlanFor works out what applying an account to a host would do.
//
// A host that has been edited by hand is left alone and said so. Silently
// reverting somebody's manual change is the fastest way to make a tool
// untrustworthy, and the drift marker exists precisely so this decision can be
// made without guessing.
func PlanFor(a model.Account, h model.Host) Plan {
	user, keys := a.Materialized()
	p := Plan{
		Host:     h.Name,
		FromUser: h.User,
		ToUser:   user,
		FromKeys: join(h.IdentityFiles),
		ToKeys:   join(keys),
	}
	switch {
	case h.ReadOnly:
		p.Action = "skip-readonly"
		p.Reason = fmt.Sprintf("declared in %s, which tram does not manage; use --force", h.File)
	case h.Drift:
		p.Action = "skip-drift"
		p.Reason = "the stanza was edited by hand since it was linked; apply leaves it as it is"
	case p.FromUser == p.ToUser && (len(keys) == 0 || p.FromKeys == p.ToKeys):
		p.Action = "unchanged"
	default:
		p.Action = "write"
	}
	return p
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
