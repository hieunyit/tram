package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/probe"
	"github.com/hieuny/tram/internal/runner"
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
	for {
		inv, err := a.Inventory()
		if err != nil {
			return err
		}
		m := tui.New(inv, &tuiRunner{app: a}, a.ASCII)
		p := tea.NewProgram(m, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			return err
		}

		out := m.Outcome()
		switch out.Action {
		case tui.ActionQuit:
			return nil
		case tui.ActionConnect:
			if err := reportSession(runConnect(a, out.Host, nil, connectOptions{})); err != nil {
				return err
			}
		case tui.ActionWindow:
			if err := reportSession(runConnect(a, out.Host, nil, connectOptions{Window: true})); err != nil {
				return err
			}
		case tui.ActionSFTP:
			if err := reportSession(runSFTP(a, out.Host)); err != nil {
				return err
			}
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

func (r *tuiRunner) Ping(hosts []model.Host) []tui.Row {
	ro, po := r.opts()
	res := runner.Run(context.Background(), jobsWithAddr(hosts), func(ctx context.Context, j runner.Job) probe.Result {
		return probe.Run(ctx, j.Host, withAddr(po, j))
	}, ro)

	rows := make([]tui.Row, len(res))
	for i, x := range res {
		rows[i] = tui.Row{
			Host:    x.Host,
			Status:  string(x.Class),
			OK:      x.Class.Good(),
			Summary: pingSummary(x),
			Body:    strings.TrimSpace(x.Raw),
		}
	}
	return rows
}

func pingSummary(x probe.Result) string {
	if x.Class.Good() {
		return fmt.Sprintf("%d ms", x.Millis)
	}
	if x.Detail != "" {
		return x.Detail
	}
	return x.Class.Explain()
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
