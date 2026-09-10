package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/launcher"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/render"
	"github.com/hieuny/tram/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type connectOptions struct {
	Window  bool
	NoTTY   bool
	Timeout int
}

var connectFlags connectOptions

// addConnectFlags puts the connection flags on the root command, because
// `tram <host>` is the root command rather than a subcommand.
func addConnectFlags(c *cobra.Command) {
	f := c.Flags()
	f.BoolVar(&connectFlags.Window, "window", false, "open the session in a new terminal window or tab")
	f.BoolVar(&connectFlags.NoTTY, "no-tty", false, "do not ask ssh for a terminal")
	f.IntVar(&connectFlags.Timeout, "connect-timeout", 0, "seconds to wait for the connection")
}

// runConnect opens a session, or runs one command and exits.
//
// tram builds the argv and then gets out of the way. It does not draw the
// session, does not proxy keystrokes and does not sit between the terminal and
// ssh: the child inherits the real streams, so a full-screen editor, a mouse
// aware pager and a remote tmux all behave as if ssh had been typed directly.
func runConnect(a *App, name string, remote []string, opt connectOptions) error {
	inv, err := a.Inventory()
	if err != nil {
		return err
	}
	h, err := a.pickHost(name)
	if err != nil {
		return err
	}

	if why := launcher.JumpPreflight(inv.Chain(h.Name)); why != "" {
		return fmt.Errorf("refusing to connect to %s: %s", h.Name, why)
	}

	if opt.Window {
		self := launcher.SelfPath()
		t := launcher.Terminal(inv.Store.Options.Terminal)
		if t == "" {
			t = launcher.DetectTerminal()
		}
		argv, err := launcher.WindowCommand(t, self, h.Name, inv.Store.Options.WindowCommand)
		if err != nil {
			return err
		}
		if err := launcher.OpenWindow(argv); err != nil {
			return err
		}
		_ = inv.Store.Touch(h.Name)
		return nil
	}

	if opt.Timeout == 0 {
		opt.Timeout = inv.Store.Options.ConnectTimeout
	}
	req := buildRequest(a, h.Name, remote, opt)
	req.ForceTTY = req.ForceTTY && isTerminal(os.Stdin)

	req.Askpass = askpassFor(a, inv, h)

	announce(inv.Chain(h.Name), h)

	_ = inv.Store.Touch(h.Name)
	res := launcher.Handoff(req.Argv(), req.Env())

	if res.Err != nil {
		return fmt.Errorf("could not start ssh: %w", res.Err)
	}
	if res.ExitCode == 0 {
		return nil
	}
	if res.SSHFailed {
		return ExitCode{Code: res.ExitCode, Err: fmt.Errorf("ssh could not open the session to %s", h.Name)}
	}
	return ExitCode{Code: res.ExitCode}
}

// announce says what is being dialled, on one line, before ssh takes the
// terminal.
//
// ssh sets no connect timeout of its own and prints nothing while it waits, so
// a jump station that does not answer looks exactly like a hung program: a
// blank screen for minutes with no clue which machine is not replying. One line
// naming the route turns that into something a person can act on, and it
// scrolls away the moment the session opens.
func announce(chain model.JumpChain, h model.Host) {
	if !isTerminal(os.Stderr) {
		return
	}
	for _, line := range routeLines(chain, h.Name) {
		fmt.Fprintln(os.Stderr, line)
	}
}

// routeLines builds what announce prints. A route through stations gets a
// second line, because that is the case where a silent wait has somewhere to
// look, and the first line alone would not say where.
func routeLines(chain model.JumpChain, name string) []string {
	if len(chain.Hops) == 0 {
		return []string{"connecting: " + name}
	}
	parts := make([]string, 0, len(chain.Hops)+1)
	for _, hop := range chain.Hops {
		parts = append(parts, hop.Spec)
	}
	parts = append(parts, name)
	return []string{
		"connecting: " + strings.Join(parts, " -> "),
		"  waiting here means a station is not answering; ctrl+c, then `tram doctor " + name + "`",
	}
}

// askpassFor decides whether ssh should route its questions through tram.
//
// It does so for one reason: to reuse a key passphrase across the hosts that
// share the key file, for as long as tram is running. Nothing is stored, so
// there is nothing to arm the helper for on a host whose keys are not
// passphrase protected, and those hosts are left to ssh entirely.
//
// The check before arming is not caution for its own sake. With the helper
// forced, ssh does not fall back to asking on its own, so arming it where it
// could not ask would fail the login with nothing on screen to explain why.
func askpassFor(a *App, inv *inventory.Inventory, h model.Host) launcher.AskpassSetup {
	if !inv.Store.Options.ReusePassphrase {
		return launcher.AskpassSetup{}
	}
	if !hasEncryptedKey(h) {
		return launcher.AskpassSetup{}
	}
	force, version := secret.SupportsAskpassRequire()
	if !force {
		fmt.Fprintf(os.Stderr,
			"note: %s has no SSH_ASKPASS_REQUIRE, so ssh will ask for the passphrase itself each time\n",
			version)
		return launcher.AskpassSetup{}
	}
	// The helper asks on the console when it has no cached answer, so it needs
	// somebody there to ask. Both checks matter: a console tram can open, and a
	// run that a person is actually sitting at. Arming it in a script would
	// block on a prompt nobody sees.
	if !isTerminal(os.Stdin) || !secret.TTYAvailable() {
		return launcher.AskpassSetup{}
	}
	return launcher.AskpassSetup{
		Enabled: true,
		Binary:  launcher.SelfPath(),
		Force:   true,
		Session: a.Session(),
	}
}

// hasEncryptedKey reports whether any key this host would offer is passphrase
// protected, which is the only case where reusing an answer helps.
func hasEncryptedKey(h model.Host) bool {
	for _, k := range h.IdentityFiles {
		if info, err := secret.InspectKey(k); err == nil && info.Encrypted {
			return true
		}
	}
	return false
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// buildRequest assembles the ssh request for a host without running it, so
// that `tram args` and the connect path cannot drift apart.
func buildRequest(a *App, host string, remote []string, opt connectOptions) launcher.Request {
	return launcher.Request{
		Host:           host,
		ConfigPath:     a.SSHConfigArg(),
		Command:        remote,
		NoTTY:          opt.NoTTY,
		ForceTTY:       len(remote) > 0 && !opt.NoTTY,
		ConnectTimeout: opt.Timeout,
	}
}

// renderArgv prints an argv in a machine-readable format.
func renderArgv(argv []string) error {
	return render.Out(os.Stdout, app.Format, map[string]any{
		"argv":    argv,
		"command": launcher.Describe(argv),
	}, &render.Grid{Columns: []string{"arg"}, Rows: argvRows(argv)})
}

func argvRows(argv []string) [][]string {
	rows := make([][]string, len(argv))
	for i, a := range argv {
		rows[i] = []string{a}
	}
	return rows
}

// runSFTP hands a host to the system sftp client, the same way a session is
// handed to ssh.
func runSFTP(a *App, host string) error {
	res := launcher.Handoff(launcher.SFTPArgv(host, a.SSHConfigArg()), nil)
	if res.Err != nil {
		return fmt.Errorf("could not start sftp: %w", res.Err)
	}
	if res.ExitCode != 0 {
		return ExitCode{Code: res.ExitCode}
	}
	return nil
}
