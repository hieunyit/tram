package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/store"
	"github.com/spf13/cobra"
)

// hostFlags collects the flags shared by add, edit and clone. Whether a flag
// was given matters as much as its value: an absent flag leaves the current
// setting alone, while an explicitly empty one removes it.
type hostFlags struct {
	addr    string
	user    string
	port    string
	jump    string
	group   string
	desc    string
	account string
	keys    []string
	aliases []string
}

func (hf *hostFlags) bind(c *cobra.Command) {
	f := c.Flags()
	f.StringVar(&hf.addr, "addr", "", "address ssh connects to, written as HostName")
	f.StringVarP(&hf.user, "user", "u", "", "login name, written as User")
	f.StringVarP(&hf.port, "port", "p", "", "port, written as Port")
	f.StringVarP(&hf.jump, "jump", "j", "", "jump station, written as ProxyJump")
	f.StringVarP(&hf.group, "group", "g", "", "group, kept as a tram comment inside the stanza")
	f.StringVar(&hf.desc, "desc", "", "description, kept as a tram comment inside the stanza")
	f.StringVarP(&hf.account, "account", "a", "", `identity to link; "" unlinks without changing the stanza`)
	f.StringArrayVarP(&hf.keys, "key", "k", nil, "private key, written as IdentityFile; repeat for several")
	f.StringArrayVar(&hf.aliases, "alias", nil, "extra name on the Host line; repeat for several")
	_ = c.RegisterFlagCompletionFunc("group", completeGroup)
	_ = c.RegisterFlagCompletionFunc("jump", completeHost)
	_ = c.RegisterFlagCompletionFunc("account", completeAccount)
}

// spec turns the flags into a Spec, setting only the fields the user named.
func (hf *hostFlags) spec(c *cobra.Command, name string) inventory.Spec {
	s := inventory.Spec{Name: name, Aliases: hf.aliases}
	f := c.Flags()
	if f.Changed("addr") {
		s.HostName = inventory.Str(hf.addr)
	}
	if f.Changed("user") {
		s.User = inventory.Str(hf.user)
	}
	if f.Changed("port") {
		s.Port = inventory.Str(hf.port)
	}
	if f.Changed("jump") {
		s.ProxyJump = inventory.Str(hf.jump)
	}
	if f.Changed("group") {
		s.Group = inventory.Str(hf.group)
	}
	if f.Changed("desc") {
		s.Desc = inventory.Str(hf.desc)
	}
	if f.Changed("account") {
		s.Account = inventory.Str(hf.account)
	}
	if f.Changed("key") {
		s.SetIdentityFiles(hf.keys)
	}
	return s
}

func newAddCmd() *cobra.Command {
	var hf hostFlags
	c := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a host",
		Long: `Write a new Host stanza.

The stanza goes into tram's own file when init has created one, and into
ssh_config itself otherwise. Either way ssh reads it: nothing is stored in a
private database of tram's own.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			s := hf.spec(c, args[0])
			if s.HostName == nil && s.ProxyJump == nil {
				fmt.Fprintf(os.Stderr, "note: no --addr given, so ssh will resolve the name %q itself\n", args[0])
			}
			ch, err := inv.Add(s)
			if err != nil {
				return err
			}
			return app.applyChange(ch, "")
		},
	}
	hf.bind(c)
	return c
}

func newEditCmd() *cobra.Command {
	var hf hostFlags
	c := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a host",
		Long: `Change one or more settings on an existing host.

Only the settings you name are touched. Comments, unknown directives and the
order of everything else in the stanza are left exactly as they were.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			h, err := app.pickHost(args[0])
			if err != nil {
				return err
			}
			ch, err := inv.Edit(h.Name, hf.spec(c, h.Name), app.Force)
			if err != nil {
				return err
			}
			return app.applyChange(ch, "")
		},
	}
	hf.bind(c)
	return c
}

func newCloneCmd() *cobra.Command {
	var hf hostFlags
	c := &cobra.Command{
		Use:               "clone <source> <new-name>",
		Short:             "Copy a host under a new name",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			src, err := app.pickHost(args[0])
			if err != nil {
				return err
			}
			ch, err := inv.Clone(src.Name, args[1], hf.spec(c, args[1]))
			if err != nil {
				return err
			}
			return app.applyChange(ch, "")
		},
	}
	hf.bind(c)
	return c
}

func newRmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "rm <name>...",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove hosts",
		Long: `Delete a Host stanza and everything inside it.

Comments and unrecognised directives that live in the stanza go with it, rather
than floating to the top of the file where they would become global settings.

If the host is a jump station for others, tram names those hosts before it
deletes anything.`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			for _, name := range args {
				h, err := app.pickHost(name)
				if err != nil {
					return err
				}
				ch, err := inv.Remove(h.Name, app.Force)
				if err != nil {
					return err
				}
				if err := app.applyChange(ch, "remove "+h.Name+"?"); err != nil {
					return err
				}
				if inv, err = app.Inventory(); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return c
}

func newMvCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "mv <old> <new>",
		Aliases: []string{"rename"},
		Short:   "Rename a host and update every ProxyJump that names it",
		Long: `Rename a host.

Every other host whose ProxyJump points at the old name is updated in the same
change, across every file in the tree. This is the operation most likely to
break a configuration, so the diff is always shown and always confirmed.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			h, err := app.pickHost(args[0])
			if err != nil {
				return err
			}
			ch, err := inv.Rename(h.Name, args[1], app.Force)
			if err != nil {
				return err
			}
			// A rename always asks, even with a single-file change, because a
			// mistake here silently breaks every route through the host.
			return app.applyChange(ch, fmt.Sprintf("rename %s to %s and update %d other stanza(s)?", h.Name, args[1], len(ch.Summary)-1))
		},
	}
	return c
}

func newInitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "init",
		Short: "Give tram its own file and make ssh read it",
		Long: `Create ~/.ssh/config.d/tram.conf and add an Include for it at the very top
of ssh_config.

The Include goes first because ssh keeps the first value it sees for most
keywords: placed below a "Host *" stanza it would be shadowed for every host.

After init, hosts you keep in other files become read-only to tram. They are
still listed, still connectable and still usable with exec; writing to one
needs --force.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			res, err := inv.Init(app.SSHDir)
			if err != nil {
				return err
			}
			if res.Created {
				fmt.Fprintf(os.Stderr, "created %s\n", res.ManagedPath)
			}
			if res.Change == nil {
				fmt.Fprintf(os.Stderr, "%s already includes %s\n", store.ConfigPath(), store.ManagedName)
				return nil
			}
			if err := app.applyChange(res.Change, ""); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "tram now writes new hosts to %s\n", res.ManagedPath)
			return nil
		},
	}
	return c
}

func newArgsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "args <host> [-- command...]",
		Short: "Print the ssh command line tram would run",
		Long: `Print the argv tram would hand to ssh, for pasting into a script or a
launcher of your own.

Nothing is executed and nothing is recorded.`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			var remote []string
			if d := c.ArgsLenAtDash(); d >= 0 {
				remote = args[d:]
				args = args[:d]
			}
			if len(args) != 1 {
				return fmt.Errorf("expected one host name")
			}
			h, err := app.pickHost(args[0])
			if err != nil {
				return err
			}
			argv := buildRequest(app, h.Name, remote, connectFlags).Argv()
			if app.Format.Machine() {
				return renderArgv(argv)
			}
			fmt.Println(strings.Join(argv, " "))
			return nil
		},
	}
	return c
}

func newSFTPCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "sftp <host>",
		Short: "Open sftp against a host",
		Long: `Hand the host to the system sftp client.

tram does not draw a file browser. sftp already exists, reads the same
ssh_config and knows the same jump hosts.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			h, err := app.pickHost(args[0])
			if err != nil {
				return err
			}
			return runSFTP(app, h.Name)
		},
	}
	return c
}

func newCompletionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:       "completion <bash|zsh|fish|powershell>",
		Short:     "Print a shell completion script",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(c *cobra.Command, args []string) error {
			root := c.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return fmt.Errorf("unknown shell %q", args[0])
		},
	}
	return c
}
