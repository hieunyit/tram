package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hieuny/tram/internal/importer"
	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/render"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var (
		group     string
		prefix    string
		format    string
		overwrite bool
		flat      bool
		set       importer.Overrides
	)
	c := &cobra.Command{
		Use:   "import <file>",
		Short: "Read hosts from an Ansible inventory or a CSV export",
		Long: `Read an inventory written for another tool and write its hosts into
ssh_config.

Three formats are read. The format is detected from the file unless --from
says otherwise:

  ansible-ini    the classic inventory: sections, host lines, key=value vars,
                 [g:vars] and [g:children], and host ranges like web[01:04]
  ansible-yaml   the YAML inventory, with nested children
  csv            a spreadsheet export with a header row

Only the Ansible variables that say how to reach a machine are read:
ansible_host, ansible_user, ansible_port, ansible_ssh_private_key_file, and a
jump host given as -J or -o ProxyJump= inside ansible_ssh_common_args.
Everything else in an inventory is about what Ansible does after it connects,
which is none of tram's business. A host with ansible_connection set to
anything but ssh is skipped and named, because ssh cannot be pointed at it.

Ansible's group nesting becomes tram's group path: a host in webservers, where
webservers is a child of prod, lands in prod/webservers. A host in several
groups keeps the most specific one, and the others are reported.

--group replaces all of that with one group of your own. --group-prefix keeps
the file's structure and nests it under yours instead, which is the right one
when the inventory's groups are worth having.

An inventory often names machines without saying how to log in to them. --user,
--key, --jump and --account set those on every host in one pass, so they do not
have to be edited in afterwards one at a time.

An existing host is left alone unless you pass --overwrite, and even then a
host outside tram's managed file needs --force as well.`,
		Args: cobra.ExactArgs(1),
		Example: `  tram import inventory.ini --dry-run
  tram import inventory.ini -g vpb-prod -u root -k ~/.ssh/id_ed25519
  tram import hosts.yml --group-prefix imported
  tram import servers.csv --overwrite`,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			plan, err := inv.PlanImport(args[0], importer.Options{
				Format:      importer.Format(format),
				Group:       group,
				GroupPrefix: prefix,
				FlatGroups:  flat,
				Set:         set,
			}, overwrite)
			if err != nil {
				return err
			}
			return runImport(app, inv, plan)
		},
	}
	f := c.Flags()
	f.StringVarP(&group, "group", "g", "", "put every imported host in this group, replacing the file's own")
	f.StringVar(&prefix, "group-prefix", "", "keep the file's groups and nest them under this one")
	f.StringVarP(&set.User, "user", "u", "", "set User on every imported host")
	f.StringVarP(&set.Key, "key", "k", "", "set IdentityFile on every imported host")
	f.StringVarP(&set.ProxyJump, "jump", "j", "", "set ProxyJump on every imported host")
	f.StringVarP(&set.Account, "account", "a", "", "link every imported host to this account")
	f.StringVar(&format, "from", "", "force the source format instead of detecting it: "+strings.Join(importer.Formats(), ", "))
	f.BoolVar(&overwrite, "overwrite", false, "update hosts that already exist")
	f.BoolVar(&flat, "flat-groups", false, "do not nest groups from Ansible children sections")
	_ = c.RegisterFlagCompletionFunc("from", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return importer.Formats(), cobra.ShellCompDirectiveNoFileComp
	})
	return c
}

func runImport(a *App, inv *inventory.Inventory, plan *inventory.ImportPlan) error {
	if a.Format.Machine() {
		g := &render.Grid{Columns: []string{"name", "action", "hostname", "user", "port", "group", "reason"}}
		for _, it := range plan.Items {
			r := it.Record
			g.Add(r.Name, string(it.Action), r.HostName, r.User, r.Port, r.Group, it.Reason)
		}
		if err := render.Out(os.Stdout, a.Format, plan, g); err != nil {
			return err
		}
	} else {
		printImportPlan(plan)
	}

	for _, w := range plan.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	for _, s := range plan.Skipped {
		fmt.Fprintln(os.Stderr, "skipped:", s)
	}

	if plan.Writes() == 0 {
		fmt.Fprintln(os.Stderr, "nothing to import")
		return nil
	}
	if a.DryRun {
		fmt.Fprintf(os.Stderr, "dry run: would write %d host(s), nothing was written\n", plan.Writes())
		return nil
	}

	ch, err := inv.ApplyImport(plan, a.Force)
	if err != nil {
		return err
	}
	c := plan.Counts()
	prompt := fmt.Sprintf("write %d new and %d changed host(s) from %s?",
		c[inventory.ImportAdd], c[inventory.ImportUpdate], plan.Source)
	return a.applyChange(ch, prompt)
}

func printImportPlan(plan *inventory.ImportPlan) {
	fmt.Printf("%s  (%s)\n\n", plan.Source, plan.Format)
	if len(plan.Items) == 0 {
		fmt.Println("  no hosts found")
		return
	}

	g := &render.Grid{Columns: []string{"", "name", "target", "group", "note"}}
	for _, it := range plan.Items {
		r := it.Record
		target := r.HostName
		if r.User != "" && target != "" {
			target = r.User + "@" + target
		}
		if r.Port != "" && r.Port != "22" {
			target += ":" + r.Port
		}
		note := it.Reason
		if len(it.Changes) > 0 {
			note = strings.Join(it.Changes, ", ")
		}
		g.Add(importMark(it.Action), r.Name, target, r.Group, note)
	}
	_ = render.WriteGrid(os.Stdout, g)

	fmt.Println()
	fmt.Println(strings.Join(planTally(plan), ", "))
}

// planTally counts the plan, mentioning only the outcomes that happened.
//
// The two kinds of "skipped" are named apart on purpose: a host tram left alone
// because it already exists is a different problem from a host ssh cannot be
// pointed at, and one line saying "skipped" for both tells you neither.
func planTally(plan *inventory.ImportPlan) []string {
	c := plan.Counts()
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(c[inventory.ImportAdd], "to add")
	add(c[inventory.ImportUpdate], "to update")
	add(c[inventory.ImportUnchanged], "already match")
	add(c[inventory.ImportSkip], "left alone")
	add(c[inventory.ImportReject], "rejected")
	add(len(plan.Skipped), "not reachable over ssh")
	if len(parts) == 0 {
		return []string{"nothing to do"}
	}
	return parts
}

// importMark is a one-character column that lets the eye find the rows that
// will actually change without reading the action word on every line.
func importMark(a inventory.ImportAction) string {
	switch a {
	case inventory.ImportAdd:
		return "+"
	case inventory.ImportUpdate:
		return "~"
	case inventory.ImportReject:
		return "!"
	case inventory.ImportUnchanged:
		return "="
	}
	return " "
}
