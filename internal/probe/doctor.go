package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/secret"
)

// Stage is one step of a diagnosis.
type Stage struct {
	// Name is the station being tested, or the destination.
	Name string `json:"name"`
	// Kind is "jump" or "destination".
	Kind   string `json:"kind"`
	Class  Class  `json:"class"`
	Detail string `json:"detail"`
	Millis int64  `json:"ms"`
	// Skipped marks the stages tram did not reach because an earlier one
	// failed.
	Skipped bool `json:"skipped"`
}

// Diagnosis is the result of walking a route.
type Diagnosis struct {
	Host   string  `json:"host"`
	Stages []Stage `json:"stages"`
	// FailedAt names the first station that did not answer, empty when the
	// whole route worked.
	FailedAt string `json:"failed_at"`
	Class    Class  `json:"class"`
	Advice   string `json:"advice"`
}

// OK reports whether the whole route worked.
func (d Diagnosis) OK() bool { return d.FailedAt == "" }

// Doctor walks a route one station at a time and stops at the first one that
// fails. opt.Addr should hold the destination's own address so that a failure
// there is not mistaken for a failure at a station.
//
// This is the difference between a tool and a script. "Connection failed" tells
// you nothing when the route is a chain of three machines; naming the station
// that refused, and not testing past it, tells you where to go and look.
func Doctor(ctx context.Context, host string, chain model.JumpChain, opt Options) Diagnosis {
	d := Diagnosis{Host: host}

	if len(chain.Cycle) > 0 {
		d.FailedAt = chain.Cycle[len(chain.Cycle)-1]
		d.Class = Config
		d.Advice = "ProxyJump loop: " + strings.Join(chain.Cycle, " -> ") +
			". ssh does not report this, it hangs, so tram refuses the route instead."
		d.Stages = append(d.Stages, Stage{Name: d.FailedAt, Kind: "jump", Class: Config, Detail: "ProxyJump loop"})
		return d
	}

	// Each station is tested by name. A station's own ProxyJump is whatever
	// precedes it in the route, so probing them in order tests exactly one new
	// leg at a time without tram having to assemble a partial chain by hand.
	failed := false
	for _, hop := range chain.Hops {
		st := Stage{Name: hop.Spec, Kind: "jump"}
		if failed {
			st.Skipped = true
			st.Class = Unknown
			d.Stages = append(d.Stages, st)
			continue
		}
		if !hop.Known {
			st.Class = Config
			st.Detail = "not a configured host; ssh will treat it as a literal address"
		}
		hopOpt := opt
		hopOpt.Addr = hop.Addr
		r := Run(ctx, hop.Host, hopOpt)
		st.Class, st.Detail, st.Millis = r.Class, r.Detail, r.Millis
		d.Stages = append(d.Stages, st)
		if !r.Class.Good() {
			failed = true
			d.FailedAt, d.Class = hop.Spec, r.Class
			d.Advice = adviceFor(r.Class, hop.Host)
		}
	}

	st := Stage{Name: host, Kind: "destination"}
	if failed {
		st.Skipped = true
		st.Class = Unknown
		d.Stages = append(d.Stages, st)
		return d
	}
	r := Run(ctx, host, opt)
	st.Class, st.Detail, st.Millis = r.Class, r.Detail, r.Millis
	d.Stages = append(d.Stages, st)
	if !r.Class.Good() {
		d.FailedAt, d.Class = host, r.Class
		d.Advice = adviceFor(r.Class, host)
	}
	return d
}

func adviceFor(c Class, host string) string {
	switch c {
	case Auth:
		return fmt.Sprintf("%s answered but rejected the credentials. Check User and IdentityFile with `tram ls %s`.", host, host)
	case Refused:
		return fmt.Sprintf("%s refused the port. sshd is probably not running, or Port is wrong.", host)
	case Timeout:
		return fmt.Sprintf("Nothing answered from %s. A firewall, a wrong address, or the machine is down.", host)
	case DNS:
		return fmt.Sprintf("The name behind %s does not resolve. Set HostName to an address that does.", host)
	case HostKey:
		return fmt.Sprintf("The host key for %s is unknown or has changed. Verify it yourself before trusting it.", host)
	case Config:
		return "ssh rejected the configuration. Run `tram doctor --config`."
	}
	return ""
}

// ---- configuration check --------------------------------------------------

// Finding is one problem found in the configuration itself.
type Finding struct {
	Severity string `json:"severity"` // error, warning
	Where    string `json:"where"`
	Message  string `json:"message"`
}

// ConfigInput is what CheckConfig needs, gathered by the caller so that this
// package does not have to know about tram's inventory.
type ConfigInput struct {
	Files      []ConfigFile
	Hosts      []model.Host
	Duplicates map[string][]string
	// LoadErrors are the problems the parser reported, such as a missing
	// include or a loop between files.
	LoadErrors []string
}

// ConfigFile is one file in the tree.
type ConfigFile struct {
	Path        string
	IncludeLine int // 1-based line of the first Include, 0 when there is none
	FirstHost   int // 1-based line of the first Host stanza, 0 when there is none
}

// CheckConfig looks for the mistakes that make ssh behave in ways people find
// hard to explain: an Include below a stanza that shadows it, a ProxyJump to a
// name that does not exist, a loop, a key that is missing or world-readable,
// and a host declared twice where only the first declaration has any effect.
func CheckConfig(in ConfigInput) []Finding {
	var out []Finding
	add := func(sev, where, msg string) { out = append(out, Finding{sev, where, msg}) }

	for _, e := range in.LoadErrors {
		add("error", "include", e)
	}

	for _, f := range in.Files {
		if st, err := os.Stat(f.Path); err == nil {
			if runtime.GOOS != "windows" && st.Mode().Perm()&0o022 != 0 {
				add("error", f.Path, fmt.Sprintf("permissions are %04o; ssh ignores a configuration that others can write", st.Mode().Perm()))
			}
		}
		if f.IncludeLine > 0 && f.FirstHost > 0 && f.IncludeLine > f.FirstHost {
			add("warning", fmt.Sprintf("%s:%d", f.Path, f.IncludeLine),
				"Include comes after a stanza; ssh keeps the first value it sees, so settings above can shadow everything the included file says")
		}
	}

	for name, files := range in.Duplicates {
		add("warning", strings.Join(files, ", "),
			fmt.Sprintf("host %q is declared more than once; ssh uses the first value for each keyword, so the later stanzas are mostly dead", name))
	}

	lookup := func(n string) (model.Host, bool) {
		for _, h := range in.Hosts {
			if strings.EqualFold(h.Name, n) {
				return h, true
			}
			for _, a := range h.Aliases {
				if strings.EqualFold(a, n) {
					return h, true
				}
			}
		}
		return model.Host{}, false
	}

	seenCycle := map[string]bool{}
	for _, h := range in.Hosts {
		where := fmt.Sprintf("%s:%d", h.File, h.Line)
		for _, spec := range model.SplitJump(h.ProxyJump) {
			hop := model.ParseJumpSpec(spec)
			if _, ok := lookup(hop.Host); !ok && !looksLikeAddress(hop.Host) {
				add("warning", where, fmt.Sprintf("%s has ProxyJump %s, which names no configured host", h.Name, hop.Host))
			}
		}
		if chain := model.ResolveChain(h.Name, lookup); len(chain.Cycle) > 0 {
			// Every host on a loop discovers the same loop, so key the finding
			// on the set of stations rather than on where the walk started.
			ring := make([]string, 0, len(chain.Cycle))
			for _, c := range chain.Cycle {
				ring = append(ring, strings.ToLower(c))
			}
			sort.Strings(ring)
			key := strings.Join(uniq(ring), ">")
			if !seenCycle[key] {
				seenCycle[key] = true
				add("error", where, fmt.Sprintf("ProxyJump loop: %s. ssh hangs on this rather than reporting an error.", strings.Join(chain.Cycle, " -> ")))
			}
		}
		for _, k := range h.IdentityFiles {
			p := secret.ExpandKeyPath(k)
			if strings.ContainsAny(p, "%") {
				continue // a token ssh expands at connect time
			}
			info, err := secret.InspectKey(p)
			switch {
			case err != nil && !info.Exists:
				add("warning", where, fmt.Sprintf("%s has IdentityFile %s, which does not exist", h.Name, k))
			case info.PermsTooOpen:
				add("error", where, fmt.Sprintf("%s is readable by others; ssh refuses to use it", p))
			}
		}
	}
	return out
}

func looksLikeAddress(s string) bool {
	return strings.Contains(s, ".") || strings.Contains(s, ":")
}

// KnownHostsPath returns the file ssh records host keys in, for reporting.
func KnownHostsPath(sshDir string) string { return filepath.Join(sshDir, "known_hosts") }

var _ = time.Second

// uniq removes adjacent duplicates from a sorted slice.
func uniq(ss []string) []string {
	out := ss[:0]
	for i, s := range ss {
		if i == 0 || ss[i-1] != s {
			out = append(out, s)
		}
	}
	return out
}
