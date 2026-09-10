package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/render"
	"github.com/hieuny/tram/internal/secret"
	"github.com/spf13/cobra"
)

func newKeyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "key",
		Short: "Work with private keys",
		Long: `Install a public key on hosts, and look at the keys a host would use.

There is nothing here for remembering a passphrase, because there is nothing to
remember: type it once when ssh asks and tram reuses it for the rest of the
run. See "tram help" for the whole of it.`,
	}
	c.AddCommand(keyPush(), keyShow())
	return c
}

// keyShow reports what tram knows about a host's keys, which is the question
// behind "why is it asking me for a passphrase again".
func keyShow() *cobra.Command {
	return &cobra.Command{
		Use:               "ls [host]",
		Short:             "Show the keys a host would use, and which are passphrase protected",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			hosts := inv.Hosts()
			if len(args) == 1 {
				h, err := app.pickHost(args[0])
				if err != nil {
					return err
				}
				hosts = []model.Host{h}
			}

			type row struct {
				Host      string `json:"host"`
				Key       string `json:"key"`
				Exists    bool   `json:"exists"`
				Encrypted bool   `json:"encrypted"`
				Type      string `json:"type"`
			}
			var rows []row
			g := &render.Grid{
				Columns: []string{"host", "key", "type", "passphrase"},
				Empty:   "no host declares an IdentityFile",
			}
			for _, h := range hosts {
				for _, k := range h.IdentityFiles {
					info, err := secret.InspectKey(k)
					state := "no"
					switch {
					case err != nil && !info.Exists:
						state = "missing"
					case err != nil:
						state = "unreadable"
					case info.Encrypted:
						state = "yes"
					}
					rows = append(rows, row{h.Name, k, info.Exists, info.Encrypted, info.Type})
					g.Add(h.Name, k, info.Type, state)
				}
			}
			return render.Out(os.Stdout, app.Format, rows, g)
		},
	}
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
