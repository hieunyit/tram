package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hieuny/tram/internal/inventory"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/probe"
	"github.com/hieuny/tram/internal/render"
	"github.com/hieuny/tram/internal/runner"
	"github.com/hieuny/tram/internal/store"
	"github.com/spf13/cobra"
)

// fleetFlags are the selection and fan-out flags every multi-host command has.
type fleetFlags struct {
	group    string
	search   string
	all      bool
	parallel int
	timeout  int
	stream   bool
}

func (ff *fleetFlags) bind(c *cobra.Command) {
	f := c.Flags()
	f.StringVarP(&ff.group, "group", "g", "", "hosts in this group, including nested groups")
	f.StringVarP(&ff.search, "search", "s", "", "hosts matching this text")
	f.BoolVar(&ff.all, "all", false, "every host")
	f.IntVarP(&ff.parallel, "parallel", "P", 0, "how many hosts at once")
	f.IntVar(&ff.timeout, "timeout", 0, "seconds to allow per host")
	_ = c.RegisterFlagCompletionFunc("group", completeGroup)
}

// hosts resolves the flags and any explicit names into a host list, and refuses
// to run against everything by accident.
func (ff *fleetFlags) hosts(a *App, names []string) ([]model.Host, error) {
	inv, err := a.Inventory()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 && ff.group == "" && ff.search == "" && !ff.all {
		return nil, fmt.Errorf("choose hosts with a name, -g, -s or --all")
	}
	var out []model.Host
	if len(names) > 0 {
		for _, n := range names {
			h, err := a.pickHost(n)
			if err != nil {
				return nil, err
			}
			out = append(out, h)
		}
		return out, nil
	}
	out = inv.Select(inventory.Filter{Group: ff.group, Search: ff.search})
	if len(out) == 0 {
		return nil, fmt.Errorf("no host matches that selection")
	}
	return out, nil
}

func (ff *fleetFlags) options(a *App) runner.Options {
	st := store.Load()
	p, t := ff.parallel, ff.timeout
	if p <= 0 {
		p = st.Options.Parallel
	}
	if t <= 0 {
		t = st.Options.Timeout
	}
	return runner.Options{Parallel: p, Timeout: time.Duration(t) * time.Second}
}

// confirmFleet asks before touching more than one machine, which is the point
// at which a mistake stops being cheap.
func (a *App) confirmFleet(hosts []model.Host, what string) bool {
	if len(hosts) <= 1 || a.Yes {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s on %d hosts: %s\n", what, len(hosts), strings.Join(model.Names(hosts), ", "))
	return a.confirm("continue?")
}

func newPingCmd() *cobra.Command {
	var ff fleetFlags
	c := &cobra.Command{
		Use:   "ping [host...]",
		Short: "Check hosts and say what kind of failure each one had",
		Long: `Try to open a session on each host and report the outcome.

The outcome is a class, not a yes or no: OK, AUTH, REFUSED, TIMEOUT, DNS,
HOST_KEY, JUMP or CONFIG. Each one points somewhere different, and collapsing
them into "failed" throws away the only useful part of the answer.

Nothing is prompted for: BatchMode is on, so a host that would ask for a
password is reported as AUTH rather than blocking the run.`,
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
			opt := ff.options(app)
			pOpt := probe.Options{ConfigPath: app.SSHConfigArg(), Timeout: opt.Timeout}

			jobs := jobsWithAddr(hosts)
			if !app.Format.Machine() && len(jobs) > 1 {
				opt.OnResult = func(r probe.Result) { fmt.Fprintf(os.Stderr, "  %-20s %s\n", r.Host, r.Class) }
			}
			results := runner.Run(context.Background(), jobs, func(ctx context.Context, j runner.Job) probe.Result {
				return probe.Run(ctx, j.Host, withAddr(pOpt, j))
			}, opt)

			g := &render.Grid{Columns: []string{"host", "class", "ms", "detail"}, RightAlign: map[int]bool{2: true}}
			for _, r := range results {
				g.Add(r.Host, string(r.Class), fmt.Sprint(r.Millis), r.Detail)
			}
			if opt.OnResult != nil {
				fmt.Fprintln(os.Stderr)
			}
			if err := render.Out(os.Stdout, app.Format, results, g); err != nil {
				return err
			}
			if s := runner.Summarise(results); s.Failed > 0 {
				return ExitCode{Code: 1, Err: fmt.Errorf("%d of %d hosts failed", s.Failed, s.Total)}
			}
			return nil
		},
	}
	ff.bind(c)
	return c
}

func newDoctorCmd() *cobra.Command {
	var (
		ff       fleetFlags
		checkCfg bool
	)
	c := &cobra.Command{
		Use:   "doctor [host]",
		Short: "Walk a route station by station, or check ssh_config itself",
		Long: `Diagnose a host by testing every station on its ProxyJump route in order,
stopping at the first one that fails.

Testing past a broken station tells you nothing, so tram does not: it names the
station, says what kind of failure it was, and marks the rest as not reached.

With --config, check the configuration itself instead: file permissions, the
placement of Include lines, hosts declared twice, ProxyJump entries that name
nothing, ProxyJump loops, and IdentityFile paths that are missing or too open.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			if checkCfg {
				return runDoctorConfig(app)
			}
			hosts, err := ff.hosts(app, args)
			if err != nil {
				return err
			}
			inv, err := app.Inventory()
			if err != nil {
				return err
			}
			opt := ff.options(app)
			pOpt := probe.Options{ConfigPath: app.SSHConfigArg(), Timeout: opt.Timeout}

			var all []probe.Diagnosis
			failed := 0
			for _, h := range hosts {
				hOpt := pOpt
				hOpt.Addr = h.Addr()
				d := probe.Doctor(context.Background(), h.Name, inv.Chain(h.Name), hOpt)
				all = append(all, d)
				if !d.OK() {
					failed++
				}
				if !app.Format.Machine() {
					printDiagnosis(d)
				}
			}
			if app.Format.Machine() {
				g := &render.Grid{Columns: []string{"host", "stage", "kind", "class", "ms", "detail"}}
				for _, d := range all {
					for _, s := range d.Stages {
						cls := string(s.Class)
						if s.Skipped {
							cls = "SKIPPED"
						}
						g.Add(d.Host, s.Name, s.Kind, cls, fmt.Sprint(s.Millis), s.Detail)
					}
				}
				if err := render.Out(os.Stdout, app.Format, all, g); err != nil {
					return err
				}
			}
			if failed > 0 {
				return ExitCode{Code: 1, Err: fmt.Errorf("%d of %d hosts have a broken route", failed, len(all))}
			}
			return nil
		},
	}
	ff.bind(c)
	c.Flags().BoolVar(&checkCfg, "config", false, "check ssh_config itself instead of a host")
	return c
}

func printDiagnosis(d probe.Diagnosis) {
	fmt.Printf("%s\n", d.Host)
	for _, s := range d.Stages {
		switch {
		case s.Skipped:
			fmt.Printf("  %-3s %-24s not reached\n", "-", s.Name)
		case s.Class.Good():
			fmt.Printf("  %-3s %-24s %s (%d ms)\n", mark(true), s.Name, s.Class, s.Millis)
		default:
			fmt.Printf("  %-3s %-24s %s  %s\n", mark(false), s.Name, s.Class, s.Detail)
		}
	}
	if d.Advice != "" {
		fmt.Printf("  %s\n", d.Advice)
	}
	fmt.Println()
}

func mark(ok bool) string {
	if app.ASCII {
		if ok {
			return "ok"
		}
		return "!!"
	}
	if ok {
		return "✓"
	}
	return "✗"
}

func runDoctorConfig(a *App) error {
	inv, err := a.Inventory()
	if err != nil {
		return err
	}
	in := probe.ConfigInput{Hosts: inv.Hosts(), Duplicates: map[string][]string{}}
	for _, e := range inv.Errors() {
		in.LoadErrors = append(in.LoadErrors, e.Error())
	}
	for _, f := range inv.Config.Files {
		cf := probe.ConfigFile{Path: f.Path}
		for i, l := range f.Lines {
			if cf.IncludeLine == 0 && l.Is("Include") {
				cf.IncludeLine = i + 1
			}
			if cf.FirstHost == 0 && (l.Is("Host") || l.Is("Match")) {
				cf.FirstHost = i + 1
			}
		}
		in.Files = append(in.Files, cf)
	}
	for name, blocks := range inv.Config.Duplicates() {
		var where []string
		for _, b := range blocks {
			where = append(where, fmt.Sprintf("%s:%d", b.File.Path, b.Head+1))
		}
		in.Duplicates[name] = where
	}

	findings := probe.CheckConfig(in)
	g := &render.Grid{
		Columns: []string{"severity", "where", "message"},
		Empty:   "no problems found in " + store.ConfigPath(),
	}
	for _, f := range findings {
		g.Add(f.Severity, shortPath(f.Where), f.Message)
	}
	if err := render.Out(os.Stdout, app.Format, findings, g); err != nil {
		return err
	}
	for _, f := range findings {
		if f.Severity == "error" {
			return ExitCode{Code: 1, Err: fmt.Errorf("%d problem(s) found", len(findings))}
		}
	}
	return nil
}

func newExecCmd() *cobra.Command {
	var ff fleetFlags
	c := &cobra.Command{
		Use:   "exec [host...] -- <command>",
		Short: "Run one command on many hosts at once",
		Long: `Run a command on several hosts in parallel and collect the output.

The command runs without a terminal, so it must not expect one. Anything
interactive belongs in a session, which is what "tram <host>" is for.

The exit status is non-zero when any host failed, so this is usable from a
build script.`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeHost,
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			d := c.ArgsLenAtDash()
			if d < 0 {
				return fmt.Errorf("put the command after --, for example: tram exec -g prod -- uptime")
			}
			command := args[d:]
			names := args[:d]
			if len(command) == 0 {
				return fmt.Errorf("no command given after --")
			}
			hosts, err := ff.hosts(app, names)
			if err != nil {
				return err
			}
			if !app.confirmFleet(hosts, "run "+strings.Join(command, " ")) {
				return fmt.Errorf("cancelled")
			}
			return runExec(app, hosts, command, ff)
		},
	}
	ff.bind(c)
	c.Flags().BoolVar(&ff.stream, "stream", false, "print each host's output as it finishes")
	return c
}

func runExec(a *App, hosts []model.Host, command []string, ff fleetFlags) error {
	opt := ff.options(a)
	pOpt := probe.Options{ConfigPath: a.SSHConfigArg(), Timeout: opt.Timeout, Command: command}

	jobs := jobsWithAddr(hosts)
	if ff.stream && !a.Format.Machine() {
		opt.OnResult = func(r probe.Result) { printExecResult(r) }
	}
	results := runner.Run(context.Background(), jobs, func(ctx context.Context, j runner.Job) probe.Result {
		return probe.Run(ctx, j.Host, withAddr(pOpt, j))
	}, opt)

	if a.Format.Machine() {
		g := &render.Grid{Columns: []string{"host", "class", "exit_code", "output"}}
		for _, r := range results {
			g.Add(r.Host, string(r.Class), fmt.Sprint(r.ExitCode), strings.TrimRight(r.Output, "\n"))
		}
		if err := render.Out(os.Stdout, a.Format, results, g); err != nil {
			return err
		}
	} else if !ff.stream {
		for _, r := range results {
			printExecResult(r)
		}
	}

	s := runner.Summarise(results)
	bad := 0
	for _, r := range results {
		if !r.Class.Good() || r.ExitCode != 0 {
			bad++
		}
	}
	if !a.Format.Machine() {
		fmt.Fprintf(os.Stderr, "%d of %d hosts succeeded\n", s.Total-bad, s.Total)
	}
	if bad > 0 {
		return ExitCode{Code: 1, Err: fmt.Errorf("%d of %d hosts failed", bad, s.Total)}
	}
	return nil
}

func printExecResult(r probe.Result) {
	head := r.Host
	if !r.Class.Good() {
		fmt.Printf("=== %s  %s  %s\n", head, r.Class, r.Detail)
		return
	}
	if r.ExitCode != 0 {
		fmt.Printf("=== %s  exit %d\n", head, r.ExitCode)
	} else {
		fmt.Printf("=== %s\n", head)
	}
	out := strings.TrimRight(r.Output, "\n")
	if out != "" {
		for _, l := range strings.Split(out, "\n") {
			fmt.Println("    " + l)
		}
	}
	if r.Raw != "" {
		for _, l := range strings.Split(strings.TrimRight(r.Raw, "\n"), "\n") {
			fmt.Println("    " + l)
		}
	}
}

func newRunCmd() *cobra.Command {
	var ff fleetFlags
	c := &cobra.Command{
		Use:   "run <snippet> [host...]",
		Short: "Run a saved snippet on hosts",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			st := store.Load()
			_ = st.SeedSnippets()
			sn, ok := st.Snippet(args[0])
			if !ok {
				return fmt.Errorf("no snippet named %q; run `tram snippet ls`", args[0])
			}
			hosts, err := ff.hosts(app, args[1:])
			if err != nil {
				return err
			}
			what := sn.Name + ": " + sn.Command
			if sn.Confirm && !app.Yes {
				fmt.Fprintf(os.Stderr, "! %s is marked as needing confirmation\n", sn.Name)
				if !app.confirm(what + " on " + fmt.Sprint(len(hosts)) + " host(s)?") {
					return fmt.Errorf("cancelled")
				}
			} else if !app.confirmFleet(hosts, what) {
				return fmt.Errorf("cancelled")
			}
			return runExec(app, hosts, []string{sn.Command}, ff)
		},
		ValidArgsFunction: func(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return completeHost(c, args, toComplete)
			}
			st := store.Load()
			var out []string
			for _, s := range st.SnippetList() {
				out = append(out, s.Name+"\t"+s.Desc)
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
	}
	ff.bind(c)
	c.Flags().BoolVar(&ff.stream, "stream", false, "print each host's output as it finishes")
	return c
}

func newSnippetCmd() *cobra.Command {
	c := &cobra.Command{Use: "snippet", Short: "Manage saved commands"}

	c.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List snippets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.resolveFormat(); err != nil {
				return err
			}
			st := store.Load()
			_ = st.SeedSnippets()
			list := st.SnippetList()
			g := &render.Grid{Columns: []string{"name", "desc", "confirm", "command"}, Empty: "no snippets"}
			for _, s := range list {
				confirm := ""
				if s.Confirm {
					confirm = "yes"
				}
				g.Add(s.Name, s.Desc, confirm, s.Command)
			}
			return render.Out(os.Stdout, app.Format, list, g)
		},
	})

	add := &cobra.Command{
		Use:   "add <name> -- <command>",
		Short: "Save a snippet",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := cmd.ArgsLenAtDash()
			if d < 1 {
				return fmt.Errorf("put the command after --")
			}
			desc, _ := cmd.Flags().GetString("desc")
			confirm, _ := cmd.Flags().GetBool("confirm")
			st := store.Load()
			return st.PutSnippet(store.Snippet{
				Name:    args[0],
				Desc:    desc,
				Command: strings.Join(args[d:], " "),
				Confirm: confirm,
			})
		},
	}
	add.Flags().String("desc", "", "what the snippet does")
	add.Flags().Bool("confirm", false, "ask before running it, for anything destructive")
	c.AddCommand(add)

	c.AddCommand(&cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a snippet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.Load().DeleteSnippet(args[0])
		},
	})
	return c
}

// jobsWithAddr carries each host's resolved address alongside its name, so the
// probe can tell a failure at the destination from one at a jump station.
func jobsWithAddr(hosts []model.Host) []runner.Job {
	jobs := make([]runner.Job, len(hosts))
	for i, h := range hosts {
		jobs[i] = runner.Job{Host: h.Name, Data: h.Addr()}
	}
	return jobs
}

func withAddr(opt probe.Options, j runner.Job) probe.Options {
	if addr, ok := j.Data.(string); ok {
		opt.Addr = addr
	}
	return opt
}
