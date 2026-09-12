// Package cmd defines tram's command line. Each file holds one command and no
// logic: the work lives in the internal packages, so the same behaviour is
// reachable from the TUI without going through a second implementation.
package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/render"
	"github.com/hieuny/tram/internal/secret"
	"github.com/hieuny/tram/internal/store"
	"github.com/spf13/cobra"
)

// Version is set at build time.
var Version = "0.1.0-dev"

// App carries the state every command shares.
type App struct {
	ConfigPath string
	SSHDir     string
	Format     render.Format
	FormatFlag string

	Yes    bool
	DryRun bool
	Diff   bool
	Force  bool
	ASCII  bool

	// Note carries something worth saying that happened during a session. The
	// interface reads it afterwards, because otherwise the line would be
	// painted over before anyone could read it.
	Note string

	inv  *inventory.Inventory
	sess *secret.Session
}

// Session returns the environment a child needs to reach this run's passphrase
// cache, creating the cache on first use.
//
// It is one run of tram, not one connection: that is what lets the second host
// sharing a key file connect without asking. CloseSession ends it.
func (a *App) Session() []string {
	if a.sess == nil {
		s, err := secret.OpenSession(store.Dir())
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not start a passphrase cache:", err)
			return nil
		}
		a.sess = s
	}
	return a.sess.Env()
}

// Secrets returns this run's passphrase cache, creating it on first use.
//
// The interface needs it for one thing: to put a passphrase in before opening a
// connection, so that ssh finds the answer waiting rather than stopping to ask
// on a screen tram is drawing on.
func (a *App) Secrets() *secret.Session {
	if a.sess == nil {
		a.Session()
	}
	return a.sess
}

// CloseSession forgets every passphrase typed during this run.
func (a *App) CloseSession() {
	a.sess.Close()
	a.sess = nil
}

var app = &App{}

// Inventory loads the configuration tree once and reuses it.
func (a *App) Inventory() (*inventory.Inventory, error) {
	if a.inv != nil {
		return a.inv, nil
	}
	inv, err := inventory.Load(inventory.Options{ConfigPath: a.ConfigPath, SSHDir: a.SSHDir})
	if err != nil {
		return nil, err
	}
	for _, e := range inv.Errors() {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
	if inv.Store.Options.ASCII {
		a.ASCII = true
	}
	a.inv = inv
	return inv, nil
}

// Reload drops the cached tree, used after a write.
func (a *App) Reload() { a.inv = nil }

// EffectiveConfig is the ssh_config tram is actually reading, whether that came
// from -F, from an environment variable, or from the default location.
func (a *App) EffectiveConfig() string {
	if a.ConfigPath != "" {
		return a.ConfigPath
	}
	return store.ConfigPath()
}

// SSHConfigArg returns the path to pass to ssh with -F, or "" when ssh would
// read the same file on its own.
//
// The distinction matters: -F tells ssh to ignore the system-wide
// configuration entirely, so passing it when it is not needed would quietly
// drop settings an administrator put in /etc/ssh/ssh_config. tram passes it
// only when it is reading somewhere other than ssh's default, which is the one
// case where not passing it would make the two disagree about what a host is.
func (a *App) SSHConfigArg() string {
	eff := a.EffectiveConfig()
	if sameFile(eff, defaultConfigPath()) {
		return ""
	}
	return eff
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ExitCode is an error carrying the status tram should exit with, so that a
// script can tell a failed connection from a failed remote command.
type ExitCode struct {
	Code int
	Err  error
}

func (e ExitCode) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

// Root builds the command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "tram [host]",
		Short: "Your ssh_config, with eyes",
		Long: `tram is a viewer and safe editor for ~/.ssh/config, plus a handful of
commands that run in parallel across many hosts.

It is not a terminal emulator, not a private connection store, not a daemon and
not a sync service. Delete tram and "ssh web1" still works, because ssh_config
is the only place tram keeps anything.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			if len(args) == 0 {
				return runTUI(app)
			}
			var remote []string
			if d := c.ArgsLenAtDash(); d >= 0 {
				remote = args[d:]
				args = args[:d]
			}
			if len(args) != 1 {
				return fmt.Errorf("expected one host name, got %d; run `tram ls` to see the list", len(args))
			}
			return runConnect(app, args[0], remote, connectFlags)
		},
		ValidArgsFunction: completeHost,
	}

	p := root.PersistentFlags()
	p.StringVarP(&app.ConfigPath, "config", "F", "", "ssh_config to read instead of ~/.ssh/config")
	p.StringVar(&app.SSHDir, "ssh-dir", "", "directory relative Include paths resolve against")
	p.StringVarP(&app.FormatFlag, "format", "f", "table", "output format: "+strings.Join(render.Formats(), ", "))
	p.BoolVarP(&app.Yes, "yes", "y", false, "answer yes to confirmations")
	p.BoolVar(&app.DryRun, "dry-run", false, "show what would change and write nothing")
	p.BoolVar(&app.Diff, "diff", false, "print a diff of the configuration change")
	p.BoolVar(&app.Force, "force", false, "write to a host tram does not manage")
	p.BoolVar(&app.ASCII, "ascii", false, "use plain ASCII output for consoles without box drawing")

	addConnectFlags(root)

	root.AddCommand(
		newLsCmd(),
		newAddCmd(),
		newEditCmd(),
		newCloneCmd(),
		newRmCmd(),
		newMvCmd(),
		newInitCmd(),
		newImportCmd(),
		newArgsCmd(),
		newSFTPCmd(),
		newAccountCmd(),
		newKeyCmd(),
		newPingCmd(),
		newDoctorCmd(),
		newExecCmd(),
		newRunCmd(),
		newSnippetCmd(),
		newVersionCmd(),
		newCompletionCmd(),
	)
	return root
}

func (a *App) resolveFormat() error {
	f, err := render.Parse(a.FormatFlag)
	if err != nil {
		return err
	}
	a.Format = f
	return nil
}

// confirm asks a yes or no question, defaulting to no. It answers yes without
// asking when -y was given, and refuses to assume yes when there is nobody to
// ask, because a script that pipes tram should not silently agree to a write.
func (a *App) confirm(prompt string) bool {
	if a.Yes {
		return true
	}
	if !isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "refusing to continue without confirmation; pass -y")
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// applyChange is the single path every write goes through, so that --dry-run,
// --diff, warnings and confirmation behave identically for every command.
func (a *App) applyChange(ch *inventory.Change, prompt string) error {
	for _, w := range ch.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	diff := ch.Diff()
	if diff == "" {
		fmt.Fprintln(os.Stderr, "nothing to change")
		ch.Discard()
		return nil
	}
	if a.DryRun || a.Diff {
		fmt.Print(diff)
	}
	if a.DryRun {
		fmt.Fprintln(os.Stderr, "dry run: nothing was written")
		ch.Discard()
		return nil
	}
	if prompt != "" && !a.Yes {
		if !a.Diff {
			fmt.Print(diff)
		}
		if !a.confirm(prompt) {
			ch.Discard()
			return fmt.Errorf("cancelled")
		}
	}
	if err := ch.Apply(); err != nil {
		return err
	}
	for _, f := range ch.Files {
		fmt.Fprintf(os.Stderr, "wrote %s (backup at %s)\n", f.Path, f.Path+".tram.bak")
	}
	a.Reload()
	return nil
}

// pickHost turns a name typed by the user into exactly one host.
//
// An exact match wins. Several fuzzy matches are reported rather than guessed
// between: choosing for the user between db-prod and db-prod-replica is how a
// tool ends up restarting the wrong database.
func (a *App) pickHost(name string) (model.Host, error) {
	inv, err := a.Inventory()
	if err != nil {
		return model.Host{}, err
	}
	hits := inv.Resolve(name)
	switch len(hits) {
	case 0:
		return model.Host{}, fmt.Errorf("no host matches %q; run `tram ls` to see the list", name)
	case 1:
		return hits[0], nil
	}
	if !isTerminal(os.Stdin) || a.Format.Machine() {
		return model.Host{}, fmt.Errorf("%q matches %d hosts: %s", name, len(hits), strings.Join(model.Names(hits), ", "))
	}
	return pickFromList(hits, name)
}

// completeHost supplies shell completion for host names.
func completeHost(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	inv, err := inventory.Load(inventory.Options{ConfigPath: app.ConfigPath, SSHDir: app.SSHDir})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, h := range inv.Hosts() {
		if toComplete == "" || strings.HasPrefix(strings.ToLower(h.Name), strings.ToLower(toComplete)) {
			label := h.Name
			if h.HostName != "" {
				label += "\t" + h.Target()
			}
			out = append(out, label)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeGroup supplies shell completion for group names.
func completeGroup(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	inv, err := inventory.Load(inventory.Options{ConfigPath: app.ConfigPath, SSHDir: app.SSHDir})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return inv.Groups(), cobra.ShellCompDirectiveNoFileComp
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			fmt.Printf("tram %s\n", Version)
			fmt.Printf("config  %s\n", store.ConfigPath())
			fmt.Printf("state   %s\n", store.Dir())
			return nil
		},
	}
}

// CloseSession forgets the passphrases held for this run. main defers it, so a
// normal exit and an error exit both clear the cache.
func CloseSession() { app.CloseSession() }
