// Package launcher builds the ssh command line and hands the terminal over to
// it. tram never draws the contents of a session: it assembles argv, steps out
// of the way, and cleans up after ssh exits.
package launcher

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hieuny/tram/internal/model"
)

// Request describes one session tram is about to open.
type Request struct {
	// Host is the name as it appears in ssh_config. Passing the name rather
	// than an expanded address is deliberate: ssh reads its own configuration,
	// so nothing tram knows has to be duplicated on the command line.
	Host string

	// ConfigPath overrides the configuration file, used when tram itself was
	// pointed at a non-default one.
	ConfigPath string

	// Command runs non-interactively and then exits, for `tram host -- cmd`.
	Command []string

	// ForceTTY asks ssh for a terminal even when running a command, which is
	// what an interactive remote program needs.
	ForceTTY bool
	// NoTTY suppresses terminal allocation, for machine-readable output.
	NoTTY bool

	// Askpass points ssh at tram's own askpass helper for this session.
	Askpass AskpassSetup

	// ConnectTimeout in seconds, zero for ssh's default.
	ConnectTimeout int

	// Extra are additional ssh arguments, inserted before the host name.
	Extra []string
}

// AskpassSetup carries what ssh needs to ask tram for a secret without the
// secret ever appearing on a command line or in an environment variable.
type AskpassSetup struct {
	// Enabled turns the helper on for this session.
	Enabled bool
	// Binary is the path to tram itself, which ssh re-invokes in askpass mode.
	Binary string
	// Token identifies which secret to serve, and is meaningless on its own.
	Token string
	// Force sets SSH_ASKPASS_REQUIRE=force so that ssh uses the helper even
	// when it has a real terminal and would otherwise prompt directly.
	Force bool
}

// Argv builds the ssh command line for a request.
func (r Request) Argv() []string {
	argv := []string{"ssh"}
	if r.ConfigPath != "" {
		argv = append(argv, "-F", r.ConfigPath)
	}
	if r.ConnectTimeout > 0 {
		argv = append(argv, "-o", "ConnectTimeout="+itoa(r.ConnectTimeout))
	}
	switch {
	case r.ForceTTY:
		argv = append(argv, "-t")
	case r.NoTTY:
		argv = append(argv, "-T")
	}
	argv = append(argv, r.Extra...)
	argv = append(argv, r.Host)
	argv = append(argv, r.Command...)
	return argv
}

// Env returns the environment ssh should run with, which differs from tram's
// own only when the askpass helper is in play.
func (r Request) Env() []string {
	env := os.Environ()
	if !r.Askpass.Enabled {
		return env
	}
	env = setEnv(env, "SSH_ASKPASS", r.Askpass.Binary)
	env = setEnv(env, "TRAM_ASKPASS_TOKEN", r.Askpass.Token)
	if r.Askpass.Force {
		// Without this ssh prefers to prompt on the terminal it already has,
		// and the helper is never called. It needs OpenSSH 8.4 or newer.
		env = setEnv(env, "SSH_ASKPASS_REQUIRE", "force")
	}
	// ssh on some platforms only consults SSH_ASKPASS when DISPLAY is set.
	if r.Askpass.Force && getEnv(env, "DISPLAY") == "" {
		env = setEnv(env, "DISPLAY", "tram")
	}
	return env
}

func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

func getEnv(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// SFTPArgv builds the argv for handing a host to sftp. sftp reads the same
// ssh_config, so again only the name is passed.
func SFTPArgv(host, configPath string) []string {
	argv := []string{"sftp"}
	if configPath != "" {
		argv = append(argv, "-F", configPath)
	}
	return append(argv, host)
}

// SelfPath returns the path of the running tram binary, which ssh needs when
// tram is used as an askpass helper.
func SelfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "tram"
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// Describe renders an argv the way a shell would show it, for `tram args` and
// for the line tram prints before handing over.
func Describe(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"'") {
			parts[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
			continue
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

// JumpPreflight reports why a host cannot be opened, or "" when it can.
//
// The check that earns its place here is the loop check: ssh given a circular
// ProxyJump does not fail, it hangs, so refusing up front is the only way the
// user learns what is wrong.
func JumpPreflight(chain model.JumpChain) string {
	if len(chain.Cycle) > 0 {
		return "ProxyJump loop: " + strings.Join(chain.Cycle, " -> ") + "; ssh would hang instead of failing"
	}
	if len(chain.Unknown) > 0 {
		return "ProxyJump names " + strings.Join(chain.Unknown, ", ") + ", which is not a configured host"
	}
	return ""
}
