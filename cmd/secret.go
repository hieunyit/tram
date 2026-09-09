package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hieuny/tram/internal/launcher"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/render"
	"github.com/hieuny/tram/internal/secret"
	"github.com/hieuny/tram/internal/store"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// readSecret takes a value from the terminal without echoing it, and refuses to
// take one from a pipe. A password typed into a shell command ends up in the
// history file and in the process table, so tram does not offer that route.
func readSecret(prompt string) (string, error) {
	if !isTerminal(os.Stdin) {
		return "", fmt.Errorf("a secret must be typed at a terminal, not piped")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("nothing entered")
	}
	return v, nil
}

func newSecretCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "secret",
		Short: "Store passwords in the operating system's keyring",
		Long: `Keep a password where the operating system keeps such things.

The value never reaches ssh_config, never appears on a command line and never
sits in an environment variable. ssh receives it by calling tram back as its
askpass helper, over a pipe, at the moment it is needed.

A password belongs to an identity rather than to a machine, so prefer
"tram secret set <account>" over setting one per host.`,
	}
	c.AddCommand(secretSet(), secretLs(), secretRm())
	return c
}

// subjectFor decides whether a name refers to an account or to a host, so that
// the user does not have to say which.
func subjectFor(a *App, name string) (secret.Subject, string, error) {
	st := store.Load()
	if _, ok := st.Account(name); ok {
		return secret.PasswordFor("account", name), "account " + name, nil
	}
	inv, err := a.Inventory()
	if err != nil {
		return "", "", err
	}
	if h, ok := inv.Host(name); ok {
		return secret.PasswordFor("host", h.Name), "host " + h.Name, nil
	}
	return "", "", fmt.Errorf("%q is neither an account nor a host", name)
}

func secretSet() *cobra.Command {
	return &cobra.Command{
		Use:   "set <account|host>",
		Short: "Store a password",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sub, label, err := subjectFor(app, args[0])
			if err != nil {
				return err
			}

			// Warn before storing rather than at connection time. On an ssh
			// older than 8.4 the askpass helper is ignored whenever ssh has a
			// terminal, which is always the case for an interactive session, so
			// the stored password would simply never be used.
			if ok, version := secret.SupportsAskpassRequire(); !ok {
				fmt.Fprintf(os.Stderr,
					"warning: your ssh is %s, which has no SSH_ASKPASS_REQUIRE.\n"+
						"         It will prompt on the terminal and ignore a stored password.\n"+
						"         Use a key instead, or upgrade to OpenSSH 8.4 or newer.\n", version)
				if !app.confirm("store it anyway?") {
					return fmt.Errorf("cancelled")
				}
			}

			v, err := readSecret("Password for " + label + ": ")
			if err != nil {
				return err
			}
			again, err := readSecret("Again: ")
			if err != nil {
				return err
			}
			if v != again {
				return fmt.Errorf("the two entries do not match")
			}

			st := secret.New(store.Dir())
			if err := st.Set(sub, v); err != nil {
				return err
			}
			backend := st.Backend()
			if err := st.Remember(sub, backend); err != nil {
				fmt.Fprintln(os.Stderr, "warning: could not record the subject for `secret ls`:", err)
			}
			fmt.Fprintf(os.Stderr, "stored password for %s in %s\n", label, backend)
			return nil
		},
		ValidArgsFunction: func(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			accounts, _ := completeAccount(c, args, toComplete)
			hosts, _ := completeHost(c, args, toComplete)
			return append(accounts, hosts...), cobra.ShellCompDirectiveNoFileComp
		},
	}
}

func secretLs() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List what secrets are stored, never their values",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			st := secret.New(store.Dir())
			subs, err := st.List()
			if err != nil {
				return err
			}
			type row struct {
				Subject string `json:"subject"`
				Kind    string `json:"kind"`
				Where   string `json:"where"`
			}
			var rows []row
			g := &render.Grid{Columns: []string{"subject", "kind", "stored in"}, Empty: "no secrets stored"}
			for _, s := range subs {
				rows = append(rows, row{s.Label(), s.Kind(), st.Where(s)})
				g.Add(s.Label(), s.Kind(), st.Where(s))
			}
			return render.Out(os.Stdout, app.Format, rows, g)
		},
	}
}

func secretRm() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <account|host>",
		Short: "Delete a stored password",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sub, label, err := subjectFor(app, args[0])
			if err != nil {
				return err
			}
			st := secret.New(store.Dir())
			if err := st.Delete(sub); err != nil {
				return fmt.Errorf("no password stored for %s", label)
			}
			_ = st.Forget(sub)
			fmt.Fprintf(os.Stderr, "removed the password for %s\n", label)
			return nil
		},
	}
}

func newKeyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "key",
		Short: "Work with private keys and the ssh agent",
	}
	c.AddCommand(keyLoad(), keyUnload(), keyList(), keyPassphrase(), keyPush())
	return c
}

// keyPathFor accepts either a host name, in which case its first IdentityFile
// is used, or a path.
func keyPathFor(a *App, arg string) (string, error) {
	inv, err := a.Inventory()
	if err == nil {
		if h, ok := inv.Host(arg); ok {
			if len(h.IdentityFiles) == 0 {
				return "", fmt.Errorf("%s has no IdentityFile", h.Name)
			}
			return h.IdentityFiles[0], nil
		}
	}
	return arg, nil
}

func keyLoad() *cobra.Command {
	var lifetime string
	c := &cobra.Command{
		Use:   "load <host|path>",
		Short: "Add a key to the ssh agent, using a stored passphrase",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			path, err := keyPathFor(app, args[0])
			if err != nil {
				return err
			}
			sub := secret.PassphraseFor(path)
			if err := secret.AgentAdd(path, lifetime, launcherSelf(), sub); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "loaded %s\n", secret.ExpandKeyPath(path))
			return nil
		},
		ValidArgsFunction: completeHost,
	}
	c.Flags().StringVarP(&lifetime, "lifetime", "t", "", "forget the key after this long, for example 8h")
	return c
}

func keyUnload() *cobra.Command {
	return &cobra.Command{
		Use:               "unload <host|path>",
		Short:             "Remove a key from the ssh agent",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			path, err := keyPathFor(app, args[0])
			if err != nil {
				return err
			}
			return secret.AgentRemove(path)
		},
	}
}

func keyList() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Show the keys the agent holds",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			list, err := secret.AgentList()
			if err != nil {
				return err
			}
			g := &render.Grid{Columns: []string{"key"}, Empty: "the agent holds no keys"}
			for _, l := range list {
				g.Add(l)
			}
			return render.Out(os.Stdout, app.Format, list, g)
		},
	}
}

func keyPassphrase() *cobra.Command {
	c := &cobra.Command{Use: "passphrase", Short: "Store key passphrases"}

	c.AddCommand(&cobra.Command{
		Use:   "set <host|path>",
		Short: "Store a key's passphrase, after checking it opens the key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := keyPathFor(app, args[0])
			if err != nil {
				return err
			}
			info, err := secret.InspectKey(path)
			if err != nil {
				return err
			}
			if !info.Encrypted {
				return fmt.Errorf("%s is not passphrase protected", info.Path)
			}
			v, err := readSecret("Passphrase for " + info.Path + ": ")
			if err != nil {
				return err
			}
			// Verify before storing. An unverified passphrase fails later, at
			// connection time, where it looks like a server problem.
			if err := secret.VerifyPassphrase(path, v); err != nil {
				return err
			}
			st := secret.New(store.Dir())
			sub := secret.PassphraseFor(path)
			if err := st.Set(sub, v); err != nil {
				return err
			}
			_ = st.Remember(sub, st.Backend())
			fmt.Fprintf(os.Stderr, "verified and stored the passphrase for %s\n", info.Path)
			return nil
		},
		ValidArgsFunction: completeHost,
	})

	c.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List the keys with a stored passphrase",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			st := secret.New(store.Dir())
			subs, _ := st.List()
			g := &render.Grid{Columns: []string{"key"}, Empty: "no passphrases stored"}
			var rows []string
			for _, s := range subs {
				if s.Kind() == "passphrase" {
					rows = append(rows, s.Label())
					g.Add(s.Label())
				}
			}
			return render.Out(os.Stdout, app.Format, rows, g)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "rm <host|path>",
		Short: "Delete a stored passphrase",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := keyPathFor(app, args[0])
			if err != nil {
				return err
			}
			st := secret.New(store.Dir())
			sub := secret.PassphraseFor(path)
			if err := st.Delete(sub); err != nil {
				return fmt.Errorf("no passphrase stored for %s", secret.ExpandKeyPath(path))
			}
			_ = st.Forget(sub)
			return nil
		},
	})
	return c
}

func keyPush() *cobra.Command {
	var ff fleetFlags
	var keyPath string
	c := &cobra.Command{
		Use:   "push [host...]",
		Short: "Install a public key on hosts, skipping the ones that have it",
		Long: `Append a public key to authorized_keys on each selected host.

The operation is idempotent: a host that already has the key is reported as
unchanged rather than getting a second copy.`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			hosts, err := ff.hosts(app, args)
			if err != nil {
				return err
			}
			pub, err := readPublicKey(keyPath)
			if err != nil {
				return err
			}
			if !app.confirmFleet(hosts, "install a public key") {
				return fmt.Errorf("cancelled")
			}
			return runExec(app, hosts, []string{pushScript(pub)}, ff)
		},
	}
	ff.bind(c)
	c.Flags().StringVarP(&keyPath, "key", "k", "", "public key to install; defaults to ~/.ssh/id_ed25519.pub then id_rsa.pub")
	return c
}

// readPublicKey finds the public key to install.
func readPublicKey(path string) (string, error) {
	candidates := []string{path}
	if path == "" {
		home, _ := os.UserHomeDir()
		candidates = []string{
			home + "/.ssh/id_ed25519.pub",
			home + "/.ssh/id_ecdsa.pub",
			home + "/.ssh/id_rsa.pub",
		}
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		p := secret.ExpandKeyPath(c)
		if !strings.HasSuffix(p, ".pub") {
			p += ".pub"
		}
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	return "", fmt.Errorf("no public key found; pass --key")
}

// pushScript appends the key only when it is not already there, so running the
// command twice leaves one copy rather than two.
func pushScript(pub string) string {
	esc := strings.ReplaceAll(pub, "'", `'"'"'`)
	return "k='" + esc + "'; " +
		"mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && " +
		`if grep -qxF "$k" ~/.ssh/authorized_keys; then echo unchanged; else printf '%s\n' "$k" >> ~/.ssh/authorized_keys && echo installed; fi`
}

func launcherSelf() string { return launcher.SelfPath() }

var _ = model.Host{}
