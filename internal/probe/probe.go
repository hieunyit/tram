// Package probe decides what went wrong with an ssh connection.
//
// It exists as one package because ping, doctor and the dashboard all need the
// same answer, and three separate implementations of "read ssh's stderr and
// guess" drift apart until the same failure is reported three different ways.
// The classification lives here and nowhere else.
package probe

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Class is what kind of failure a connection attempt hit. The point of naming
// them is that each one has a different fix: a refused connection is a firewall
// or a stopped daemon, a denied one is a key or a user name, and a timeout is
// usually neither.
type Class string

const (
	// OK means the connection opened and a command ran.
	OK Class = "OK"
	// Auth means the host answered and rejected the credentials.
	Auth Class = "AUTH"
	// Refused means something answered on the port and said no, which usually
	// means sshd is not running rather than that the network is broken.
	Refused Class = "REFUSED"
	// Timeout means nothing answered in time.
	Timeout Class = "TIMEOUT"
	// DNS means the name did not resolve, so nothing was even attempted.
	DNS Class = "DNS"
	// HostKey means the host's key is unknown or has changed. tram never
	// answers that question for you.
	HostKey Class = "HOST_KEY"
	// Jump means the failure happened on the way, at a jump station rather
	// than at the destination.
	Jump Class = "JUMP"
	// Config means ssh rejected the configuration before connecting.
	Config Class = "CONFIG"
	// Unknown means ssh failed in a way tram does not recognise, and the raw
	// message is shown rather than flattened into a lie.
	Unknown Class = "UNKNOWN"
)

// Good reports whether the class represents a working connection.
func (c Class) Good() bool { return c == OK }

// Explain returns a one-line description of what the class means.
func (c Class) Explain() string {
	switch c {
	case OK:
		return "connected and ran a command"
	case Auth:
		return "the host answered and rejected the credentials"
	case Refused:
		return "the port answered and refused; sshd is probably not running"
	case Timeout:
		return "nothing answered in time; a firewall or a down host"
	case DNS:
		return "the name did not resolve"
	case HostKey:
		return "the host key is unknown or has changed"
	case Jump:
		return "a jump station on the way failed"
	case Config:
		return "ssh rejected the configuration"
	}
	return "ssh failed in a way tram does not recognise"
}

// Result is one probe of one host.
type Result struct {
	Host    string        `json:"host"`
	Class   Class         `json:"class"`
	Latency time.Duration `json:"-"`
	Millis  int64         `json:"ms"`
	// Hop names the station the failure happened at, empty when it was the
	// destination itself.
	Hop string `json:"hop"`
	// Detail is the line of ssh's own output that led to the classification.
	Detail string `json:"detail"`
	// Raw is everything ssh wrote, kept so that an Unknown result is still
	// actionable.
	Raw string `json:"raw"`
	// Output is the remote command's standard output, for exec.
	Output string `json:"output"`
	// ExitCode is the remote command's status, or 255 for an ssh failure.
	ExitCode int `json:"exit_code"`
}

// Options controls how a probe is run.
type Options struct {
	ConfigPath string
	// Timeout bounds the whole attempt, including the jump chain.
	Timeout time.Duration
	// Command runs instead of the default no-op. exec supplies one.
	Command []string
	// Jump overrides the host's own ProxyJump, used by doctor to test one leg
	// of a route at a time.
	Jump string
	// Addr is the address the destination resolves to. ssh names addresses
	// rather than host names in its failure lines, so without this a failure at
	// the destination reads as a failure at a jump station.
	Addr string
	// Extra are additional ssh options.
	Extra []string

	// Env is the environment ssh runs with, and Helper says that it arms
	// tram's askpass helper in the mode that answers from this run's cache or
	// not at all.
	//
	// The two come as a pair because of jump stations. ssh reaches a ProxyJump
	// host by starting a second ssh, and hands that one -F and -v but not -o
	// BatchMode, so the second ssh would stop and ask for a key passphrase on
	// the terminal. It does inherit the environment. With the helper forced,
	// every question on every hop goes to tram and none to the terminal, and
	// BatchMode is left off so that a passphrase tram does have can be given.
	Env    []string
	Helper bool
}

// classifiers map a substring of ssh's output to a class. Order matters: the
// first match wins, and the more specific patterns come first.
var classifiers = []struct {
	re    *regexp.Regexp
	class Class
}{
	{regexp.MustCompile(`(?i)REMOTE HOST IDENTIFICATION HAS CHANGED|host key verification failed|no matching host key|key_verify failed`), HostKey},
	{regexp.MustCompile(`(?i)could not resolve hostname|name or service not known|nodename nor servname|temporary failure in name resolution|no address associated`), DNS},
	{regexp.MustCompile(`(?i)connection refused`), Refused},
	{regexp.MustCompile(`(?i)connection timed out|operation timed out|timed out while waiting|connect to host .* port .*: connection timed out`), Timeout},
	{regexp.MustCompile(`(?i)permission denied|too many authentication failures|no supported authentication methods|authentication failed`), Auth},
	{regexp.MustCompile(`(?i)network is unreachable|no route to host|host is down`), Timeout},
	{regexp.MustCompile(`(?i)bad configuration option|unsupported option|line \d+: `), Config},
	{regexp.MustCompile(`(?i)kex_exchange_identification|connection closed by remote host|connection reset by peer|banner exchange`), Refused},
}

// jumpMarker recognises the wording ssh uses when the failure happened while
// setting up a jump rather than at the destination.
var jumpMarker = regexp.MustCompile(`(?i)^(.*: )?(ssh: )?connect to host (\S+)`)

// Classify reads ssh's output and names the failure.
//
// The stderr of a failed ssh run usually contains several lines, only one of
// which says what actually happened. Classify picks the most specific line
// rather than the last one, and keeps it as the detail so that the class is
// always traceable back to something ssh really said.
func Classify(out string, exitCode int) (Class, string) {
	if exitCode == 0 {
		return OK, ""
	}
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for _, c := range classifiers {
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if c.re.MatchString(l) {
				return c.class, l
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return Unknown, l
		}
	}
	return Unknown, ""
}

// HopOf extracts the host named in a failure line when that host is somewhere
// other than the destination, which is how tram tells a jump station's failure
// from the destination's.
//
// ssh writes the address it dialled, not the name from the configuration, so
// the destination's own address has to be recognised too. Without that, every
// refused connection to a host with a HostName would be blamed on a jump
// station that was never involved.
func HopOf(detail string, target ...string) string {
	m := jumpMarker.FindStringSubmatch(detail)
	if m == nil {
		return ""
	}
	named := strings.Trim(m[3], "[]")
	for _, t := range target {
		if t != "" && strings.EqualFold(named, strings.Trim(t, "[]")) {
			return ""
		}
	}
	return named
}

// Run probes one host and returns a classified result.
//
// BatchMode is forced on so that ssh fails instead of stopping to ask for a
// password. A probe that blocks on a prompt is not a probe.
func Run(ctx context.Context, host string, opt Options) Result {
	args := Args(host, opt)

	// ssh's own ConnectTimeout covers the TCP connect and nothing else. Name
	// resolution happens before it and can take seconds of its own, so tram's
	// outer deadline is deliberately looser: killing ssh a moment before it
	// prints "could not resolve hostname" would report a timeout and hide the
	// actual cause.
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout+10*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if opt.Env != nil {
		cmd.Env = opt.Env
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	elapsed := time.Since(start)

	res := Result{Host: host, Latency: elapsed, Millis: elapsed.Milliseconds(), Output: stdout.String(), Raw: strings.TrimSpace(stderr.String())}
	if err == nil {
		res.Class = OK
		return res
	}
	if ee, ok := err.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
	} else {
		res.ExitCode = 255
	}
	if ctx.Err() == context.DeadlineExceeded {
		// ssh may already have said what was wrong before tram killed it. Its
		// own words are better than a generic timeout, so they win.
		if c, detail := Classify(res.Raw, res.ExitCode); c != Unknown {
			res.Class, res.Detail = c, detail
			return res
		}
		res.Class = Timeout
		res.Detail = "tram gave up after " + elapsed.Round(time.Millisecond).String()
		return res
	}
	// An exit status other than 255 came from the remote command, which means
	// the connection itself worked.
	if res.ExitCode != 0 && res.ExitCode != 255 && len(opt.Command) > 0 {
		res.Class = OK
		return res
	}
	res.Class, res.Detail = Classify(res.Raw, res.ExitCode)
	res.Hop = HopOf(res.Detail, host, opt.Addr)
	if res.Hop != "" {
		res.Class = Jump
	}
	return res
}

// Args builds the ssh command line for a probe.
func Args(host string, opt Options) []string {
	var args []string
	if opt.ConfigPath != "" {
		args = append(args, "-F", opt.ConfigPath)
	}
	// BatchMode keeps ssh from asking anything of the terminal, but only ssh
	// itself: the second ssh a jump station needs never sees it. With the helper
	// armed there is no need for it, and it would stop a cached passphrase from
	// ever being handed over, because in batch mode ssh skips an encrypted key
	// without asking anyone.
	if !opt.Helper {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args, "-o", "StrictHostKeyChecking=accept-new")

	secs := int(opt.Timeout.Seconds())
	if secs < 1 {
		secs = 10
	}
	args = append(args, "-o", "ConnectTimeout="+itoa(secs))
	if opt.Jump != "" {
		args = append(args, "-J", opt.Jump)
	}
	args = append(args, opt.Extra...)
	args = append(args, "-T", host)
	if len(opt.Command) > 0 {
		return append(args, opt.Command...)
	}
	return append(args, "true")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
