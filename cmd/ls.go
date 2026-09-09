package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/render"
	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	var (
		wide  bool
		tree  bool
		group string
		searc string
	)
	c := &cobra.Command{
		Use:     "ls [host]",
		Aliases: []string{"list"},
		Short:   "List hosts, or show one in detail",
		Long: `List the hosts declared in ssh_config and every file it includes.

With a host name, show that host in full, including the ProxyJump route
resolved end to end and the directives tram does not model.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				h, err := app.pickHost(args[0])
				if err != nil {
					return err
				}
				return showHost(inv, h)
			}
			hosts := inv.Select(inventory.Filter{Group: group, Search: searc})
			if tree {
				return showTree(hosts)
			}
			return showList(hosts, wide)
		},
	}
	f := c.Flags()
	f.BoolVar(&wide, "wide", false, "add the account column and the drift marker")
	f.BoolVar(&tree, "tree", false, "show hosts grouped as a tree")
	f.StringVarP(&group, "group", "g", "", "only hosts in this group, including nested groups")
	f.StringVarP(&searc, "search", "s", "", "only hosts matching this text")
	_ = c.RegisterFlagCompletionFunc("group", completeGroup)
	return c
}

func showList(hosts []model.Host, wide bool) error {
	cols := []string{"name", "target", "group", "jump"}
	if wide {
		cols = []string{"name", "target", "group", "jump", "account", "identity", "file"}
	}
	g := &render.Grid{Columns: cols, Empty: "no hosts found; run `tram add` or point -F at a config file"}

	for _, h := range hosts {
		mark := ""
		if h.Favorite {
			mark = star() + " "
		}
		if wide {
			acct := h.Account
			if h.Drift {
				// The asterisk says the stanza no longer matches the account it
				// is linked to, which is exactly when apply will leave it alone.
				acct += "*"
			}
			g.Add(mark+h.Name, h.Target(), h.Group, h.ProxyJump, acct,
				strings.Join(h.IdentityFiles, ","), shortPath(h.File))
			continue
		}
		g.Add(mark+h.Name, h.Target(), h.Group, h.ProxyJump)
	}
	return render.Out(os.Stdout, app.Format, hosts, g)
}

func showTree(hosts []model.Host) error {
	if app.Format.Machine() {
		return render.Out(os.Stdout, app.Format, hosts, nil)
	}
	root := &render.Tree{}
	for _, h := range hosts {
		node := root
		for _, seg := range h.GroupPath() {
			node = node.Child(seg)
		}
		label := h.Name
		if h.HostName != "" {
			label += "  " + h.Target()
		}
		node.Leaves = append(node.Leaves, label)
	}
	root.Sort()
	render.WriteTree(os.Stdout, root, app.ASCII)
	return nil
}

// hostDetail is the shape `ls <host> -f json` produces. Every field is present
// even when empty, so a consumer never has to distinguish absent from blank.
type hostDetail struct {
	model.Host
	Chain    model.JumpChain     `json:"chain"`
	Route    []string            `json:"route"`
	Managed  bool                `json:"managed"`
	Warnings []string            `json:"warnings"`
	Extra    map[string][]string `json:"extra"`
}

func showHost(inv *inventory.Inventory, h model.Host) error {
	chain := inv.Chain(h.Name)
	d := hostDetail{Host: h, Chain: chain, Managed: !h.ReadOnly, Extra: h.Other}
	if d.Extra == nil {
		d.Extra = map[string][]string{}
	}
	if d.Warnings == nil {
		d.Warnings = []string{}
	}
	for _, hop := range chain.Hops {
		d.Route = append(d.Route, hop.Spec)
	}
	d.Route = append(d.Route, h.Name)
	if len(chain.Cycle) > 0 {
		d.Warnings = append(d.Warnings, "ProxyJump loop: "+strings.Join(chain.Cycle, " -> ")+"; ssh would hang")
	}
	for _, u := range chain.Unknown {
		d.Warnings = append(d.Warnings, "ProxyJump names "+u+", which is not a configured host")
	}

	if app.Format.Machine() {
		return render.Out(os.Stdout, app.Format, d, nil)
	}

	w := os.Stdout
	fmt.Fprintf(w, "%s\n", h.Name)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-12s %s\n", k, v)
		}
	}
	row("aliases", strings.Join(h.Aliases, " "))
	row("hostname", h.Addr())
	row("user", h.User)
	row("port", h.PortOr())
	row("identity", strings.Join(h.IdentityFiles, ", "))
	row("group", h.Group)
	row("desc", h.Desc)
	if h.Account != "" {
		v := h.Account
		if h.Drift {
			v += "  (edited by hand; apply will not overwrite it)"
		}
		row("account", v)
	}
	if h.LastUsed > 0 {
		row("last used", time.Unix(h.LastUsed, 0).Format("2006-01-02 15:04"))
	}
	if h.Favorite {
		row("pinned", "yes")
	}
	row("file", fmt.Sprintf("%s:%d", h.File, h.Line))
	if h.ReadOnly {
		row("access", "read-only; pass --force to write to it")
	}

	if len(d.Route) > 1 {
		fmt.Fprintf(w, "  %-12s %s\n", "route", strings.Join(d.Route, " -> "))
		for _, hop := range chain.Hops {
			state := hop.Addr
			if !hop.Known {
				state = "not a configured host"
			}
			fmt.Fprintf(w, "    %-14s %s\n", hop.Spec, state)
		}
	}
	if len(h.Other) > 0 {
		keys := make([]string, 0, len(h.Other))
		for k := range h.Other {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintln(w, "  other")
		for _, k := range keys {
			for _, v := range h.Other[k] {
				fmt.Fprintf(w, "    %-14s %s\n", k, v)
			}
		}
	}
	for _, warn := range d.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", warn)
	}
	return nil
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func star() string {
	if app.ASCII {
		return "*"
	}
	return "★"
}
