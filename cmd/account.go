package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/render"
	"github.com/hieuny/tram/internal/secret"
	"github.com/hieuny/tram/internal/store"
	"github.com/spf13/cobra"
)

func completeAccount(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	st := store.Load()
	var out []string
	for _, a := range st.AccountList() {
		out = append(out, a.Name+"\t"+a.User)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func newAccountCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "account",
		Aliases: []string{"acct"},
		Short:   "Manage identities",
		Long: `An account is an identity: who you log in as and how you prove it.

It holds a user name, an authentication method and a key path. It holds no
address, no port and no jump host, because those belong to the machine rather
than to you. Keeping that line is what stops the model from becoming a second,
worse copy of ssh_config.

Linking a host to an account writes User and IdentityFile into that host's
stanza once. ssh then reads them the way it reads anything else, so removing
tram changes nothing.`,
	}
	c.AddCommand(accountLs(), accountAdd(), accountEdit(), accountApply(), accountRm())
	return c
}

func accountLs() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List accounts with the number of hosts linked to each",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			list := inv.Store.AccountList()
			type row struct {
				model.Account
				Hosts   int `json:"hosts"`
				Drifted int `json:"drifted"`
			}
			var rows []row
			g := &render.Grid{
				Columns:    []string{"name", "user", "auth", "key", "hosts", "drifted"},
				RightAlign: map[int]bool{4: true, 5: true},
				Empty:      "no accounts; add one with `tram account add`",
			}
			for _, a := range list {
				n, drift := 0, 0
				for _, h := range inv.Hosts() {
					if strings.EqualFold(h.Account, a.Name) {
						n++
						if h.Drift {
							drift++
						}
					}
				}
				rows = append(rows, row{a, n, drift})
				g.Add(a.Name, a.User, string(a.Auth), a.KeyPath, fmt.Sprint(n), fmt.Sprint(drift))
			}
			return render.Out(os.Stdout, app.Format, rows, g)
		},
	}
}

func accountAdd() *cobra.Command {
	var user, key, auth, desc string
	c := &cobra.Command{
		Use:   "add <name>",
		Short: "Create an identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a := model.Account{Name: args[0], User: user, KeyPath: key, Desc: desc, Auth: model.AuthMethod(auth)}
			if !c.Flags().Changed("auth") {
				a.Auth = model.AuthKey
				if key == "" {
					a.Auth = model.AuthAgent
				}
			}
			if err := a.Valid(); err != nil {
				return err
			}
			if a.Auth == model.AuthKey {
				info, err := secret.InspectKey(a.KeyPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				} else if info.Encrypted {
					fmt.Fprintf(os.Stderr, "note: %s is passphrase protected; store it with `tram key passphrase set %s`\n", info.Path, a.KeyPath)
				}
			}
			st := store.Load()
			if err := st.PutAccount(a); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "created account %s\n", a.Name)
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&user, "user", "u", "", "login name")
	f.StringVarP(&key, "key", "k", "", "private key path")
	f.StringVar(&auth, "auth", "", "key, password or agent")
	f.StringVar(&desc, "desc", "", "what this identity is for")
	return c
}

func accountEdit() *cobra.Command {
	var user, key, auth, desc string
	var apply bool
	c := &cobra.Command{
		Use:               "edit <name>",
		Short:             "Change an identity",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAccount,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			a, ok := inv.Store.Account(args[0])
			if !ok {
				return fmt.Errorf("no account named %q", args[0])
			}
			f := c.Flags()
			if f.Changed("user") {
				a.User = user
			}
			if f.Changed("key") {
				a.KeyPath = key
			}
			if f.Changed("auth") {
				a.Auth = model.AuthMethod(auth)
			}
			if f.Changed("desc") {
				a.Desc = desc
			}
			if err := a.Valid(); err != nil {
				return err
			}
			if err := inv.Store.PutAccount(a); err != nil {
				return err
			}
			if !apply {
				n := len(inv.Store.LinkedHosts(a.Name))
				if n > 0 {
					fmt.Fprintf(os.Stderr, "%d host(s) still carry the old values; run `tram account apply %s` to update them\n", n, a.Name)
				}
				return nil
			}
			return runAccountApply(app, a.Name)
		},
	}
	f := c.Flags()
	f.StringVarP(&user, "user", "u", "", "login name")
	f.StringVarP(&key, "key", "k", "", "private key path")
	f.StringVar(&auth, "auth", "", "key, password or agent")
	f.StringVar(&desc, "desc", "", "what this identity is for")
	f.BoolVar(&apply, "apply", false, "write the new values into every linked host")
	return c
}

func accountApply() *cobra.Command {
	return &cobra.Command{
		Use:   "apply <name>",
		Short: "Write an identity into every host linked to it",
		Long: `Write the account's User and IdentityFile into each linked host.

Hosts that have been edited by hand since they were linked are marked as
drifted and left exactly as they are. tram does not revert your edits.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAccount,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			return runAccountApply(app, args[0])
		},
	}
}

func runAccountApply(a *App, name string) error {
	inv, err := a.Inventory()
	if err != nil {
		return err
	}
	ch, plans, err := inv.ApplyAccount(name, a.Force)
	if err != nil {
		return err
	}
	if a.Format.Machine() {
		g := &render.Grid{Columns: []string{"host", "action", "reason", "to_user", "to_keys"}}
		for _, p := range plans {
			g.Add(p.Host, p.Action, p.Reason, p.ToUser, p.ToKeys)
		}
		if err := render.Out(os.Stdout, a.Format, plans, g); err != nil {
			return err
		}
	} else {
		for _, p := range plans {
			switch p.Action {
			case "write":
				fmt.Printf("  %-24s %s -> %s\n", p.Host, orDash(p.FromUser), orDash(p.ToUser))
			case "unchanged":
			default:
				fmt.Printf("  %-24s %s: %s\n", p.Host, p.Action, p.Reason)
			}
		}
	}
	return a.applyChange(ch, "")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func accountRm() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete an identity",
		Long: `Delete an account and unlink every host that used it.

The hosts keep the User and IdentityFile the account had written, so nothing
stops connecting. Only the label goes away.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAccount,
		RunE: func(c *cobra.Command, args []string) error {
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			if _, ok := inv.Store.Account(args[0]); !ok {
				return fmt.Errorf("no account named %q", args[0])
			}
			linked := inv.Store.LinkedHosts(args[0])
			if len(linked) > 0 && !app.Force && !app.Yes {
				fmt.Fprintf(os.Stderr, "%d host(s) are linked: %s\n", len(linked), strings.Join(linked, ", "))
				fmt.Fprintln(os.Stderr, "their stanzas keep the values the account wrote; only the link is removed")
				if !app.confirm("delete " + args[0] + "?") {
					return fmt.Errorf("cancelled")
				}
			}
			n, err := inv.UnlinkAccount(args[0])
			if err != nil {
				return err
			}
			if err := inv.Store.DeleteAccount(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "deleted account %s and unlinked %d host(s)\n", args[0], n)
			return nil
		},
	}
}
