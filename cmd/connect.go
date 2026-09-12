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
	Verbose int
}

var connectFlags connectOptions

// addConnectFlags puts the connection flags on the root command, because
// `tram <host>` is the root command rather than a subcommand.
func addConnectFlags(c *cobra.Command) {
	f := c.Flags()
	f.BoolVar(&connectFlags.Window, "window", false, "open the session in a new terminal window or tab")
	f.BoolVar(&connectFlags.NoTTY, "no-tty", false, "do not ask ssh for a terminal")
	f.IntVar(&connectFlags.Timeout, "connect-timeout", 0, "seconds to wait for the connection")
	f.CountVarP(&connectFlags.Verbose, "verbose", "v", "show ssh's own progress; repeat for more")
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
	if res.Interrupted {
		// ctrl+c only reaches ssh while it is still trying to connect: once a
		// session is open the key goes to the program on the far side. So an
		// interrupt means it never got in, and saying where to look next is
		// worth one line. No pause: they just pressed ctrl+c to get out.
		fmt.Fprintf(os.Stderr,
			"gave up on %s. `tram doctor %s` stops at the first thing that does not answer; `tram %s -v` shows ssh's own progress.\n",
			h.Name, h.Name, h.Name)
		return ExitCode{Code: res.ExitCode}
	}
	if res.SSHFailed {
		return ExitCode{Code: res.ExitCode, Err: fmt.Errorf("ssh could not open the session to %s", h.Name)}
	}
	return ExitCode{Code: res.ExitCode, Err: fmt.Errorf("%s: the remote command exited with status %d", h.Name, res.ExitCode)}
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
// It is armed for one purpose and one only: to hand ssh a passphrase tram has
// already been told, so that the second host sharing a key file does not ask
// again. Armed without one, the helper has to ask on the console itself, and
// that is a worse place to be asked than ssh's own prompt: the helper is a
// child process borrowing the terminal in the middle of an authentication.
//
// So the rule is the narrow one. A host with no encrypted key has nothing to
// reuse. A host whose passphrase tram does not know is left to ssh entirely,
// which is what tram did before it existed and works perfectly well.
func askpassFor(a *App, inv *inventory.Inventory, h model.Host) launcher.AskpassSetup {
	// One question to ssh, not three: every one of these is about the same list
	// of keys, and asking for it costs a process.
	keys := effectiveKeys(a, h)
	hasKey := firstEncrypted(keys) != ""
	cached := firstLocked(a, keys) == ""

	force, version := secret.SupportsAskpassRequire()
	if !force && hasKey {
		fmt.Fprintf(os.Stderr,
			"note: %s has no SSH_ASKPASS_REQUIRE, so ssh will ask for the passphrase itself each time\n",
			version)
	}
	// The helper still needs a console for the one question it relays rather
	// than answers: an unknown host key.
	console := isTerminal(os.Stdin) && secret.TTYAvailable()

	if !armAskpass(inv.Store.Options.ReusePassphrase, hasKey, cached, force, console) {
		return launcher.AskpassSetup{}
	}
	return launcher.AskpassSetup{
		Enabled: true,
		Binary:  launcher.SelfPath(),
		Force:   true,
		Session: a.Session(),
	}
}

// armAskpass is the whole rule, in one place, so that it can be read and tested
// without a key, a machine or a terminal anywhere near it.
//
// cached is the one that matters and the one that was got wrong. The helper
// exists to hand ssh an answer tram already has. With an answer, arming it
// means nobody is asked twice. Without one, arming it means the helper has to
// ask on the console itself, in the middle of an authentication, from a child
// process borrowing the terminal: a worse place to be asked than ssh's own
// prompt, which has always worked.
func armAskpass(reuse, hasKey, cached, force, console bool) bool {
	return reuse && hasKey && cached && force && console
}

// firstEncrypted names the first key in a list that exists on this machine and
// is passphrase protected, or nothing.
//
// Only the first: ssh tries them in order, and asking about every encrypted key
// on the machine to open one host would be worse than the problem.
func firstEncrypted(keys []string) string {
	for _, k := range keys {
		if info, err := secret.InspectKey(k); err == nil && info.Exists && info.Encrypted {
			return secret.ExpandKeyPath(k)
		}
	}
	return ""
}

// firstLocked is that key again, unless this run already knows its passphrase,
// in which case nothing stands in the way.
//
// The two questions look alike and are not. Folding them together was a real
// bug: a host stopped counting as having an encrypted key the moment its
// passphrase was cached, so the helper was disarmed exactly when it had
// something to offer, and every host after the first asked again.
func firstLocked(a *App, keys []string) string {
	key := firstEncrypted(keys)
	if key == "" {
		return ""
	}
	if cache := a.Secrets(); cache != nil {
		if _, have := cache.Get(key); have {
			return ""
		}
	}
	return key
}

// firstLockedKey answers the same question about a host, for the interface.
func firstLockedKey(a *App, h model.Host) string { return firstLocked(a, effectiveKeys(a, h)) }

// effectiveKeys is every key ssh would offer for a host, which is more than the
// stanza names: a Host * block above it, or ssh's own defaults when nothing
// names a key at all.
func effectiveKeys(a *App, h model.Host) []string {
	if keys := launcher.IdentityFiles(a.SSHConfigArg(), h.Name); len(keys) > 0 {
		return keys
	}
	return h.IdentityFiles
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
		Verbose:        opt.Verbose,
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
