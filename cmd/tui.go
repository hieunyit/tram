package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/launcher"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/probe"
	"github.com/hieuny/tram/internal/remote"
	"github.com/hieuny/tram/internal/runner"
	"github.com/hieuny/tram/internal/secret"
	"github.com/hieuny/tram/internal/tui"
)

// runTUI shows the interface, and keeps showing it after every session.
//
// The loop is the whole design. The interface cannot open a session while it
// owns the terminal, so it exits, names what it wants, the command layer hands
// the terminal to ssh, and the interface starts again once ssh is done. The
// alternate screen is left and re-entered around the session, which is what
// makes the terminal look clean on both sides of it.
func runTUI(a *App) error {
	if !isTerminal(os.Stdout) {
		return fmt.Errorf("the interface needs a terminal; try `tram ls`")
	}
	tui.Version = Version
	for {
		inv, err := a.Inventory()
		if err != nil {
			return err
		}
		m := tui.New(inv, &tuiRunner{app: a}, a.ASCII)
		opts := []tea.ProgramOption{tea.WithAltScreen()}
		if inv.Store.Options.MouseOn() {
			// Cell motion rather than all motion: tram wants presses, the wheel
			// and a drag, and reporting every idle movement of the pointer is
			// a stream of events nothing here reads.
			opts = append(opts, tea.WithMouseCellMotion())
		}
		p := tea.NewProgram(m, opts...)
		if _, err := p.Run(); err != nil {
			return err
		}

		out := m.Outcome()
		a.Note = ""
		switch out.Action {
		case tui.ActionQuit:
			return nil
		case tui.ActionConnect:
			if err := reportSession(runConnect(a, out.Host, nil, connectOptions{})); err != nil {
				return err
			}
		case tui.ActionSFTP:
			if err := reportSession(runSFTP(a, out.Host)); err != nil {
				return err
			}
		}
		// Anything the session had to say is on the terminal now, and the
		// interface is about to paint over it.
		if a.Note != "" {
			pause()
		}
		a.Reload()
	}
}

// reportSession decides whether a failed session should end tram.
//
// It should not. A refused connection is a normal thing to discover from the
// list, and dropping the user back to a shell because one host was down would
// make the interface useless. The status is shown and the loop continues.
func reportSession(err error) error {
	if err == nil {
		return nil
	}
	var ec ExitCode
	if asExitCode(err, &ec) {
		if ec.Err != nil {
			fmt.Fprintln(os.Stderr, ec.Err)
		} else {
			fmt.Fprintf(os.Stderr, "the remote command exited with status %d\n", ec.Code)
		}
		pause()
		return nil
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	pause()
	return nil
}

func asExitCode(err error, out *ExitCode) bool {
	if ec, ok := err.(ExitCode); ok {
		*out = ec
		return true
	}
	return false
}

// pause holds the message on screen long enough to read before the interface
// repaints over it.
func pause() {
	if !isTerminal(os.Stdin) {
		return
	}
	fmt.Fprint(os.Stderr, "press enter to go back to tram ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// tuiRunner gives the interface the same probe and exec implementation the
// command line uses, so a ping from the list and a ping from the shell cannot
// classify the same failure differently.
type tuiRunner struct{ app *App }

func (r *tuiRunner) opts() (runner.Options, probe.Options) {
	inv, _ := r.app.Inventory()
	p, t := 5, 10
	if inv != nil {
		p, t = inv.Store.Options.Parallel, inv.Store.Options.Timeout
	}
	ro := runner.Options{Parallel: p, Timeout: time.Duration(t) * time.Second}
	return ro, probe.Options{ConfigPath: r.app.SSHConfigArg(), Timeout: ro.Timeout}
}

// factsCommand is what tram runs on the far end to fill in the details pane.
//
// They are four ordinary commands and nothing else: no script, no temporary
// file, no assumption about the shell beyond running one line. A host that has
// none of them, such as a switch, still answers the connection, and the parse
// simply finds nothing. Every one of them is a read.
const factsCommand = "uname -sr 2>/dev/null; uptime 2>/dev/null; " +
	"df -P / 2>/dev/null | tail -1; free -m 2>/dev/null | sed -n 2p"

// Measure probes hosts and, in the same connection, asks each one what it is
// and how loaded it is.
//
// One round trip answers latency, load and system together. Asking separately
// would double the connections for facts that are only worth having because
// they are cheap.
func (r *tuiRunner) Measure(hosts []model.Host) []tui.Measurement {
	ro, po := r.opts()
	po.Command = []string{factsCommand}

	res := runner.Run(context.Background(), jobsWithAddr(hosts), func(ctx context.Context, j runner.Job) probe.Result {
		return probe.Run(ctx, j.Host, withAddr(po, j))
	}, ro)

	out := make([]tui.Measurement, len(res))
	for i, x := range res {
		mm := tui.Measurement{
			Host:   x.Host,
			Class:  string(x.Class),
			OK:     x.Class.Good(),
			Millis: x.Millis,
			Detail: probeSummary(x),
		}
		if mm.OK {
			mm.OS, mm.Uptime, mm.Load, mm.Disk, mm.RAM = parseFacts(x.Output)
		} else {
			mm.Detail = x.Detail
			mm.Explain = x.Class.Explain()
		}
		out[i] = mm
	}
	return out
}

// Open puts hosts in a tab each, or one of them in a pane beside the list.
//
// tram does not give up the screen for this: the terminal opens the tab and
// tram carries on drawing, which is the difference between this and enter.
func (r *tuiRunner) Open(hosts []model.Host, beside bool) (string, error) {
	inv, err := r.app.Inventory()
	if err != nil {
		return "", err
	}
	self := launcher.SelfPath()
	t := launcher.Terminal(inv.Store.Options.Terminal)
	if t == launcher.TermNone {
		t = launcher.DetectTerminal()
	}
	override := inv.Store.Options.WindowCommand
	// The tab runs tram again, and a second tram would start with an empty
	// cache and ask for the same passphrase. Handing it this run's cache is
	// what makes one answer cover the whole run, tabs included.
	env := launcher.Request{Askpass: launcher.AskpassSetup{
		Enabled: true, Binary: launcher.SelfPath(), Session: r.app.Session(),
	}}.Env()

	if beside {
		// A pane is about one host: there is no useful reading of "put six
		// hosts beside the list".
		h := hosts[0]
		argv, split, err := launcher.SplitCommand(t, self, h.Name, override)
		if err != nil {
			return "", err
		}
		if err := launcher.OpenQuietly(argv, env); err != nil {
			return "", err
		}
		if !split {
			return h.Name + " opened in a window; " + whereabouts(t) + " cannot split here", nil
		}
		return h.Name + " opened beside the list", nil
	}

	for _, h := range hosts {
		argv, err := launcher.WindowCommand(t, self, h.Name, override)
		if err != nil {
			return "", err
		}
		if err := launcher.OpenQuietly(argv, env); err != nil {
			return "", err
		}
	}
	// Where it went is worth saying. Run from a terminal that is not the one
	// the tab lands in, "opened a tab" is true and useless.
	what := "a new tab"
	if t == launcher.TermWindowsTerminal && !launcher.InsideWindowsTerminal() {
		what = "a Windows Terminal window"
	}
	if len(hosts) == 1 {
		return hosts[0].Name + " opened in " + what, nil
	}
	return fmt.Sprintf("opened %d of %s", len(hosts), what), nil
}

// Files opens a shell on a host for the two-pane browser.
//
// It is opened the same way an interactive session is, askpass and all, so a
// passphrase typed for a session is not typed again for the browser.
func (r *tuiRunner) Files(h model.Host) (tui.FileSystem, error) {
	inv, err := r.app.Inventory()
	if err != nil {
		return nil, err
	}
	req := launcher.Request{
		Host:           h.Name,
		ConfigPath:     r.app.SSHConfigArg(),
		ConnectTimeout: inv.Store.Options.ConnectTimeout,
		NoTTY:          true,
		// A shell, not a login shell: the browser asks it questions and reads
		// the answers, and a profile printing a banner would be read as a file
		// list.
		Command: []string{"/bin/sh"},
		// The same helper a session uses, so a passphrase already typed in this
		// run opens the browser without asking for it a second time.
		Askpass: askpassFor(r.app, inv, h),
	}
	timeout := time.Duration(max(inv.Store.Options.Timeout, 20)) * time.Second
	return remote.Open(h.Name, remote.Options{
		Argv:    req.Argv(),
		Env:     req.Env(),
		Timeout: timeout,
	})
}

// Locked names the key file standing between tram and a host.
//
// It looks along the whole route, because a jump station's key is as much in
// the way as the destination's, and asks ssh itself which keys each of them
// would offer: the stanza is not the whole answer, and a key in a Host * block
// or one of ssh's own defaults is just as real.
func (r *tuiRunner) Locked(h model.Host) string {
	inv, err := r.app.Inventory()
	if err != nil {
		return ""
	}
	hosts := []model.Host{h}
	for _, hop := range inv.Chain(h.Name).Hops {
		if station, ok := inv.Host(model.ParseJumpSpec(hop.Spec).Host); ok {
			hosts = append(hosts, station)
		}
	}
	for _, x := range hosts {
		if key := firstLockedKey(r.app, x); key != "" {
			return key
		}
	}
	return ""
}

// Unlock checks a passphrase against the key and remembers it for this run.
func (r *tuiRunner) Unlock(keyPath, passphrase string) error {
	if err := secret.VerifyPassphrase(keyPath, passphrase); err != nil {
		return err
	}
	cache := r.app.Secrets()
	if cache == nil {
		return fmt.Errorf("this run has no passphrase cache to put it in")
	}
	return cache.Put(keyPath, passphrase)
}

// Copy moves one file or folder between this machine and a host.
func (r *tuiRunner) Copy(job remote.Copy) error {
	env := os.Environ()
	if inv, err := r.app.Inventory(); err == nil {
		if h, ok := inv.Host(job.Host); ok {
			// scp asks the same question ssh does, and can have the same
			// answer: the one already given in this run.
			env = launcher.Request{Askpass: askpassFor(r.app, inv, h)}.Env()
		}
	}
	return job.Run(r.app.SSHConfigArg(), env)
}

// whereabouts names a terminal the way the user would.
func whereabouts(t launcher.Terminal) string {
	switch t {
	case launcher.TermWindowsTerminal:
		return "Windows Terminal"
	case launcher.TermTmux:
		return "tmux"
	case launcher.TermNone:
		return "no terminal tram recognises"
	}
	return string(t)
}

// Agent reports what the ssh agent is holding.
//
// ssh-add's exit status is the answer: 0 with a list, 1 for an agent with
// nothing in it, 2 for no agent at all. The keys themselves are never shown;
// how many there are is all the bar has room for and all it needs to say.
func (r *tuiRunner) Agent() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ssh-add", "-l").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "agent empty"
		}
		return "no agent"
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	if n == 1 {
		return "agent 1 key"
	}
	return fmt.Sprintf("agent %d keys", n)
}

// probeSummary is the one line a result is worth when it is not a success.
func probeSummary(x probe.Result) string {
	if x.Class.Good() {
		return fmt.Sprintf("%d ms", x.Millis)
	}
	if x.Detail != "" {
		return x.Detail
	}
	return x.Class.Explain()
}

// parseFacts reads what uname and uptime wrote.
//
// Both commands vary between systems, so this looks for the shapes they agree
// on and gives up quietly on the rest: an empty field is drawn as a dash, which
// is better than a wrong reading.
func parseFacts(output string) (os, up, load, disk, ram string) {
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.Contains(line, " up ") || strings.Contains(line, "load average"):
			u, l := parseUptime(line)
			if u != "" {
				up = u
			}
			if l != "" {
				load = l
			}
		case strings.HasPrefix(line, "Mem:") || strings.HasPrefix(line, "Mem "):
			ram = parseMem(line)
		case strings.HasSuffix(line, " /"):
			disk = parseDisk(line)
		case os == "":
			// uname -sr writes one line and nothing else, so the first line that
			// is none of the others is it.
			os = line
		}
	}
	return os, up, load, disk, ram
}

// parseDisk reads the last line of df -P /, which is the same six fields on
// every system that has df at all: device, size, used, free, percentage, mount.
func parseDisk(line string) string {
	f := strings.Fields(line)
	if len(f) < 6 {
		return ""
	}
	for _, v := range f {
		if strings.HasSuffix(v, "%") {
			return v
		}
	}
	return ""
}

// parseMem turns the Mem: line of free -m into a percentage, which is the form
// the reading is worth in a cell four characters wide.
func parseMem(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 {
		return ""
	}
	total, err1 := strconv.Atoi(f[1])
	used, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil || total <= 0 {
		return ""
	}
	return strconv.Itoa(used*100/total) + "%"
}

// parseUptime pulls how long the machine has been up and its first load figure
// out of the one line uptime writes.
func parseUptime(line string) (up, load string) {
	if i := strings.Index(line, " up "); i >= 0 {
		rest := line[i+4:]
		// The clause after the uptime is either the user count or the load
		// average, and both are introduced by a comma.
		cut := len(rest)
		for _, marker := range []string{"user", "load average"} {
			if j := strings.Index(rest, marker); j >= 0 && j < cut {
				if k := strings.LastIndex(rest[:j], ","); k >= 0 {
					cut = k
				}
			}
		}
		up = strings.TrimSpace(rest[:cut])
	}
	if i := strings.Index(line, "load average"); i >= 0 {
		if j := strings.Index(line[i:], ":"); j >= 0 {
			fields := strings.FieldsFunc(line[i+j+1:], func(rn rune) bool { return rn == ',' || rn == ' ' })
			if len(fields) > 0 {
				load = fields[0]
			}
		}
	}
	return up, load
}

func (r *tuiRunner) Doctor(hosts []model.Host) []tui.Row {
	inv, err := r.app.Inventory()
	if err != nil {
		return nil
	}
	_, po := r.opts()
	rows := make([]tui.Row, 0, len(hosts))
	for _, h := range hosts {
		hOpt := po
		hOpt.Addr = h.Addr()
		d := probe.Doctor(context.Background(), h.Name, inv.Chain(h.Name), hOpt)
		var body strings.Builder
		for _, s := range d.Stages {
			switch {
			case s.Skipped:
				fmt.Fprintf(&body, "%-24s not reached\n", s.Name)
			case s.Class.Good():
				fmt.Fprintf(&body, "%-24s %s (%d ms)\n", s.Name, s.Class, s.Millis)
			default:
				fmt.Fprintf(&body, "%-24s %s  %s\n", s.Name, s.Class, s.Detail)
			}
		}
		if d.Advice != "" {
			fmt.Fprintf(&body, "\n%s\n", d.Advice)
		}
		summary := fmt.Sprintf("%d stage(s) ok", len(d.Stages))
		status := "OK"
		if !d.OK() {
			status = string(d.Class)
			summary = "stopped at " + d.FailedAt
		}
		rows = append(rows, tui.Row{Host: h.Name, Status: status, OK: d.OK(), Summary: summary, Body: body.String()})
	}
	return rows
}

func (r *tuiRunner) Exec(hosts []model.Host, command string) []tui.Row {
	ro, po := r.opts()
	po.Command = []string{command}
	res := runner.Run(context.Background(), jobsWithAddr(hosts), func(ctx context.Context, j runner.Job) probe.Result {
		return probe.Run(ctx, j.Host, withAddr(po, j))
	}, ro)

	rows := make([]tui.Row, len(res))
	for i, x := range res {
		body := strings.TrimRight(x.Output, "\n")
		if x.Raw != "" {
			if body != "" {
				body += "\n"
			}
			body += strings.TrimRight(x.Raw, "\n")
		}
		summary := "exit 0"
		ok := x.Class.Good() && x.ExitCode == 0
		switch {
		case !x.Class.Good():
			summary = x.Detail
		case x.ExitCode != 0:
			summary = fmt.Sprintf("exit %d", x.ExitCode)
		}
		rows[i] = tui.Row{Host: x.Host, Status: string(x.Class), OK: ok, Summary: summary, Body: body}
	}
	return rows
}

// pickFromList asks which of several matching hosts was meant. tram asks
// rather than guessing, because the names that collide are usually the ones
// where the difference matters.
func pickFromList(hits []model.Host, query string) (model.Host, error) {
	fmt.Fprintf(os.Stderr, "%q matches %d hosts:\n", query, len(hits))
	for i, h := range hits {
		fmt.Fprintf(os.Stderr, "  %2d  %-24s %s\n", i+1, h.Name, h.Target())
	}
	fmt.Fprint(os.Stderr, "choose [1]: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return model.Host{}, fmt.Errorf("cancelled")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return hits[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(hits) {
		return model.Host{}, fmt.Errorf("%q is not one of the choices", line)
	}
	return hits[n-1], nil
}
