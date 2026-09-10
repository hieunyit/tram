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
	"github.com/hieuny/tram/internal/store"
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

	req := buildRequest(a, h.Name, remote, opt)
	req.ForceTTY = req.ForceTTY && isTerminal(os.Stdin)

	st := secret.New(store.Dir())
	req.Askpass = askpassFor(a, inv, st, h)

	_ = inv.Store.Touch(h.Name)
	res := launcher.Handoff(req.Argv(), req.Env())

	// A secret typed during the session is only worth keeping if it opened the
	// session. Committing on the way in would remember a typo forever. A remote
	// command that exits non-zero still authenticated, so that counts as worked.
	if note := settleLearned(st, req.Askpass, !res.SSHFailed); note != "" {
		fmt.Fprintln(os.Stderr, note)
		a.Note = note
	}

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

// askpassFor decides whether ssh should ask tram for the secrets it needs.
//
// The secret itself does not travel here. Only a subject name and a nonce go
// into the child's environment, and ssh calls tram back on a separate
// invocation to fetch or capture the value.
//
// Two cases arm the helper. One is a secret tram already holds, which is the
// point of storing it. The other is the first connection to a host that has
// none: the helper asks once, on the console, and the answer is remembered if
// the session works. That is what turns "type it every time" into "type it
// once", and it is why the helper is armed even when the keyring is empty.
func askpassFor(a *App, inv *inventory.Inventory, st *secret.Store, h model.Host) launcher.AskpassSetup {
	force, version := secret.SupportsAskpassRequire()

	// A password set on the host wins over one set on its account, because the
	// more specific answer is the one that was meant.
	sub := secret.PasswordFor("host", h.Name)
	_, stored := st.Get(sub)
	if stored != nil && h.Account != "" {
		acct := secret.PasswordFor("account", h.Account)
		if _, err := st.Get(acct); err == nil {
			sub, stored = acct, nil
		}
	}

	if stored == nil {
		if !force {
			fmt.Fprintf(os.Stderr,
				"warning: a password is stored for %s but %s has no SSH_ASKPASS_REQUIRE, so ssh will prompt instead\n",
				h.Name, version)
		}
		return launcher.AskpassSetup{
			Enabled: true,
			Binary:  launcher.SelfPath(),
			Token:   string(sub),
			Force:   force,
		}
	}

	// Nothing stored. Offer to learn one, but only when every part of the
	// mechanism is actually there. Arming it otherwise would be worse than
	// doing nothing: with SSH_ASKPASS_REQUIRE=force ssh does not fall back to
	// asking on its own, so a helper with nowhere to prompt would fail the
	// login with nothing on screen to explain why.
	switch {
	case !inv.Store.Options.RememberSecrets:
		return launcher.AskpassSetup{}
	case !force:
		return launcher.AskpassSetup{}
	case !secret.TTYAvailable():
		return launcher.AskpassSetup{}
	}
	return launcher.AskpassSetup{
		Enabled: true,
		Binary:  launcher.SelfPath(),
		Token:   string(sub),
		Force:   true,
		Learn:   secret.NewNonce(),
	}
}

// settleLearned commits or discards whatever the helper captured, and returns
// the line to tell the user about it, or "" when there is nothing to say.
func settleLearned(st *secret.Store, setup launcher.AskpassSetup, worked bool) string {
	if setup.Learn == "" {
		return ""
	}
	l, ok := st.TakePending(setup.Learn)
	if !ok {
		return "" // ssh never had to ask
	}
	if !worked {
		return "the session failed, so tram did not remember what you typed"
	}
	if err := st.Commit(l); err != nil {
		return "tram could not save the secret: " + err.Error()
	}
	return fmt.Sprintf("tram remembered the %s for %s and will not ask again. Undo with `tram secret rm %s`.",
		l.Subject.Kind(), l.Subject.Label(), lastField(l.Subject.Label()))
}

func lastField(s string) string {
	if i := strings.LastIndex(s, " "); i >= 0 {
		return s[i+1:]
	}
	return s
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
